#!/usr/bin/env bash
# zz-pocs/0002 smoke test.
#
# 1. Build the ringmaster binary (packaged via buildZxScriptFromFile).
# 2. Pipe a canned MCP session (initialize → tools/list → tools/call) into it.
# 3. Print the responses and the sandbox-agent's $out/hello.txt.

set -euo pipefail

cd "$(dirname "$0")"
POC_DIR="$(pwd)"

echo "=== building ringmaster + sandbox-agent ==="
nix build .#default --print-build-logs

RINGMASTER="$POC_DIR/result/bin/ringmaster"
if [[ ! -x "$RINGMASTER" ]]; then
  echo "ringmaster binary not found at $RINGMASTER" >&2
  exit 1
fi

echo
echo "=== driving ringmaster with a canned MCP session ==="

# Canned MCP session: initialize, tools/list, tools/call. One JSON object per
# line. The tool call asks run_discover to operate on the clown worktree root.
WORKSPACE_REF="$(cd ../.. && pwd)"
REQUESTS=$(cat <<EOF
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"run.sh","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_discover","arguments":{"prompt":"poc smoke test","workspace_ref":"$WORKSPACE_REF"}}}
EOF
)

# Feed the requests, close stdin, capture stdout. stderr goes through to
# our terminal so we see ringmaster's logs.
RESPONSES=$(CLOWN_POC_FLAKE_DIR="$POC_DIR" "$RINGMASTER" <<<"$REQUESTS")

echo
echo "=== responses ==="
echo "$RESPONSES" | while read -r line; do
  echo "$line" | jq .
done

echo
echo "=== extracting out_ref from the final tools/call response ==="
OUT_REF=$(
  echo "$RESPONSES" |
    jq -r 'select(.id == 3) | .result.content[0].text' |
    jq -r '.out_ref // empty'
)

if [[ -z "$OUT_REF" ]]; then
  echo "no out_ref returned — check responses above" >&2
  exit 1
fi

echo "out_ref: $OUT_REF"
echo
echo "=== $OUT_REF/hello.txt (last 20 lines) ==="
tail -20 "$OUT_REF/hello.txt"
