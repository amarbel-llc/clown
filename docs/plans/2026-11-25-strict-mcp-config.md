# `--strict-mcp-config` and how clown hands its servers to claude

Status: note for the coordinator's decision; nothing built. Written by clown/deft-hazel/coco.

## Question

The allow-list plan said clown passes `--strict-mcp-config` on every launch so that MCP servers from the
operator's home (user scope) cannot reach a session. Does the flag fit how clown delivers its own servers?

## Seen (just explore explore-strict-mcp-probe, one run, claude 2.1.330, confined unit, proxy credential)

A throwaway plugin dir (`--plugin-dir`) declaring one server in its `.mcp.json`:

| flags | `mcp_servers` in the stream-json init event |
|---|---|
| none | `plugin:strict-probe-plugin:strict-probe`, status `failed` (command `true`), source `plugin` |
| `--strict-mcp-config` | `[]` |

So with the flag, plugin-delivered servers are dropped. Clown delivers every one of its servers through
`--plugin-dir` (no `--mcp-config` anywhere in `*.go`), so adding the flag alone removes all of clown's MCP tools
(moxy, the job platform, the stdio bridges) from every session.

## Consequence of the alternative (handing servers over by `--mcp-config`)

Not run, but visible from this very session: plugin servers are named `mcp__plugin_<plugin>_<server>__<tool>`
(for example `mcp__plugin_moxy_moxy__folio_read`). A server given by `--mcp-config` is named `mcp__<server>__<tool>`
instead (claude's rule for non-plugin servers; I have not run it). Every permission rule, hook matcher and
allow-list that names a tool would change. That is a fleet-wide rename, not a flag.

## Recommendation (unverified parts marked)

Do not add `--strict-mcp-config`. The user-scope config it was meant to exclude lives under `CLAUDE_CONFIG_DIR`;
the full unit already sets that to a per-session directory with no user MCP configuration (seen: today's confined
run, credentials file 0 bytes, claude answered). (When this note was written a private-view unit was still planned,
the one mode that would have shown the operator's home; the operator ruled 2026-11-25 that the full unit plus the
bridge is the one confined mode and the view was not built, so there is no unit that mounts `~/.claude*`.)
Unknown, not tested: whether project-scope `.mcp.json` files inside the worktree still load (they are agent-writable
content); if that matters, the probe to run next is the same recipe with a `.mcp.json` in the worktree and
`--setting-sources` limited.

## Decided (coordinator, 2026-11-25)

No `--strict-mcp-config`; the per-session `CLAUDE_CONFIG_DIR` is the mechanism. A project-scope `.mcp.json` in the
worktree starts servers inside the unit as the session's own processes: outside the boundary's threat model, not probed.

## For the bridge's stdio-client services (servers that move outside the unit)

They must keep their `mcp__plugin_<p>_<s>__*` names, so they stay plugin-delivered. Whether a plugin's server entry
can simply name `clown bridge-stdio --service X` as its `command`: BELIEVED yes (an entry is a command and args that
claude execs and speaks MCP over stdio to; `bridge-stdio` is exactly that pipe, exit 1 plus the down text on stderr
when the bridge is down), and that would be the whole change on the unit side. NOT run: the one thing to check is
that the entry's `command` resolves inside the unit (the unit's `CLOWN_BIN` is the shim's own binary, so an entry
should use it, not a PATH lookup) and that claude shows the server `connected` through a fake service. A hermetic
probe is the same recipe with `bridge-stdio` against a fake service; not written.
