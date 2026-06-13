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

# Multi-instance live smoke for ringmaster (FDR-0010 criterion 2). Real
# llama-server children, real circus CLI, real RPC. Two independent
# aliases load two different GGUFs, both serve /v1/messages
# concurrently on distinct ports, then both stop cleanly. Complements
# the fake-llama-server bats coverage in zz-tests_bats/ringmaster.bats
# by proving the same lifecycle survives a real, slow, multi-GB model
# load.
#
# Usage: just smoke-ringmaster-multi [model-a] [model-b]
# Defaults: gemma3-1b + qwen3-1.7b (both small, both already on disk).
[group("test")]
smoke-ringmaster-multi MODEL_A="gemma3-1b" MODEL_B="qwen3-1.7b": build
    #!/usr/bin/env bash
    set -euo pipefail
    a="{{MODEL_A}}"
    b="{{MODEL_B}}"
    for m in "$a" "$b"; do
        gguf="$HOME/.local/share/circus/models/${m}.gguf"
        if [[ ! -f "$gguf" ]]; then
            echo "FAIL: model not found at $gguf" >&2
            ls "$HOME/.local/share/circus/models/" 2>/dev/null | sed 's/^/        /' >&2
            exit 1
        fi
    done
    sock="$HOME/.local/state/circus/control.sock"
    if [[ -S "$sock" ]] && ./result/bin/circus list >/dev/null 2>&1; then
        echo "FAIL: a ringmaster daemon is already running on $sock" >&2
        echo "      stop it first so this smoke doesn't step on home-manager state" >&2
        exit 1
    fi
    rm -f "$sock"
    log=$(mktemp)
    trap 'set +e; ./result/bin/circus stop "$a" >/dev/null 2>&1 || true; ./result/bin/circus stop "$b" >/dev/null 2>&1 || true; [[ -n "${rm_pid:-}" ]] && kill "$rm_pid" 2>/dev/null; wait 2>/dev/null; rm -f "$log"' EXIT
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
    echo "=== circus start $a ==="
    ./result/bin/circus start "$a"
    echo
    echo "=== circus start $b ==="
    ./result/bin/circus start "$b"
    echo
    echo "=== circus list (expect two rows) ==="
    ./result/bin/circus list
    echo
    port_a=$(./result/bin/circus list | awk -v alias="$a" '$1==alias {print $4}')
    port_b=$(./result/bin/circus list | awk -v alias="$b" '$1==alias {print $4}')
    if [[ -z "$port_a" || -z "$port_b" ]]; then
        echo "FAIL: could not parse both ports from circus list" >&2
        exit 1
    fi
    if [[ "$port_a" == "$port_b" ]]; then
        echo "FAIL: both instances reported the same port $port_a" >&2
        exit 1
    fi
    echo ">> distinct ports: $a=$port_a $b=$port_b"
    echo
    for pair in "$a:$port_a" "$b:$port_b"; do
        m="${pair%%:*}"
        p="${pair##*:}"
        echo "=== POST /v1/messages on 127.0.0.1:$p ($m) ==="
        # max_tokens=256 to give chain-of-thought / reasoning models
        # (e.g. qwen3) room to finish a "thinking" block and emit a
        # final text block. accept either "text" or "thinking"
        # content as evidence the instance is serving inference.
        resp=$(curl -sS -X POST "http://127.0.0.1:$p/v1/messages" \
            -H 'Content-Type: application/json' \
            -d '{"model":"'"$m"'","max_tokens":256,"messages":[{"role":"user","content":"Say only the word READY."}]}')
        echo "$resp"
        if ! echo "$resp" | grep -qE '"text"|"thinking"'; then
            echo "FAIL: $m: /v1/messages response missing both text and thinking content" >&2
            exit 1
        fi
        echo
    done
    echo "=== circus stop $a ==="
    ./result/bin/circus stop "$a"
    echo
    echo "=== circus list (expect one row: $b only) ==="
    ./result/bin/circus list
    echo
    if ./result/bin/circus list | awk '{print $1}' | grep -qx "$a"; then
        echo "FAIL: $a still in list after stop" >&2
        exit 1
    fi
    if ! ./result/bin/circus list | awk '{print $1}' | grep -qx "$b"; then
        echo "FAIL: $b disappeared from list (should still be running)" >&2
        exit 1
    fi
    echo "=== circus stop $b ==="
    ./result/bin/circus stop "$b"
    echo
    echo "=== circus list (expect empty) ==="
    ./result/bin/circus list
    echo
    echo "OK: two concurrent ringmaster-managed llama-server instances round-tripped"

# Compute and probe the tailnet URL for a running circus instance.
# Resolves the alias to its port via `circus list`, the host to its
# MagicDNS name via `tailscale status --json`, then POSTs a short
# /v1/messages request (Anthropic-format — what claude-code talks to).
# Accepts both "text" and "thinking" content as evidence the instance
# is serving inference (qwen3-style reasoning models emit thinking
# blocks first).
#
# Validates ringmaster is reachable, the alias is registered, and the
# instance is bound somewhere tailnet-reachable. Warns (not fatal)
# when the instance is bound to a non-0.0.0.0 address — a tailnet IP
# (100.x.y.z) is also fine; loopback is not.
#
# Diagnostic only: this hits the Anthropic endpoint. Opencode/crush
# use the OpenAI endpoint at /v1/chat/completions; if claude-code
# works against this URL it does not automatically mean opencode does
# (different model templates handle tool-use differently — see #57
# for gemma3's known crush incompatibility).
#
# Pending FDR-0011 phase 2 (#87); will be replaced by `clown
# --backend=circus --circus-bind=…` which does this resolution
# internally.
#
# Usage: just smoke-tailnet-url [alias]
#   alias defaults to gemma3-12b
[group("test")]
smoke-tailnet-url ALIAS="gemma3-12b": build
    #!/usr/bin/env bash
    set -euo pipefail
    alias="{{ALIAS}}"
    if ! command -v tailscale >/dev/null; then
        echo "FAIL: tailscale CLI not on PATH" >&2
        exit 1
    fi
    if ! command -v jq >/dev/null; then
        echo "FAIL: jq not on PATH" >&2
        exit 1
    fi
    sock="${RINGMASTER_SOCKET:-$HOME/.local/state/circus/control.sock}"
    if [[ ! -S "$sock" ]]; then
        echo "FAIL: ringmaster socket not found at $sock" >&2
        echo "      start ringmaster first: ./result/bin/ringmaster daemon &" >&2
        exit 1
    fi
    port=$(./result/bin/circus list | awk -v a="$alias" '$1==a {print $4}')
    if [[ -z "$port" ]]; then
        echo "FAIL: alias \"$alias\" not registered with ringmaster" >&2
        ./result/bin/circus list >&2
        exit 1
    fi
    bind=$(./result/bin/circus list | awk -v a="$alias" '$1==a {print $3}')
    if [[ "$bind" != "0.0.0.0" ]]; then
        echo "WARNING: alias \"$alias\" is bound to $bind, not 0.0.0.0 — tailnet reach not guaranteed" >&2
    fi
    host=$(tailscale status --self --json | jq -r '.Self.DNSName' | sed 's/\.$//')
    if [[ -z "$host" ]]; then
        echo "FAIL: could not resolve this host's tailnet MagicDNS name" >&2
        exit 1
    fi
    url="http://${host}:${port}"
    echo "TAILNET_URL=${url}"
    echo
    echo "=== quick /v1/messages smoke ==="
    set +e
    resp=$(curl -sS --max-time 60 -X POST "${url}/v1/messages" \
        -H 'Content-Type: application/json' \
        -d '{"model":"'"$alias"'","max_tokens":32,"messages":[{"role":"user","content":"Say only READY."}]}')
    rc=$?
    set -e
    echo "$resp"
    if [[ $rc -ne 0 ]]; then
        echo "FAIL: curl rc=$rc (network unreachable? firewall? wrong tailnet name?)" >&2
        exit 1
    fi
    if ! echo "$resp" | grep -qE '"text"|"thinking"'; then
        echo "FAIL: response missing content text/thinking block" >&2
        exit 1
    fi
    echo
    echo "OK: tailnet URL is reachable and serving inference"
    echo "    URL: ${url}"

# Launch clown pointed at a tailnet-exposed circus instance.
#
# Mechanism: clown has no --backend=circus flag yet (FDR-0011 phase 2,
# tracked by #87), so this recipe sets the four env vars that
# claude-code reads natively:
#
#   ANTHROPIC_BASE_URL          tailnet URL of the llama-server instance
#   ANTHROPIC_AUTH_TOKEN=dummy  claude-code needs *some* auth string
#   ANTHROPIC_API_KEY=dummy     same; llama-server ignores both
#   ANTHROPIC_CUSTOM_MODEL_OPTION   bypass claude-code's model
#                                   allow-list (would otherwise reject
#                                   any non-anthropic model name)
#
# Then exec's clown with --model <alias> appended after `--` so it
# reaches claude-code unmodified.
#
# Why --naked by default: per FDR-0009, --naked is the documented
# escape hatch that bypasses plugin host + system prompt + safety
# defaults + profile picker. For diagnostic smokes against a local
# model this is the cleanest reproducer because nothing clown-side
# can interact with the slow model warm-up. Pass NAKED=0 to exercise
# the full pipeline.
#
# Tool calls: claude-code talks to llama-server's /v1/messages
# (Anthropic-format). Most open GGUFs are not trained on Anthropic's
# tool-call format, so tool use will degrade to prose narration. For
# tool use, prefer smoke-opencode-against-tailnet /
# smoke-crush-against-tailnet (OpenAI-format) against a tool-trained
# model like qwen2.5-coder-7b.
#
# Usage: just smoke-clown-against-tailnet [alias] [naked]
#   alias defaults to gemma3-12b
#   naked=1 (default): --naked
#   naked=0:           full clown pipeline
[group("test")]
smoke-clown-against-tailnet ALIAS="gemma3-12b" NAKED="1": build
    #!/usr/bin/env bash
    set -euo pipefail
    alias="{{ALIAS}}"
    naked="{{NAKED}}"
    if ! command -v tailscale >/dev/null; then
        echo "FAIL: tailscale CLI not on PATH" >&2
        exit 1
    fi
    if ! command -v jq >/dev/null; then
        echo "FAIL: jq not on PATH" >&2
        exit 1
    fi
    sock="${RINGMASTER_SOCKET:-$HOME/.local/state/circus/control.sock}"
    if [[ ! -S "$sock" ]]; then
        echo "FAIL: ringmaster socket not found at $sock" >&2
        echo "      start ringmaster first: ./result/bin/ringmaster daemon &" >&2
        exit 1
    fi
    port=$(./result/bin/circus list | awk -v a="$alias" '$1==a {print $4}')
    if [[ -z "$port" ]]; then
        echo "FAIL: alias \"$alias\" not registered with ringmaster" >&2
        ./result/bin/circus list >&2
        exit 1
    fi
    host=$(tailscale status --self --json | jq -r '.Self.DNSName' | sed 's/\.$//')
    url="http://${host}:${port}"
    echo ">> pointing clown at: ${url}"
    echo ">> model alias:        ${alias}"
    naked_flag=""
    if [[ "$naked" == "1" ]]; then
        naked_flag="--naked"
        echo ">> mode:               --naked (no plugins, no system prompt)"
    else
        echo ">> mode:               full pipeline"
    fi
    echo
    exec env \
        ANTHROPIC_BASE_URL="${url}" \
        ANTHROPIC_AUTH_TOKEN=dummy \
        ANTHROPIC_API_KEY=dummy \
        ANTHROPIC_CUSTOM_MODEL_OPTION='{"model":"'"$alias"'","max_tokens":2048}' \
        ./result/bin/clown $naked_flag -- --model "$alias"

# Launch opencode pointed at a tailnet-exposed circus instance.
#
# Why the file dance: clown's runOpencode dispatch is (1) --profile
# with backend=gateway → use prof.URL/Token, (2) --profile with
# backend=local → read the (broken since FDR-0010) portfile, (3) no
# --profile → read ~/.config/circus/opencode.toml for url+token.
# Path 3 is the only one that works without code changes. Clown
# transforms that URL into an `@ai-sdk/openai-compatible` provider
# entry in a temp opencode.json and points opencode at it via
# OPENCODE_CONFIG, so all opencode↔llama-server traffic goes through
# /v1/chat/completions (OpenAI format).
#
# Lifecycle: backs up any existing ~/.config/circus/opencode.toml to
# <file>.bak-$$, writes the synthesized one for this run, restores on
# EXIT via a bash trap (including Ctrl-C / SIGINT). Two concurrent
# invocations are racy because the backup name is PID-suffixed but
# only one file slot exists — don't launch two in parallel against
# the same user.
#
# Tool calls: opencode passes tool definitions in OpenAI's
# tools[].function shape and parses tool_calls[]. A tool-use-trained
# model (qwen2.5-coder-7b is the smallest one that fits M2 Pro
# 16GB — `just download-qwen-coder` to fetch) will actually attempt
# the calls. Generalist instruct models (gemma3-*) will mostly
# narrate them in prose.
#
# Pending FDR-0011 phase 2 (#87) + #86 (flexible registry): will be
# replaced by `clown --provider=opencode --backend=circus
# --circus-alias=<x> --circus-bind=…` which sources the URL from
# ringmaster RPC directly and doesn't touch the user's TOML.
#
# Usage: just smoke-opencode-against-tailnet [alias]
#   alias defaults to gemma3-12b
[group("test")]
smoke-opencode-against-tailnet ALIAS="gemma3-12b": build
    #!/usr/bin/env bash
    set -euo pipefail
    alias="{{ALIAS}}"
    if ! command -v tailscale >/dev/null || ! command -v jq >/dev/null; then
        echo "FAIL: tailscale or jq not on PATH" >&2
        exit 1
    fi
    sock="${RINGMASTER_SOCKET:-$HOME/.local/state/circus/control.sock}"
    if [[ ! -S "$sock" ]]; then
        echo "FAIL: ringmaster socket not found at $sock" >&2
        exit 1
    fi
    port=$(./result/bin/circus list | awk -v a="$alias" '$1==a {print $4}')
    if [[ -z "$port" ]]; then
        echo "FAIL: alias \"$alias\" not registered with ringmaster" >&2
        ./result/bin/circus list >&2
        exit 1
    fi
    host=$(tailscale status --self --json | jq -r '.Self.DNSName' | sed 's/\.$//')
    # llama-server's OpenAI-compatible endpoint is /v1/{chat/completions,models,...}
    # The TOML field is the base URL; opencode appends path segments itself.
    url="http://${host}:${port}/v1"
    cfg="$HOME/.config/circus/opencode.toml"
    cfg_dir="$(dirname "$cfg")"
    mkdir -p "$cfg_dir"
    bak=""
    if [[ -f "$cfg" ]]; then
        bak="${cfg}.bak-$$"
        mv "$cfg" "$bak"
        echo ">> backed up existing $cfg → $bak"
    fi
    trap '
        set +e
        rm -f "'"$cfg"'"
        if [[ -n "'"$bak"'" && -f "'"$bak"'" ]]; then
            mv "'"$bak"'" "'"$cfg"'"
            echo ">> restored $cfg" >&2
        fi
    ' EXIT
    {
        echo "# Synthesized by 'just smoke-opencode-against-tailnet'."
        echo "# Pointing opencode at the tailnet-exposed circus instance."
        echo "url = \"${url}\""
        echo "token = \"local\""
    } >"$cfg"
    echo ">> pointing opencode at: ${url}"
    echo ">> model alias:          ${alias}"
    echo
    # Bare --provider, no --profile — falls through to readOpencodeLocalConfig.
    ./result/bin/clown --provider=opencode -- --model "$alias"

# Launch crush pointed at a tailnet-exposed circus instance.
#
# Same shape as smoke-opencode-against-tailnet but for crush:
#
#   - reads ~/.config/circus/crush.toml (not opencode.toml)
#   - clown writes a CRUSH_GLOBAL_CONFIG dir with a synthesized
#     crush.json containing one provider of type=openai-compat
#     pointed at the tailnet URL
#   - crush talks OpenAI to /v1/chat/completions, same wire shape as
#     opencode
#
# Same backup-and-restore lifecycle (trap on EXIT). Same model-quality
# caveat — use a tool-use-trained model (qwen2.5-coder-7b is the
# smallest tested one) for working tool calls. gemma3 specifically
# has a known Jinja chat-template incompatibility with crush's
# message-alternation pattern; see #57.
#
# Pending FDR-0011 phase 2 (#87): will be replaced by `clown
# --provider=crush --backend=circus …`.
#
# Usage: just smoke-crush-against-tailnet [alias]
#   alias defaults to gemma3-12b
[group("test")]
smoke-crush-against-tailnet ALIAS="gemma3-12b": build
    #!/usr/bin/env bash
    set -euo pipefail
    alias="{{ALIAS}}"
    if ! command -v tailscale >/dev/null || ! command -v jq >/dev/null; then
        echo "FAIL: tailscale or jq not on PATH" >&2
        exit 1
    fi
    sock="${RINGMASTER_SOCKET:-$HOME/.local/state/circus/control.sock}"
    if [[ ! -S "$sock" ]]; then
        echo "FAIL: ringmaster socket not found at $sock" >&2
        exit 1
    fi
    port=$(./result/bin/circus list | awk -v a="$alias" '$1==a {print $4}')
    if [[ -z "$port" ]]; then
        echo "FAIL: alias \"$alias\" not registered with ringmaster" >&2
        ./result/bin/circus list >&2
        exit 1
    fi
    host=$(tailscale status --self --json | jq -r '.Self.DNSName' | sed 's/\.$//')
    url="http://${host}:${port}/v1"
    cfg="$HOME/.config/circus/crush.toml"
    cfg_dir="$(dirname "$cfg")"
    mkdir -p "$cfg_dir"
    bak=""
    if [[ -f "$cfg" ]]; then
        bak="${cfg}.bak-$$"
        mv "$cfg" "$bak"
        echo ">> backed up existing $cfg → $bak"
    fi
    trap '
        set +e
        rm -f "'"$cfg"'"
        if [[ -n "'"$bak"'" && -f "'"$bak"'" ]]; then
            mv "'"$bak"'" "'"$cfg"'"
            echo ">> restored $cfg" >&2
        fi
    ' EXIT
    {
        echo "# Synthesized by 'just smoke-crush-against-tailnet'."
        echo "# Pointing crush at the tailnet-exposed circus instance."
        echo "url = \"${url}\""
        echo "token = \"local\""
    } >"$cfg"
    echo ">> pointing crush at: ${url}"
    echo ">> model alias:       ${alias}"
    echo
    ./result/bin/clown --provider=crush -- --model "$alias"

# Swap to the dev-loop podman-machine `clown-dev`. Stops
# podman-machine-default (the eng-managed VM) to free the VM slot
# (podman on darwin is single-VM), then initializes / starts
# clown-dev. The eng default is unavailable until `dev-tent-down`
# restarts it. See AGENTS.md § Dev loop for tent for the full story.
[group("test")]
dev-tent-up:
    nix run .#dev-tent-machine-up

# Swap back from clown-dev to the eng-managed default. Stops and
# removes clown-dev, then restarts podman-machine-default. Safe to
# run when no clown-dev machine exists.
[group("test")]
dev-tent-down:
    nix run .#dev-tent-machine-down

# Print BOTH machines' states (clown-dev and podman-machine-default).
# Helpful for confirming which one currently owns the VM slot.
[group("test")]
dev-tent-status:
    nix run .#dev-tent-machine-status

# Extract the macOS Keychain `Claude Code-credentials` entry to
# ~/.claude/.credentials.json so claude inside the tent (Linux, no
# Keychain access) can authenticate. claude on darwin stores OAuth
# tokens in the Keychain; on Linux it falls back to the JSON file at
# this well-known path, which is bind-mounted into the tent via
# `~/.claude`. Will trigger a Touch ID / password prompt the first
# time and on every key rotation. Re-run when in-tent claude starts
# reporting "Not logged in".
#
# This recipe is `debug-` prefixed because it's expected to be
# transient: once the auth story has a proper home (clown#100's
# home-manager module activation hook, or upstream claude gaining a
# CLAUDE_CREDENTIALS_FILE env var that points at a host path), this
# manual step disappears.
[group("test")]
debug-extract-claude-credentials:
    #!/usr/bin/env bash
    set -euo pipefail
    out="$HOME/.claude/.credentials.json"
    mkdir -p "$(dirname "$out")"
    if ! security find-generic-password -s 'Claude Code-credentials' -w >"$out"; then
        echo "FAIL: could not read 'Claude Code-credentials' keychain entry" >&2
        rm -f "$out"
        exit 1
    fi
    chmod 600 "$out"
    bytes=$(wc -c <"$out" | tr -d ' ')
    echo ">> extracted $bytes bytes to $out"
    head -c 200 "$out"
    echo
    echo ">> first 200 bytes printed above; if it looks like JSON, you're good"

# End-to-end dev-tent smoke: swap to clown-dev, build .#dev, run
# `clown --tent -- --version`. Pass DOWN=1 to swap back at the end.
[group("test")]
smoke-dev-tent DOWN="0":
    #!/usr/bin/env bash
    set -euo pipefail
    echo ">> swapping to clown-dev (stops podman-machine-default)"
    nix run .#dev-tent-machine-up
    echo
    echo ">> building .#dev (clown binary targeting clown-dev)"
    nix build .#dev
    echo
    echo ">> running clown --tent -- --version"
    ./result/bin/clown --tent -- --version
    echo
    if [[ "{{DOWN}}" = "1" ]]; then
        echo ">> swapping back to podman-machine-default"
        nix run .#dev-tent-machine-down
    else
        echo ">> clown-dev left running; swap back with: just dev-tent-down"
    fi

# Download a model by URL and emit a ready-to-paste registry entry.
#
# Uses `circus download <name> --url <url>` (the ad-hoc path added in
# commit a9a288e). The first fetch runs with no SHA verification —
# this recipe computes the SHA-256 *after* download and prints the
# JSON entry that can be appended to
# internal/circusmodels/registry.json. Promoting an ad-hoc download
# to a registry entry then needs:
#
#   1. Paste the printed JSON into the registry.json array.
#   2. Fill in the "description" field (the printed value is "TODO").
#   3. Bump the count in internal/circusmodels/registry_test.go's
#      TestRegistry_ContainsExpectedModels (current: 6) and add the
#      new name to its expected-models slice.
#   4. Rebuild and re-test.
#
# This SHA-emit workflow is what issue #85 (xet-bridge URL pitfall)
# documented as the right way to source SHAs. Issue #86 proposes
# automating the whole registry-extension flow.
#
# Idempotent: if the file is already on disk, prints the SHA + JSON
# entry from the existing file without re-fetching.
#
# Usage: just download-ad-hoc <name> <url>
[group("test")]
download-ad-hoc NAME URL: build
    #!/usr/bin/env bash
    set -euo pipefail
    name="{{NAME}}"
    url="{{URL}}"
    if [[ -z "$name" || -z "$url" ]]; then
        echo "usage: just download-ad-hoc <name> <url>" >&2
        exit 1
    fi
    dest="$HOME/.local/share/circus/models/${name}.gguf"
    if [[ -f "$dest" ]]; then
        echo ">> already installed at $dest"
        echo ">> sha256:"
        shasum -a 256 "$dest" | awk '{print "   "$1}'
        echo
        echo ">> to add to registry, append this entry to internal/circusmodels/registry.json:"
        size=$(stat -f %z "$dest" 2>/dev/null || stat -c %s "$dest")
        sha=$(shasum -a 256 "$dest" | awk '{print $1}')
        printf '  {\n    "name": "%s",\n    "url": "%s",\n    "sha256": "%s",\n    "size": %s,\n    "description": "TODO"\n  }\n' "$name" "$url" "$sha" "$size"
        exit 0
    fi
    echo ">> downloading $name from $url"
    echo ">> integrity check will be SKIPPED on first fetch; we'll print the real SHA afterward."
    echo
    ./result/bin/circus download "$name" --url "$url"
    if [[ ! -f "$dest" ]]; then
        echo "FAIL: download claimed success but $dest does not exist" >&2
        exit 1
    fi
    echo
    echo ">> downloaded to: $dest"
    echo ">> computing sha256 (this takes a few seconds on multi-GB files)..."
    size=$(stat -f %z "$dest" 2>/dev/null || stat -c %s "$dest")
    sha=$(shasum -a 256 "$dest" | awk '{print $1}')
    echo
    echo ">> sha256: $sha"
    echo ">> size:   $size bytes"
    echo
    echo ">> to add to registry, append this entry to internal/circusmodels/registry.json:"
    printf '  {\n    "name": "%s",\n    "url": "%s",\n    "sha256": "%s",\n    "size": %s,\n    "description": "TODO"\n  }\n' "$name" "$url" "$sha" "$size"

# Download Qwen2.5-Coder-7B-Instruct (Q4_K_M, ~4.7GB).
#
# Why this specific model: trained on OpenAI's function-call format,
# fits M2 Pro 16GB unified memory comfortably with headroom for
# macOS + other apps. It's the smallest model I've tested where tool
# calls through opencode/crush actually work (rather than degrading
# to prose narration). Larger options:
#
#   qwen2.5-coder-14b   ~8.5GB Q4_K_M — tight on 16GB
#   qwen3-coder-30b     ~14GB Q3_K_M  — at the memory edge; MoE 3B active
#
# After the download lands and you paste the printed JSON into the
# registry (per download-ad-hoc), the working loop is:
#
#   ./result/bin/circus start qwen2.5-coder-7b --bind 0.0.0.0
#   just smoke-opencode-against-tailnet qwen2.5-coder-7b
#
# Tool-call quality at 7B is ~70-85% on simple multi-step tasks.
# Expect occasional hallucinated tool names and wrong arg shapes —
# this is the model, not the harness.
#
# Usage: just download-qwen-coder
[group("test")]
download-qwen-coder: build
    @just download-ad-hoc qwen2.5-coder-7b \
        https://huggingface.co/Qwen/Qwen2.5-Coder-7B-Instruct-GGUF/resolve/main/qwen2.5-coder-7b-instruct-q4_k_m.gguf

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

# Rewrite the CLOWN_VERSION line in version.env to the given semver
# (eng-versioning(7)). Pure mutation — does not stage or commit; `release`
# composes that. No-op (exit 0) if already at the target. Usage:
# just bump-version 0.5.0
[group("maintenance")]
bump-version new_version:
    #!/usr/bin/env bash
    set -euo pipefail
    . version.env
    current="$CLOWN_VERSION"
    if [[ "$current" == "{{new_version}}" ]]; then
        gum log --level info "version.env already at {{new_version}}"
        exit 0
    fi
    sed -i.bak 's/^export CLOWN_VERSION=.*/export CLOWN_VERSION={{new_version}}/' version.env && rm version.env.bak
    gum log --level info "bumped CLOWN_VERSION: $current → {{new_version}}"

# Create a signed, verified git tag for the CURRENT CLOWN_VERSION (read from
# version.env) and push it to origin (eng-versioning(7)). The "v" prefix is
# added for you. Pass a changelog as the message for richer notes; defaults
# to "release v<ver>". Usage: just tag "release v0.5.0"
[group("maintenance")]
[positional-arguments]
tag message="":
    #!/usr/bin/env bash
    set -euo pipefail
    . version.env
    tag="v${CLOWN_VERSION}"
    if git rev-parse "$tag" >/dev/null 2>&1; then
        gum log --level error "tag $tag already exists"
        exit 1
    fi
    msg="${1:-release $tag}"
    prev=$(git tag --sort=-v:refname -l "v*" | head -1)
    if [[ -n "$prev" ]]; then
        gum log --level info "Previous: $prev"
        git log --oneline "$prev"..HEAD
    fi
    git tag -s -m "$msg" "$tag"
    gum log --level info "Created tag: $tag"
    git push origin "$tag"
    gum log --level info "Pushed $tag"
    git tag -v "$tag"

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

# Cut a release (eng-versioning(7)): refuse off the default branch,
# assemble the changelog BEFORE bumping (so the release commit isn't in its
# own notes), bump CLOWN_VERSION in version.env, commit, sign+push the tag,
# and create the GitHub Release. The bump+commit is skipped when version.env
# already holds the target (a prior commit may have pre-bumped it). The "v"
# prefix is added for you. Usage: just release 0.5.0
[group("maintenance")]
release new_version:
    #!/usr/bin/env bash
    set -euo pipefail
    default_branch=$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's#^origin/##' || true)
    default_branch="${default_branch:-master}"
    current_branch=$(git rev-parse --abbrev-ref HEAD)
    if [[ "$current_branch" != "$default_branch" ]]; then
        gum log --level error "release must run on $default_branch, not $current_branch"
        exit 1
    fi
    # Changelog BEFORE the bump so the release commit isn't in its own notes.
    prev=$(git tag --sort=-v:refname -l "v*" | head -1)
    header="release v{{new_version}}"
    if [[ -n "$prev" ]]; then
        notes="$header"$'\n\n'"$(git log --format='- %s' "$prev"..HEAD)"
    else
        notes="$header"
    fi
    # Bump + commit, unless version.env already holds the target.
    just bump-version "{{new_version}}"
    if ! git diff --quiet version.env; then
        git add version.env
        git commit -m "release v{{new_version}}"
    fi
    just tag "$notes"
    gh release create "v{{new_version}}" --title "v{{new_version}}" --notes "$notes"
