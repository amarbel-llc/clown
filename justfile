default: build

build: build-nix

build-nix:
    nix build --show-trace

clean:
    rm -rf result

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
