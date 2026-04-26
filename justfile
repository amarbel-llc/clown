default: build test check

# Aggregator: run every test recipe (Go unit tests + plugin-host
# integration tests). Moxy-dependent tests skip cleanly when moxy is not
# on PATH, so this recipe is safe to run from any environment.
test: test-go test-plugin-host test-stdio-bridge test-plugin-host-moxy test-plugin-host-moxy-disabled

# Aggregator: run every check recipe (currently: mandoc lint on
# clown-authored man pages). Non-test correctness gates belong here.
check: check-lint-man

build: build-nix

# Build Go binaries
[group("go")]
build-go:
    go build ./cmd/...

# Run Go tests across the whole module (internal + cmd packages).
[group("go")]
test-go:
    go test ./...

# Build the mock MCP server used by integration tests
[group("go")]
build-mock-server:
    go build -o tests/synthetic-plugin/bin/mock-mcp-server ./internal/pluginhost/testdata/mockserver

# Build the mock stdio MCP server used by clown-stdio-bridge integration tests.
[group("go")]
build-mock-stdio-mcp:
    go build -o tests/synthetic-plugin/bin/mock-stdio-mcp ./internal/pluginhost/testdata/mockstdiomcp

# Regenerate gomod2nix.toml after go.mod changes (uses the gomod2nix
# binary from the devshell so the tool version matches the nix builder).
[group("go")]
gomod2nix:
    gomod2nix generate

# Integration test: launch clown-stdio-bridge wrapping a mock stdio
# MCP server. Verifies the handshake/healthcheck path (skeleton-level)
# AND the streamable-HTTP MCP translation path: client POSTs an
# `initialize` request and receives a JSON response with the matching
# id; client triggers a server-initiated notification via a
# `notify-broadcast` request and observes it on the GET SSE stream.
[group("test")]
test-stdio-bridge: build build-mock-stdio-mcp
    #!/usr/bin/env bash
    set -euo pipefail
    bin="./result/bin/clown-stdio-bridge"
    mock="$(pwd)/tests/synthetic-plugin/bin/mock-stdio-mcp"
    echo "Launching $bin --command $mock"
    handshake_file=$(mktemp)
    log_file=$(mktemp)
    trap 'rm -f "$handshake_file" "$log_file"; kill "$bridge_pid" 2>/dev/null || true' EXIT
    "$bin" --command "$mock" -- >"$handshake_file" 2>"$log_file" &
    bridge_pid=$!
    for _ in $(seq 1 30); do
        if [[ -s "$handshake_file" ]]; then break; fi
        sleep 0.1
    done
    handshake=$(head -n1 "$handshake_file")
    if [[ -z "$handshake" ]]; then
        echo "FAIL: bridge produced no handshake within 3 s" >&2
        cat "$log_file" >&2
        exit 1
    fi
    echo "handshake: $handshake"
    if ! grep -qE '^1\|1\|tcp\|127\.0\.0\.1:[0-9]+\|streamable-http$' <<<"$handshake"; then
        echo "FAIL: handshake does not match expected format" >&2
        exit 1
    fi
    addr=$(awk -F'|' '{print $4}' <<<"$handshake")
    base="http://$addr"

    # /healthz baseline.
    healthz_status=$(curl -s -o /dev/null -w '%{http_code}' "$base/healthz")
    if [[ "$healthz_status" != "200" ]]; then
        echo "FAIL: /healthz returned $healthz_status, want 200" >&2
        exit 1
    fi
    echo "OK: /healthz returned 200"

    # Round-trip: POST an initialize request, expect a JSON response
    # with the same id.
    init_resp=$(curl -sS -X POST "$base/mcp" \
        -H 'Content-Type: application/json' \
        -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}')
    if ! grep -q '"id":1' <<<"$init_resp"; then
        echo "FAIL: initialize response missing id=1: $init_resp" >&2
        exit 1
    fi
    if ! grep -q 'mock-stdio-mcp' <<<"$init_resp"; then
        echo "FAIL: initialize response missing serverInfo.name: $init_resp" >&2
        exit 1
    fi
    echo "OK: initialize round-trip succeeded"

    # tools/list: one mock tool.
    tools_resp=$(curl -sS -X POST "$base/mcp" \
        -H 'Content-Type: application/json' \
        -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')
    if ! grep -q '"name":"echo"' <<<"$tools_resp"; then
        echo "FAIL: tools/list response missing echo tool: $tools_resp" >&2
        exit 1
    fi
    echo "OK: tools/list round-trip succeeded"

    # SSE broadcast: open a GET SSE stream in the background, trigger
    # a server-initiated notification via the mock's notify-broadcast
    # method, observe the notification on the SSE stream.
    sse_out=$(mktemp)
    trap 'rm -f "$handshake_file" "$log_file" "$sse_out"; kill "$bridge_pid" 2>/dev/null || true' EXIT
    curl -sNS "$base/mcp" >"$sse_out" 2>/dev/null &
    sse_pid=$!
    sleep 0.2
    curl -sS -X POST "$base/mcp" \
        -H 'Content-Type: application/json' \
        -d '{"jsonrpc":"2.0","id":3,"method":"notify-broadcast"}' >/dev/null
    deadline=$(($(date +%s) + 3))
    while [[ $(date +%s) -lt $deadline ]]; do
        if grep -q 'tools/list_changed' "$sse_out" 2>/dev/null; then break; fi
        sleep 0.1
    done
    kill "$sse_pid" 2>/dev/null || true
    wait "$sse_pid" 2>/dev/null || true
    if ! grep -q 'tools/list_changed' "$sse_out"; then
        echo "FAIL: SSE stream did not receive notifications/tools/list_changed" >&2
        echo "--- sse_out ---" >&2
        cat "$sse_out" >&2
        echo "--- bridge log ---" >&2
        cat "$log_file" >&2
        exit 1
    fi
    echo "OK: SSE broadcast received server-initiated notification"

    kill -TERM "$bridge_pid"
    wait "$bridge_pid" 2>/dev/null || true
    echo "OK: clown-stdio-bridge integration test passed"

# Integration test: launch clown-plugin-host with the synthetic plugin's
# clown.json and verify the mock HTTP MCP server starts, completes the
# handshake, passes health checks, compiles plugin manifests with
# URL-based MCP entries, and preserves original server names.
[group("test")]
test-plugin-host: build build-mock-server
    #!/usr/bin/env bash
    set -euo pipefail
    plugin_dir="$(pwd)/tests/synthetic-plugin"
    echo "Starting clown-plugin-host with synthetic plugin..."
    # The inspect-compiled helper extracts the compiled plugin.json
    # before clown-plugin-host cleans up the staging dir on shutdown.
    output=$(timeout 30 ./result/bin/clown-plugin-host \
        --plugin-dir "$plugin_dir" \
        -- "$plugin_dir/bin/inspect-compiled" 2>&1) || {
        echo "FAIL: clown-plugin-host exited with $?" >&2
        echo "$output" >&2
        exit 1
    }
    echo "$output"
    # Verify the downstream received a compiled --plugin-dir
    if echo "$output" | grep -qE 'COMPILED_PLUGIN_DIR=.*/clown-plugin-compile-'; then
        echo "OK: downstream received compiled --plugin-dir"
    else
        echo "FAIL: downstream did not receive a clown-plugin-compile-* --plugin-dir path" >&2
        exit 1
    fi
    # Extract the compiled plugin.json and verify injected mcpServers
    compiled_json=$(echo "$output" | sed -n '/COMPILED_PLUGIN_JSON_START/,/COMPILED_PLUGIN_JSON_END/p' \
        | grep -v 'COMPILED_PLUGIN_JSON_')
    if [[ -z "$compiled_json" ]]; then
        echo "FAIL: could not extract compiled plugin.json from output" >&2
        exit 1
    fi
    echo "Compiled plugin.json:"
    echo "$compiled_json"
    # Server name must be "mock-mcp" (original clown.json key), not renamed
    if echo "$compiled_json" | jq -e '.mcpServers["mock-mcp"]' >/dev/null 2>&1; then
        echo "OK: mcpServers contains 'mock-mcp' (original server name preserved)"
    else
        echo "FAIL: mcpServers does not contain 'mock-mcp' key" >&2
        exit 1
    fi
    # Entry must be url-based (type + url), not command-based
    entry_type=$(echo "$compiled_json" | jq -r '.mcpServers["mock-mcp"].type')
    entry_url=$(echo "$compiled_json" | jq -r '.mcpServers["mock-mcp"].url')
    if [[ "$entry_type" == "http" ]]; then
        echo "OK: type is 'http'"
    else
        echo "FAIL: type = '$entry_type', want 'http'" >&2
        exit 1
    fi
    if [[ "$entry_url" =~ ^http://127\.0\.0\.1:[0-9]+/mcp$ ]]; then
        echo "OK: url matches http://127.0.0.1:<port>/mcp pattern"
    else
        echo "FAIL: url = '$entry_url', want http://127.0.0.1:<port>/mcp" >&2
        exit 1
    fi
    # Original command-based entry must be gone
    if echo "$compiled_json" | jq -e '.mcpServers["mock-mcp"].command' >/dev/null 2>&1; then
        echo "FAIL: command field still present in compiled entry" >&2
        exit 1
    fi
    echo "OK: no command field in compiled entry"
    # Other fields (name, agents) must survive compilation
    if echo "$compiled_json" | jq -e '.name == "synthetic-test"' >/dev/null 2>&1; then
        echo "OK: name field preserved"
    else
        echo "FAIL: name field lost or changed" >&2
        exit 1
    fi
    if echo "$compiled_json" | jq -e '.agents | length > 0' >/dev/null 2>&1; then
        echo "OK: agents field preserved"
    else
        echo "FAIL: agents field lost" >&2
        exit 1
    fi
    echo "OK: plugin-host integration test passed"

# Integration test: launch clown-plugin-host with the real moxy MCP server as
# a plugin, exercising the clown-plugin-protocol against a production server
# instead of the synthetic mock. Moxy must already be on $PATH; its plugin
# dir is derived as <prefix>/share/purse-first/moxy.
#
# Skipped unconditionally: moxy is a downstream consumer of clown
# (consumers wire it via lib.mkCircus). Validating clown-plugin-host
# against a downstream artifact is a layering violation. Set
# CLOWN_RUN_DOWNSTREAM_TESTS=1 to opt back in for local debugging.
[group("test")]
test-plugin-host-moxy: build
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ "${CLOWN_RUN_DOWNSTREAM_TESTS:-0}" != "1" ]]; then
        echo "SKIP: test-plugin-host-moxy depends on the downstream moxy plugin;"
        echo "      set CLOWN_RUN_DOWNSTREAM_TESTS=1 to opt in."
        exit 0
    fi
    if ! moxy_bin=$(command -v moxy 2>/dev/null); then
        echo "SKIP: moxy not found on PATH — skipping plugin-host-moxy integration test"
        exit 0
    fi
    moxy_prefix=$(dirname "$(dirname "$moxy_bin")")
    plugin_dir="$moxy_prefix/share/purse-first/moxy"
    echo "Using moxy at: $moxy_bin"
    echo "Plugin dir:    $plugin_dir"
    if [[ ! -f "$plugin_dir/clown.json" ]]; then
        echo "SKIP: $plugin_dir/clown.json is missing."
        echo "      Your moxy on PATH is too old; update it to a version that"
        echo "      ships share/purse-first/moxy/clown.json. Skipping test."
        exit 0
    fi
    echo "Starting clown-plugin-host with moxy as the plugin..."
    output=$(timeout 60 ./result/bin/clown-plugin-host \
        --verbose \
        --plugin-dir "$plugin_dir" \
        -- echo DOWNSTREAM_MARKER 2>&1) || {
        echo "FAIL: clown-plugin-host exited with $?" >&2
        echo "$output" >&2
        exit 1
    }
    echo "$output"
    # NB: use heredoc string instead of `echo "$output" | grep -q`. With
    # `set -o pipefail`, grep -q exits early on first match, echo dies
    # SIGPIPE, and the pipeline returns 141 — making the if-condition
    # read as false even though grep matched. Was issue #23.
    if grep -q 'DOWNSTREAM_MARKER' <<<"$output"; then
        echo "OK: downstream received its original args"
    else
        echo "FAIL: downstream did not receive original args" >&2
        exit 1
    fi
    # No assertion on plugin server stderr here: clown-plugin-host
    # captures plugin stderr to its log file but does not mirror to the
    # terminal. The "compiled --plugin-dir" check below independently
    # confirms that the host discovered and managed the moxy server.
    # Regression guard for the plugin.json compilation path: the downstream
    # --plugin-dir must point at a clown-plugin-compile-* staging dir (the
    # exact parent varies with $TMPDIR), not the source plugin_dir.
    if grep -qE -- '--plugin-dir[ =][^ ]*/clown-plugin-compile-' <<<"$output"; then
        echo "OK: downstream received compiled --plugin-dir"
    else
        echo "FAIL: downstream did not receive a clown-plugin-compile-* --plugin-dir path; compilation did not run" >&2
        exit 1
    fi
    if grep -qE -- "--plugin-dir[ =]$plugin_dir( |$)" <<<"$output"; then
        echo "FAIL: downstream received ORIGINAL --plugin-dir ($plugin_dir); compilation should have substituted it" >&2
        exit 1
    fi
    echo "OK: plugin-host-moxy integration test passed"

# Like test-plugin-host-moxy but exercises --disable-clown-protocol. The
# flag is expected to skip discovery, HTTP server launch, and plugin
# manifest compilation, so the downstream should see the original
# --plugin-dir path (uncompiled).
#
# Skipped unconditionally for the same downstream-layering reason as
# test-plugin-host-moxy. Set CLOWN_RUN_DOWNSTREAM_TESTS=1 to opt back
# in for local debugging.
[group("test")]
test-plugin-host-moxy-disabled: build
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ "${CLOWN_RUN_DOWNSTREAM_TESTS:-0}" != "1" ]]; then
        echo "SKIP: test-plugin-host-moxy-disabled depends on the downstream moxy plugin;"
        echo "      set CLOWN_RUN_DOWNSTREAM_TESTS=1 to opt in."
        exit 0
    fi
    if ! moxy_bin=$(command -v moxy 2>/dev/null); then
        echo "SKIP: moxy not found on PATH — skipping plugin-host-moxy-disabled integration test"
        exit 0
    fi
    moxy_prefix=$(dirname "$(dirname "$moxy_bin")")
    plugin_dir="$moxy_prefix/share/purse-first/moxy"
    echo "Using plugin dir: $plugin_dir"
    output=$(timeout 30 ./result/bin/clown-plugin-host \
        --disable-clown-protocol \
        --plugin-dir "$plugin_dir" \
        -- echo DOWNSTREAM_MARKER 2>&1) || {
        echo "FAIL: clown-plugin-host exited with $?" >&2
        echo "$output" >&2
        exit 1
    }
    echo "$output"
    # See note in test-plugin-host-moxy on `grep -q` + pipefail (#23).
    if grep -q -- "--plugin-dir $plugin_dir" <<<"$output"; then
        echo "OK: downstream received original --plugin-dir (pass-through)"
    else
        echo "FAIL: downstream did not receive --plugin-dir $plugin_dir" >&2
        exit 1
    fi
    if grep -q 'clown-plugin-compile-' <<<"$output"; then
        echo "FAIL: plugin manifest compilation ran despite --disable-clown-protocol" >&2
        exit 1
    fi
    echo "OK: plugin-host-moxy-disabled integration test passed"

build-nix:
    nix build --show-trace

# Build the stamped clown-manpages store path (with @MDOCDATE@
# substituted for the flake's lastModifiedDate) and echo where it
# landed. Useful as a prerequisite for docs recipes.
[group("docs")]
build-man:
    #!/usr/bin/env bash
    set -euo pipefail
    nix build --show-trace .#clown-manpages --out-link result-man
    echo "built: $(readlink -f result-man)"

# Render a single manpage as utf8 through mandoc to preview how it
# looks. Accepts either a source path (man/man1/clown-plugin-host.1)
# or a built path (result-man/share/man/man1/clown-plugin-host.1).
[group("docs")]
render-man PAGE:
    nix shell nixpkgs#mandoc -c mandoc -Tutf8 {{PAGE}}

# Lint mdoc(7) manpages with mandoc -Tlint. Operates on the built pages
# so @MDOCDATE@ has already been substituted, meaning we lint what
# actually ships.
#
# Scope: only clown-authored pages (clown*). Upstream pages we repackage
# (claude-code*, codex*) carry other copyrights and have pre-existing
# mandoc warnings that we don't own.
[group("check")]
check-lint-man: build-man
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(readlink -f result-man)
    pages=(
        "$out"/share/man/man1/clown*.1
        "$out"/share/man/man5/clown*.5
        "$out"/share/man/man7/clown*.7
    )
    nix shell nixpkgs#mandoc -c bash -c '
        failed=0
        for page in "$@"; do
            [[ -f "$page" ]] || continue
            if ! mandoc -Tlint -Wwarning "$page"; then
                failed=1
            fi
        done
        exit $failed
    ' _ "${pages[@]}"

# Feed a hand-crafted .mcp.json into claude to see whether its schema
# validator accepts it. MODE is one of: bare (just "url"), typed-http
# ("type":"http","url"), typed-sse. Useful for reproducing the
# mcpServers schema error seen when clown-plugin-host ships the config.
[group("explore")]
explore-mcp-config MODE:
    #!/usr/bin/env bash
    set -u
    cfg=$(mktemp /tmp/clown-repro-mcp-XXXXXX.json)
    trap 'rm -f "$cfg"' EXIT
    case "{{MODE}}" in
        bare)
            cat > "$cfg" <<'EOF'
    {"mcpServers":{"moxy/moxy":{"url":"http://127.0.0.1:42323/mcp"}}}
    EOF
            ;;
        typed-http)
            cat > "$cfg" <<'EOF'
    {"mcpServers":{"moxy/moxy":{"type":"http","url":"http://127.0.0.1:42323/mcp"}}}
    EOF
            ;;
        typed-sse)
            cat > "$cfg" <<'EOF'
    {"mcpServers":{"moxy/moxy":{"type":"sse","url":"http://127.0.0.1:42323/sse"}}}
    EOF
            ;;
        *) echo "MODE must be bare|typed-http|typed-sse" >&2 ; exit 2 ;;
    esac
    echo ">> wrote $cfg:"
    cat "$cfg"
    echo
    echo ">> claude --mcp-config $cfg mcp list (exit code reported)"
    claude --mcp-config "$cfg" mcp list 2>&1 | head -40 || true
    echo ">> exit=$?"

# Smoke-test the --skip-failed / CLOWN_SKIP_FAILED_PLUGINS / no-opt-in
# branches using a pre-built .tmp/bad-plugin that points at a nonexistent
# binary. MODE is one of: none | flag | env. Append "+v" to turn on
# --verbose (e.g. `just explore-skip-failed flag+v`). Assumes the plugin
# dir already exists (created by hand for now).
[group("explore")]
explore-skip-failed MODE: build
    #!/usr/bin/env bash
    set -u
    plugin_dir="$(pwd)/.tmp/bad-plugin"
    if [[ ! -f "$plugin_dir/clown.json" ]]; then
        echo "FAIL: $plugin_dir/clown.json not found. Create the bad-plugin fixture first." >&2
        exit 2
    fi
    mode="{{MODE}}"
    verbose=
    if [[ "$mode" == *"+v" ]]; then
        verbose=--verbose
        mode="${mode%+v}"
    fi
    env_skip=
    args=()
    case "$mode" in
        none)        ;;
        flag)        args+=(--skip-failed) ;;
        env)         env_skip="CLOWN_SKIP_FAILED_PLUGINS=1" ;;
        *) echo "MODE must be none|flag|env (optionally with +v)" >&2 ; exit 2 ;;
    esac
    [[ -n "$verbose" ]] && args+=("$verbose")
    echo ">> mode={{MODE}} args=${args[*]:-(none)} env=${env_skip:-(none)}"
    set -x
    env $env_skip ./result/bin/clown-plugin-host \
        --plugin-dir "$plugin_dir" \
        "${args[@]}" \
        -- echo DOWNSTREAM_MARKER
    exit_code=$?
    set +x
    echo ">> exit=$exit_code"

clean:
    rm -rf result

# Build clown with moxy + bob plugins via mkCircus, using the local worktree
# as the clown input. Only evaluates clown/moxy/bob — not the full eng flake.
build-circus *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    root=$(git rev-parse --show-toplevel)
    nix build --show-trace {{ARGS}} --impure --expr "
      let
        clown = builtins.getFlake \"path:$root\";
        moxy  = builtins.getFlake \"github:amarbel-llc/moxy\";
        bob   = builtins.getFlake \"github:amarbel-llc/bob\";
        system = builtins.currentSystem;
        circus = clown.lib.\${system}.mkCircus {
          plugins = [
            { flake = moxy; dirs = [ \"share/purse-first/moxy\" ]; }
            { flake = bob;  dirs = [ \"share/purse-first/*\" ]; }
          ];
        };
      in circus.packages.default
    "

# Build and exec clown with plugins (mkCircus).
run-circus *ARGS:
    just build-circus
    exec ./result/bin/clown {{ARGS}}

# Verify plugin agents appear in `claude agents list` using the in-repo
# synthetic test plugin.
[group("test")]
test-plugin-agents: build
    #!/usr/bin/env bash
    set -euo pipefail
    plugin_dir="$(pwd)/tests/synthetic-plugin"
    if [[ ! -f "$plugin_dir/.claude-plugin/plugin.json" ]]; then
        echo "FAIL: synthetic plugin manifest missing" >&2
        exit 1
    fi
    agents_output=$(./result/bin/clown --naked --plugin-dir "$plugin_dir" \
        agents list 2>&1) || true
    echo "$agents_output"
    echo "---"
    failed=0
    for agent in yaml-test-agent toml-test-agent; do
        if echo "$agents_output" | grep -q "$agent"; then
            echo "OK: $agent loaded"
            echo "--- $agent details ---"
            ./result/bin/clown --naked --plugin-dir "$plugin_dir" \
                agents show "$agent" 2>&1 || true
        else
            echo "FAIL: $agent NOT loaded" >&2
            failed=1
        fi
    done
    exit $failed

# Probe the plugin.json "agents" field schema by trying different formats
# and reporting which ones pass validation. Requires a built clown.
[group("explore")]
explore-agents-schema: build
    #!/usr/bin/env bash
    set -euo pipefail
    plugin_dir="$(pwd)/tests/synthetic-plugin"
    manifest="$plugin_dir/.claude-plugin/plugin.json"
    base='{"name":"synthetic-test","version":"0.0.1","description":"probe"'
    cli="./result/bin/clown --naked --plugin-dir $plugin_dir plugin list"
    try_variant() {
        local label="$1" json="$2"
        echo "$json" > "$manifest"
        output=$($cli 2>&1) || true
        if echo "$output" | grep -q '✔ loaded'; then
            echo "OK   $label"
        elif echo "$output" | grep -q 'Invalid input'; then
            echo "FAIL $label  (Invalid input)"
        else
            echo "??   $label"
            echo "     $output" | head -3
        fi
    }
    echo "=== Probing agents field schema ==="
    try_variant 'no agents field'            "$base}"
    try_variant 'agents: {}'                 "$base,\"agents\":{}}"
    try_variant 'agents: []'                 "$base,\"agents\":[]}"
    try_variant 'agents: ["agents/*.md"]'    "$base,\"agents\":[\"agents/*.md\"]}"
    try_variant 'agents: ["./agents/yaml-agent"]' "$base,\"agents\":[\"./agents/yaml-agent\"]}"
    try_variant 'agents: ["./agents/yaml-agent.md"]' "$base,\"agents\":[\"./agents/yaml-agent.md\"]}"
    try_variant 'agents: "agents"'           "$base,\"agents\":\"agents\"}"
    try_variant 'agents: true'               "$base,\"agents\":true}"
    try_variant 'agents: {"yaml-test-agent":{"description":"test"}}' \
                "$base,\"agents\":{\"yaml-test-agent\":{\"description\":\"test\"}}}"
    # Restore clean manifest
    echo "$base}" > "$manifest"
    echo "=== Done ==="

# Bump all flake inputs and rebuild to verify
bump: && build
    nix flake update

# Smoke-test the built clown binary against a real Claude OAuth session.
# The passthru test (nix build .#checks.x86_64-linux.managedSettingsRead)
# is what actually proves the managed-settings path patch works. This recipe
# confirms the built binary launches end-to-end with no settings errors.
test-managed-live: build
    #!/usr/bin/env bash
    set -euo pipefail
    diag=$(mktemp)
    trap 'rm -f "$diag"' EXIT
    CLAUDE_CODE_DIAGNOSTICS_FILE="$diag" \
        ./result/bin/clown mcp list >/dev/null 2>&1 || true
    if [[ ! -s "$diag" ]]; then
        echo "FAIL: no diagnostics emitted — clown did not launch claude" >&2
        exit 1
    fi
    if ! grep -q '"settings_load_completed"' "$diag"; then
        echo "FAIL: settings_load_completed event missing" >&2
        cat "$diag" >&2
        exit 1
    fi
    if grep -q '"error_count":[1-9]' "$diag"; then
        echo "FAIL: claude reported settings parse errors" >&2
        cat "$diag" >&2
        exit 1
    fi
    echo "OK: clown launched claude, settings loaded without errors"
    echo "(For path-read proof, run: nix build .#checks.x86_64-linux.managedSettingsRead)"

# Tag a release. The "v" prefix is added for you, so pass the semver
# without it. Usage: just tag 0.1.0 "feat: managed settings burnin"
[group("maint")]
tag version message:
    #!/usr/bin/env bash
    set -euo pipefail
    tag="v{{version}}"
    prev=$(git tag --sort=-v:refname -l "v*" | head -1)
    if [[ -n "$prev" ]]; then
        gum log --level info "Previous: $prev"
        git log --oneline "$prev"..HEAD
    fi
    git tag -s -m "{{message}}" "$tag"
    gum log --level info "Created tag: $tag"
    git push origin "$tag"
    gum log --level info "Pushed $tag"
    git tag -v "$tag"

# Cut a release: assemble a changelog-style message from commits
# since the last v* tag, then call `tag` to sign, push, and verify.
# The "v" prefix is added for you, so pass the semver without it.
# Usage: just release 0.1.0
#
# Use `just tag <version> <message>` directly if you want full
# control over the tag message.
# Throwaway: launch ./result/bin/clown with an ad-hoc plugin dir
# supplied via the new --plugin-dir flag. The wrapper script in
# result/bin/clown sets CLOWN_PLUGIN_META at exec time, so the env-var
# route doesn't survive; the runtime flag is the right knob.
[group("debug")]
debug-clown-with-stdio-plugin PLUGIN_DIR=".tmp/chrest-plugin": build
    #!/usr/bin/env bash
    set -euo pipefail
    plugin_dir=$(realpath {{PLUGIN_DIR}})
    if [[ ! -f "$plugin_dir/clown.json" ]]; then
        echo "ERROR: $plugin_dir/clown.json not found" >&2
        exit 1
    fi
    echo "Launching ./result/bin/clown --verbose --plugin-dir $plugin_dir"
    exec ./result/bin/clown --verbose --plugin-dir "$plugin_dir"

# Manually exercise the stdio bridge against a real stdio MCP. Expects
# a plugin directory at $PLUGIN_DIR (default .tmp/stdio-bridge-plugin)
# containing clown.json (with stdioServers entries), the standard
# .claude-plugin/plugin.json, and a probe.sh helper that takes
# --plugin-dir, extracts the wrapped server URL from the compiled
# manifest, and exercises it.
[group("debug")]
debug-stdio-bridge-plugin PLUGIN_DIR=".tmp/stdio-bridge-plugin": build
    #!/usr/bin/env bash
    set -euo pipefail
    plugin_dir="{{PLUGIN_DIR}}"
    if [[ ! -f "$plugin_dir/clown.json" || ! -x "$plugin_dir/probe.sh" ]]; then
        echo "ERROR: $plugin_dir is missing clown.json or probe.sh" >&2
        exit 1
    fi
    ./result/bin/clown-plugin-host \
        --verbose \
        --plugin-dir "$plugin_dir" \
        -- "$plugin_dir/probe.sh"

# Run the moxy-dependent integration tests with the opt-in env var pre-set.
# Useful for verifying fixes to those recipes without typing the long form.
[group("debug")]
debug-run-downstream-tests:
    #!/usr/bin/env bash
    set -euo pipefail
    CLOWN_RUN_DOWNSTREAM_TESTS=1 just test-plugin-host-moxy
    CLOWN_RUN_DOWNSTREAM_TESTS=1 just test-plugin-host-moxy-disabled

# Probe a running llama-server for /v1/models and a test /v1/messages call.
# Restarts circus with the specified model (defaults to gemma3).
# Usage: just debug-circus-api [model-store-path]
[group("debug")]
debug-circus-api model="":
    #!/usr/bin/env bash
    set -euo pipefail
    nix build --show-trace .#circus -o result-circus
    echo "restarting circus..." >&2
    ./result-circus/bin/circus stop 2>/dev/null || true
    model_arg=""
    if [[ -n "{{model}}" ]]; then
        model_arg="--model {{model}}"
    fi
    ./result-circus/bin/circus start $model_arg </dev/tty >/dev/tty
    port_file="$HOME/.local/state/circus/llama-server.port"
    port=$(cat "$port_file")
    base="http://127.0.0.1:$port"
    echo "=== GET $base/v1/models ==="
    models_json=$(curl -sf "$base/v1/models")
    echo "$models_json" | jq .
    model_id=$(echo "$models_json" | jq -r '.data[0].id // .models[0].model // empty')
    echo ""
    echo "=== POST $base/v1/messages model=$model_id ==="
    curl -f "$base/v1/messages" \
        -H "Content-Type: application/json" \
        -H "x-api-key: dummy" \
        -d "{\"model\":\"$model_id\",\"max_tokens\":10,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}" \
        | jq .

[group("maint")]
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "{{version}}" > version.txt
    git add version.txt
    git commit -m "release v{{version}}"
    prev=$(git tag --sort=-v:refname -l "v*" | head -1)
    header="release v{{version}}"
    if [[ -n "$prev" ]]; then
        summary=$(git log --format='- %s' "$prev"..HEAD)
        msg="$header"$'\n\n'"$summary"
    else
        msg="$header"
    fi
    just tag "{{version}}" "$msg"
