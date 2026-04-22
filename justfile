default: build

build: build-nix

# Build Go binaries
[group("go")]
build-go:
    go build ./cmd/...

# Run Go tests
[group("go")]
test-go:
    go test ./internal/...

# Build the mock MCP server used by integration tests
[group("go")]
build-mock-server:
    go build -o tests/synthetic-plugin/bin/mock-mcp-server ./internal/pluginhost/testdata/mockserver

# Regenerate gomod2nix.toml after go.mod changes (uses the gomod2nix
# binary from the devshell so the tool version matches the nix builder).
[group("go")]
gomod2nix:
    gomod2nix generate

# Integration test: launch clown-plugin-host with the synthetic plugin's
# clown.json and verify the mock HTTP MCP server starts, completes the
# handshake, passes health checks, and generates valid MCP config.
[group("test")]
test-plugin-host: build build-mock-server
    #!/usr/bin/env bash
    set -euo pipefail
    plugin_dir="$(pwd)/tests/synthetic-plugin"
    echo "Starting clown-plugin-host with synthetic plugin..."
    # Run with a dummy downstream that just prints its args and exits.
    # We use 'echo' as the downstream — clown-plugin-host will pass
    # --mcp-config <file> to it, so we can inspect the output.
    output=$(timeout 30 ./result/bin/clown-plugin-host \
        --plugin-dir "$plugin_dir" \
        -- echo DOWNSTREAM_MARKER 2>&1) || {
        echo "FAIL: clown-plugin-host exited with $?" >&2
        echo "$output" >&2
        exit 1
    }
    echo "$output"
    # Verify the downstream received --mcp-config
    if echo "$output" | grep -q -- '--mcp-config'; then
        echo "OK: downstream received --mcp-config flag"
    else
        echo "FAIL: downstream did not receive --mcp-config" >&2
        exit 1
    fi
    if echo "$output" | grep -q 'DOWNSTREAM_MARKER'; then
        echo "OK: downstream received its original args"
    else
        echo "FAIL: downstream did not receive original args" >&2
        exit 1
    fi
    echo "OK: plugin-host integration test passed"

# Integration test: launch clown-plugin-host with the real moxy MCP server as
# a plugin, exercising the clown-plugin-protocol against a production server
# instead of the synthetic mock. Moxy must already be on $PATH; its plugin
# dir is derived as <prefix>/share/purse-first/moxy.
[group("test")]
test-plugin-host-moxy: build
    #!/usr/bin/env bash
    set -euo pipefail
    moxy_bin=$(command -v moxy) || {
        echo "FAIL: moxy not found on PATH" >&2
        exit 1
    }
    moxy_prefix=$(dirname "$(dirname "$moxy_bin")")
    plugin_dir="$moxy_prefix/share/purse-first/moxy"
    echo "Using moxy at: $moxy_bin"
    echo "Plugin dir:    $plugin_dir"
    if [[ ! -f "$plugin_dir/clown.json" ]]; then
        echo "FAIL: $plugin_dir/clown.json is missing." >&2
        echo "      Your moxy on PATH is too old; update it to a version that" >&2
        echo "      ships share/purse-first/moxy/clown.json." >&2
        exit 1
    fi
    echo "Starting clown-plugin-host with moxy as the plugin..."
    output=$(timeout 60 ./result/bin/clown-plugin-host \
        --plugin-dir "$plugin_dir" \
        -- echo DOWNSTREAM_MARKER 2>&1) || {
        echo "FAIL: clown-plugin-host exited with $?" >&2
        echo "$output" >&2
        exit 1
    }
    echo "$output"
    if echo "$output" | grep -q -- '--mcp-config'; then
        echo "OK: downstream received --mcp-config flag"
    else
        echo "FAIL: downstream did not receive --mcp-config" >&2
        exit 1
    fi
    if echo "$output" | grep -q 'DOWNSTREAM_MARKER'; then
        echo "OK: downstream received its original args"
    else
        echo "FAIL: downstream did not receive original args" >&2
        exit 1
    fi
    if echo "$output" | grep -q 'moxy/moxy'; then
        echo "OK: clown-plugin-host reported the moxy server"
    else
        echo "FAIL: no sign of the moxy/moxy managed server in host output" >&2
        exit 1
    fi
    echo "OK: plugin-host-moxy integration test passed"

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

# Lint all mdoc(7) manpages with mandoc -Tlint. Operates on the built
# pages so @MDOCDATE@ has already been substituted, meaning we lint
# what actually ships.
[group("docs")]
lint-man: build-man
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(readlink -f result-man)
    pages=(
        "$out"/share/man/man1/*.1
        "$out"/share/man/man5/*.5
        "$out"/share/man/man7/*.7
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

# Smoke-test the --skip-failed / CLOWN_SKIP_FAILED_PLUGINS / no-opt-in
# branches using a pre-built .tmp/bad-plugin that points at a nonexistent
# binary. Assumes the plugin dir already exists (created by hand for now).
[group("explore")]
explore-skip-failed MODE: build
    #!/usr/bin/env bash
    set -u
    plugin_dir="$(pwd)/.tmp/bad-plugin"
    if [[ ! -f "$plugin_dir/clown.json" ]]; then
        echo "FAIL: $plugin_dir/clown.json not found. Create the bad-plugin fixture first." >&2
        exit 2
    fi
    env_skip=
    args=()
    case "{{MODE}}" in
        none)        ;;
        flag)        args+=(--skip-failed) ;;
        env)         env_skip="CLOWN_SKIP_FAILED_PLUGINS=1" ;;
        *) echo "MODE must be none|flag|env" >&2 ; exit 2 ;;
    esac
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
    agents_output=$(./result/bin/clown --clean --plugin-dir "$plugin_dir" \
        agents list 2>&1) || true
    echo "$agents_output"
    echo "---"
    failed=0
    for agent in yaml-test-agent toml-test-agent; do
        if echo "$agents_output" | grep -q "$agent"; then
            echo "OK: $agent loaded"
            echo "--- $agent details ---"
            ./result/bin/clown --clean --plugin-dir "$plugin_dir" \
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
    cli="./result/bin/clown --clean --plugin-dir $plugin_dir plugin list"
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
