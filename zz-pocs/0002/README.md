# zz-pocs/0002: Ringmaster MCP server + synthetic sandbox-agent flake

## Purpose

Prove the ringmaster dispatch loop end-to-end:

1. Ringmaster is an MCP server speaking JSON-RPC over stdio.
2. On `tools/call` for `run_discover`, ringmaster shells `nix build` against
   a co-located **synthetic flake** (`flake.nix` in this directory), passing
   the caller-supplied workspace as a flake input override.
3. The synthetic flake's `sandbox-agent` derivation copies the workspace
   content, runs an in-derivation agent (`agent.sh`), and writes results to
   `$out`.
4. Ringmaster returns `$out`'s store path as the MCP result.

## What the POC proves

- MCP stdio protocol: `initialize`, `tools/list`, `tools/call` round-trip.
- `nix build` dispatch from a script, per-invocation workspace override.
- `$PWD = $out` contract from FDR-0001 (agent sees an ordinary writable
  directory; whatever it leaves behind becomes the derivation output).
- Serialized invocations (single in-flight mutex).

## What the POC does NOT prove (deferred)

- Sandcastle confinement. Still stubbed. Blocked on
  [amarbel-llc/eng#41](https://github.com/amarbel-llc/eng/issues/41).
- Real LLM agent. `agent.sh` is bash. Once sandcastle is re-enabled and
  eng#41 lands, the tracer brings in claude-code against a real endpoint.
- Egress broker (mitmproxy). Not yet spawned; the `sandbox-agent` derivation
  has no network access anyway (no `__impure`).
- Concurrent invocations. Mutex serializes them.
- TOML catalog parsing. `run_discover` is hardcoded.
- Hot reload, cgroup limits, audit log, `invocations.jsonl` observability.
- Multi-backend. Just bash.

## Synthetic flake template note

`flake.nix` here is a **hand-written example of what v1's generator should
produce per invocation**. In v1, ringmaster will render this flake (or
something equivalent) from a TOML subagent definition, parameterize its
inputs (workspace, reference repos, etc.), and hand it to `nix build`. The
rendered flake must be correct in its final form each time — valid schema,
no dangling placeholders, deterministic for the same inputs.

## Files

| File | Purpose |
| --- | --- |
| `flake.nix` | Synthetic flake with two outputs: the ringmaster binary and the `sandbox-agent` derivation. |
| `ringmaster.ts` | zx script: reads JSON-RPC from stdin, dispatches `run_discover` via `nix build`. |
| `agent.sh` | In-derivation agent: copies `$workspace` into `$out`, appends a marker line. |
| `run.sh` | Smoke test: builds ringmaster, pipes a canned MCP session in, prints the response and `hello.txt`. |

## How to run

```
git add zz-pocs/0002/
./zz-pocs/0002/run.sh
```

The flake must be git-tracked for Nix to see it. The smoke test drives
ringmaster with a canned 3-step MCP session and prints the result.

## How ringmaster resolves the flake

`ringmaster.ts` looks for the flake dir via `$CLOWN_POC_FLAKE_DIR`. `run.sh`
sets it to `zz-pocs/0002/`. Manual invocation (e.g. pointing Claude Code's
MCP client at the ringmaster binary) should set the same env var.

In v1, ringmaster won't need this — it'll generate the flake in a tempdir or
a clown-managed state directory, and pass the path internally.

## Success criteria

- [ ] `run.sh` completes without error on darwin.
- [ ] Ringmaster responds to `initialize` and `tools/list` with valid JSON-RPC.
- [ ] `tools/call` for `run_discover` returns `{status: "success", out_ref: ...}`.
- [ ] `$out_ref/hello.txt` contains a "processed by zz-pocs/0002" line plus
      the POC-0001 header (since the workspace includes POC-0001's output).

## Outcome log

Fill in after running.
