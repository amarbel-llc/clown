#!/usr/bin/env bash
# Thin shim: builds run.ts (zx) via nix and exec's it. See run.ts for the
# actual driver.

set -euo pipefail
cd "$(dirname "$0")"

if ! command -v mitmdump >/dev/null 2>&1; then
  echo "run.sh: mitmdump not on PATH. Install via 'nix shell nixpkgs#mitmproxy' or add to your devshell." >&2
  exit 2
fi

# Build the zx driver via the POC-0002 shape: use amarbel-llc/nixpkgs's
# buildZxScriptFromFile. For simplicity here, we just invoke with bun
# directly if it's on PATH — the zx cache is handled by the ///!dep hash.
if ! command -v bun >/dev/null 2>&1; then
  echo "run.sh: bun not on PATH. Install via 'nix shell nixpkgs#bun'." >&2
  exit 2
fi

exec bun run run.ts
