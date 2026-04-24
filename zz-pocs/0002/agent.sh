#!/usr/bin/env bash
# Agent for zz-pocs/0002: reads a workspace, writes a processed copy to $out.
#
# Args:
#   $1  read-only workspace path (from the `workspace` flake input's outPath)
#   $2  writable output path ($out)
#
# Demonstrates the read-then-write contract: the subagent has both access to
# the caller's workspace content and a writable area of its own. For the POC
# we just copy the workspace in and append a marker line.

set -euo pipefail

WORKSPACE="${1:-}"
OUT="${2:-}"

if [[ -z $WORKSPACE || -z $OUT ]]; then
  echo "agent.sh: expected workspace and out paths as args" >&2
  exit 2
fi

echo "agent.sh: workspace=$WORKSPACE"
echo "agent.sh: out=$OUT"

# Copy workspace contents into $out. Preserve symlinks as-is (no -L) — the
# worktree contains `result` / `result-man` symlinks into the nix store, and
# dereferencing them would pull entire closures in. `vendor/` and `.git/`
# are also large; skip them to keep the POC fast. In v1 ringmaster controls
# which subtree gets copied (workspace.include globs per RFC-0003).

shopt -s dotglob nullglob
for entry in "$WORKSPACE"/*; do
  base=$(basename "$entry")
  case "$base" in
  .git | vendor | node_modules | result | result-*)
    echo "agent.sh: skipping $base"
    continue
    ;;
  esac
  cp -a "$entry" "$OUT/"
done

# The workspace comes from a nix store path, so its files are read-only. Make
# everything we just copied writable so we can append to it. `chmod -R u+w`
# applies recursively; the dir itself is already writable because we mkdir'd
# $out in buildPhase before calling this script.
chmod -R u+w "$OUT"

# Append a processed-marker file.
{
  echo "processed by zz-pocs/0002 sandbox-agent"
  echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "uname: $(uname -srm)"
  echo "workspace root: $WORKSPACE"
} >>"$OUT/hello.txt"

echo "agent.sh: wrote $OUT/hello.txt"
