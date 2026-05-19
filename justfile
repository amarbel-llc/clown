default: build test check

# Throwaway investigation recipes live in zz-explore/justfile. Invoke as
# `just explore <recipe>`. Optional so the file can be deleted without
# breaking the parent justfile.
mod? explore 'zz-explore/justfile'

# Aggregator: run every test recipe (Go unit tests + plugin-host
# integration tests). Moxy-dependent tests skip cleanly when moxy is not
# on PATH, so this recipe is safe to run from any environment.
test: test-go test-plugin-host test-stdio-bridge test-plugin-host-moxy test-plugin-host-moxy-disabled

# Aggregator: run every check recipe (currently: mandoc lint on
# clown-authored man pages). Non-test correctness gates belong here.
check: check-lint-man

# Format the tree via treefmt (config: treefmt.nix). Forwards args, e.g.
# `just fmt --ci` to fail if anything would change.
fmt *ARGS:
    nix fmt -- {{ARGS}}

build: build-nix

# Build Go binaries
[group("go")]
build-go:
    go build ./cmd/...

# Run Go tests across the whole module (internal + cmd packages).
[group("go")]
test-go:
    go test ./...

# Regenerate gomod2nix.toml after go.mod changes (uses the gomod2nix
# binary from the devshell so the tool version matches the nix builder).
[group("go")]
gomod2nix:
    gomod2nix generate

# Integration test: launch clown-stdio-bridge wrapping a mock stdio
# MCP server. Verifies handshake/healthcheck and the streamable-HTTP
# MCP translation path. Runs the full bats suite via the nix sandbox
# lane (bats.lib.${system}.batsLane from amarbel-llc/bats);
# stdio_bridge.bats is one of the files it executes.
[group("test")]
test-stdio-bridge:
    nix build .#bats-default --no-link --print-build-logs

# Host-side smoke tests for the FDR-0007 tent (C+F + B shape, the
# 2026-05-19 update). Excluded from the standard bats lane because
# the nix sandbox can't run rootless podman; this recipe is the only
# path to exercise the wired-up tent image + bind mounts + SSH socket
# forwarding end-to-end.
#
# Side effects:
#   - Builds .#tent-image and `podman load`s it (idempotent — podman
#     skips the load if the tag is already present).
#   - The `ssh -T git@github.com` test makes outbound :22 to GitHub
#     and uses the host's ssh-agent / pivy-agent. Set
#     CLOWN_TENT_NO_NETWORK=1 to skip it offline.
#
# Linux-only (rootless podman). On darwin, point the recipe at the
# podman-machine VM by exporting CONTAINER_HOST=ssh://… before invoking.
[group("test")]
test-tent-smoke:
    #!/usr/bin/env bash
    set -euo pipefail

    root="$(git rev-parse --show-toplevel)"
    cd "$root"

    if ! command -v podman >/dev/null 2>&1; then
        echo "FAIL: podman not on PATH" >&2
        exit 2
    fi

    # Ensure the tent image is loaded. nix build prints the tarball
    # store path; podman load is idempotent against an already-loaded
    # tag, so re-runs are cheap.
    echo "tent-smoke: ensuring tent-image is built and loaded..."
    tarball="$(nix build --no-link --print-out-paths .#tent-image 2>/dev/null)"
    if [[ -z "$tarball" || ! -e "$tarball" ]]; then
        echo "FAIL: nix build .#tent-image produced no out-path" >&2
        exit 2
    fi
    podman load -i "$tarball" >/dev/null

    # bats-libs is exposed as a flake output (flake.nix's packages.<system>.bats-libs).
    # Its share/bats subdir is what BATS_LIB_PATH wants — common.bash's
    # `bats_load_library` calls resolve through there. Same derivation
    # the sandboxed batsLane uses, so semantics match.
    bats_libs_store="$(nix build --no-link --print-out-paths .#bats-libs)"
    if [[ -z "$bats_libs_store" ]]; then
        echo "FAIL: nix build .#bats-libs produced no out-path" >&2
        exit 2
    fi
    export BATS_LIB_PATH="$bats_libs_store/share/bats"

    # Pull bats from nixpkgs at the pinned input. The host devshell
    # doesn't carry bats today (it's normally invoked inside the
    # sandbox lane); ensuring it here keeps the recipe self-contained.
    bats_bin="$(nix build --no-link --print-out-paths 'nixpkgs#bats')/bin/bats"
    if [[ ! -x "$bats_bin" ]]; then
        echo "FAIL: nix build nixpkgs#bats did not produce a bats binary" >&2
        exit 2
    fi

    echo "tent-smoke: running bats against zz-tests_bats/tent_smoke.bats"
    "$bats_bin" zz-tests_bats/tent_smoke.bats

# Integration test: launch clown-plugin-host with the synthetic plugin's
# clown.json and verify URL-based MCP compilation, name preservation,
# and agents field passthrough. Runs the full bats suite via the nix
# sandbox lane; plugin_host.bats is one of the files it executes.
[group("test")]
test-plugin-host:
    nix build .#bats-default --no-link --print-build-logs

# Build clown-cover and emit the bats-suite coverage profile to
# result/coverage.out. Distinct from `go test -cover` (unit
# reachability) — this measures what zz-tests_bats/* exercises through
# the real CLI against -cover-instrumented binaries.
[group("test")]
cover-bats:
    nix build .#clown-cover --no-link --print-build-logs

# Build clown-cover and open the HTML coverage report. Falls back
# to printing the path if no $BROWSER is available.
[group("test")]
cover-bats-html:
    #!/usr/bin/env bash
    set -euo pipefail
    nix build .#clown-cover
    profile=$(readlink -f result)/coverage.out
    if [[ -n "${BROWSER:-}" ]]; then
        go tool cover -html="$profile"
    else
        echo "coverage profile: $profile"
        echo "open with: go tool cover -html=$profile"
    fi

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

# Probe which claude invocation surfaces .mcp.json schema errors. Used
# while picking the integration form for #14. Findings (claude-code
# 2.1.111):
#   - `--mcp-config <a> <b>` (variadic) validates each file. If any
#     file fails (schema or not-found), claude prints "Error: Invalid
#     MCP configuration:" plus per-file diagnostics and exits.
#   - `--mcp-config=FILE mcp list` does NOT surface schema errors —
#     `mcp list` lists user-scoped servers regardless of file content.
#     Likely silently drops invalid entries.
#   - `--strict-mcp-config --mcp-config=FILE mcp list` also does not
#     surface schema errors (same reason).
# So the working form: `--mcp-config FILE NONEXISTENT_FILE` — claude
# emits both errors, the schema marker is the discriminator.
[group("explore")]
explore-claude-mcp-config-parsing:
    #!/usr/bin/env bash
    set -u
    cfg=$(mktemp /tmp/clown-probe-mcp-XXXXXX.json)
    bogus=/tmp/clown-probe-bogus-$$.json
    trap 'rm -f "$cfg" "$bogus"' EXIT
    echo ">> bare (schema-invalid)"
    cat > "$cfg" <<'EOF'
    {"mcpServers":{"test/server":{"url":"http://127.0.0.1:42323/mcp"}}}
    EOF
    timeout 5s claude --mcp-config "$cfg" "$bogus" 2>&1 || true
    echo ">> typed-http (schema-valid)"
    cat > "$cfg" <<'EOF'
    {"mcpServers":{"test/server":{"type":"http","url":"http://127.0.0.1:42323/mcp"}}}
    EOF
    timeout 5s claude --mcp-config "$cfg" "$bogus" 2>&1 || true
    echo ">> typed-sse (schema-valid)"
    cat > "$cfg" <<'EOF'
    {"mcpServers":{"test/server":{"type":"sse","url":"http://127.0.0.1:42323/sse"}}}
    EOF
    timeout 5s claude --mcp-config "$cfg" "$bogus" 2>&1 || true

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

# End-to-end smoke against a real llama-server + a real GGUF.
# Exercises the full ringmaster -> launcher -> llama-cpp -> circus
# control path, plus a /v1/messages request to the spawned instance.
# Refuses to run if a daemon is already up (we don't want to step
# on the user's home-manager-managed daemon).
#
# Usage: just smoke-ringmaster [model-name]
# Default model: gemma3-1b (smallest GGUF in ~/.local/share/circus/models)
[group("test")]
smoke-ringmaster MODEL="gemma3-1b": build
    #!/usr/bin/env bash
    set -euo pipefail
    model="{{MODEL}}"
    gguf="$HOME/.local/share/circus/models/${model}.gguf"
    if [[ ! -f "$gguf" ]]; then
        echo "FAIL: model not found at $gguf" >&2
        echo "      installed models:" >&2
        ls "$HOME/.local/share/circus/models/" 2>/dev/null | sed 's/^/        /' >&2
        exit 1
    fi
    sock="$HOME/.local/state/circus/control.sock"
    if [[ -S "$sock" ]] && ./result/bin/circus list >/dev/null 2>&1; then
        echo "FAIL: a ringmaster daemon is already running on $sock" >&2
        echo "      stop it first (launchctl unload / kill) so this smoke" >&2
        echo "      doesn't step on home-manager state" >&2
        exit 1
    fi
    rm -f "$sock"
    log=$(mktemp)
    trap 'set +e; ./result/bin/circus stop "$model" >/dev/null 2>&1 || true; [[ -n "${rm_pid:-}" ]] && kill "$rm_pid" 2>/dev/null; wait 2>/dev/null; rm -f "$log"' EXIT
    echo ">> launching ringmaster (log: $log)"
    ./result/bin/ringmaster daemon >"$log" 2>&1 &
    rm_pid=$!
    for i in $(seq 1 50); do
        [[ -S "$sock" ]] && break
        sleep 0.1
    done
    if [[ ! -S "$sock" ]]; then
        echo "FAIL: ringmaster never bound $sock" >&2
        cat "$log" >&2
        exit 1
    fi
    echo ">> ringmaster up"
    echo
    echo "=== circus models ==="
    ./result/bin/circus models
    echo
    echo "=== circus list (expect empty) ==="
    ./result/bin/circus list
    echo
    echo "=== circus start $model (may take several seconds for llama-cpp to warm up) ==="
    ./result/bin/circus start "$model"
    echo
    echo "=== circus list (expect one row) ==="
    ./result/bin/circus list
    echo
    echo "=== circus status $model ==="
    ./result/bin/circus status "$model"
    echo
    port=$(./result/bin/circus list | awk -v a="$model" '$1==a {print $4}')
    if [[ -z "$port" ]]; then
        echo "FAIL: could not parse port from circus list" >&2
        exit 1
    fi
    echo "=== POST /v1/messages on 127.0.0.1:$port ==="
    resp=$(curl -sS -X POST "http://127.0.0.1:$port/v1/messages" \
        -H 'Content-Type: application/json' \
        -d '{"model":"'"$model"'","max_tokens":64,"messages":[{"role":"user","content":"Say only the word READY."}]}')
    echo "$resp"
    if ! echo "$resp" | grep -q '"text"'; then
        echo "FAIL: /v1/messages response missing a content text field" >&2
        exit 1
    fi
    echo
    echo "=== circus stop $model ==="
    ./result/bin/circus stop "$model"
    echo
    echo "=== circus list (expect empty again) ==="
    ./result/bin/circus list
    echo
    echo "OK: ringmaster + llama-server + circus round-trip succeeded"

# Empirical probe — can we coax real llama-server far enough that
# ringmaster's /health poll succeeds WITHOUT a GGUF? Times --version
# exit latency, --help exit latency, and how long /health takes to
# come up under various model-less invocations. Used as the source
# of evidence behind cmd/ringmaster/launcher_real_test.go and the
# decision that option B is feasible. Re-run after nixpkgs-llama
# bumps to confirm the behaviour still holds.
#
# Empirical results last captured 2026-05-19:
#   * --version exits in <100 ms (after Metal init lines on macOS)
#   * --help exits the same way
#   * launch with no --model enters "router mode" and serves /health
#     in ~350 ms. /v1/messages won't work without a model, but
#     /health does.
#
# Run `just build` first.
[group("debug")]
debug-probe-real-llama-server:
    #!/usr/bin/env bash
    set -u
    # Read the burned-in LlamaServerPath out of ./result/bin/ringmaster
    # — buildcfg.LlamaServerPath is a Go string literal, so it appears
    # verbatim in the binary's string table.
    LS=$(strings ./result/bin/ringmaster \
        | grep -E '^/nix/store/[^/]+-llama-cpp[^/]*/bin/llama-server$' \
        | head -1)
    if [[ -z "$LS" || ! -x "$LS" ]]; then
        echo "FATAL: could not resolve a runnable llama-server path" >&2
        echo "       extracted: '$LS'" >&2
        exit 1
    fi
    echo "Burned-in llama-server: $LS"
    echo
    echo "=== probe 1: --version (wallclock, 30s cap) ==="
    time timeout 30 "$LS" --version 2>&1 | head -20
    echo "exit: ${PIPESTATUS[0]}"
    echo
    echo "=== probe 2: --help (wallclock, 30s cap) ==="
    time timeout 30 "$LS" --help 2>&1 | head -5
    echo "(help text truncated; exit: ${PIPESTATUS[0]})"
    echo
    echo "=== probe 3: launch with no model, poll /health up to 15s ==="
    logf=$(mktemp)
    trap 'rm -f "$logf"' EXIT
    "$LS" --port 38201 --host 127.0.0.1 >"$logf" 2>&1 &
    pid=$!
    started_at=$(python3 -c 'import time; print(time.time())')
    ok=
    for i in $(seq 1 75); do
        sleep 0.2
        if curl -sSf -o /dev/null --max-time 1 http://127.0.0.1:38201/health 2>/dev/null; then
            now=$(python3 -c 'import time; print(time.time())')
            dt=$(python3 -c "print(f'{$now - $started_at:.2f}')")
            echo "  /health came up after ${dt}s (poll #$i)"
            ok=yes
            break
        fi
        if ! kill -0 "$pid" 2>/dev/null; then
            now=$(python3 -c 'import time; print(time.time())')
            dt=$(python3 -c "print(f'{$now - $started_at:.2f}')")
            echo "  process exited after ${dt}s without serving /health"
            break
        fi
    done
    if [[ "$ok" == "yes" ]]; then
        kill "$pid" 2>/dev/null
        wait "$pid" 2>/dev/null
        echo "  shut down cleanly"
    elif kill -0 "$pid" 2>/dev/null; then
        echo "  process still alive after 15s, /health never came up — killing"
        kill "$pid" 2>/dev/null
        wait "$pid" 2>/dev/null
    fi
    echo
    echo "=== llama-server stderr (first 30 lines) ==="
    head -30 "$logf"
    echo "==="
    echo
    echo "Summary:"
    echo "  /health-without-model: ${ok:-NO}"

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
    git tag -s -m {{quote(message)}} "$tag"
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
