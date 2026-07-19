---
status: exploring
date: 2026-04-29
---

# Plugin-Contributed Tool Permission Hooks

## Abstract

This specification proposes a mechanism by which clown plugins
declare permission posture for the MCP tools they ship, using the
same state vocabulary as moxy's `perms-request`. Status is
**exploring** — questions below are intentionally open and will be
answered in future revisions before this becomes implementable.

## Motivation

Today, plugin authors have no way to express "this tool is
read-only, always allow" or "this tool is destructive, ask every
time" alongside their tool definitions. Permission posture lives
entirely in the user's `~/.claude/settings.local.json` (or
managed-settings deny lists), divorced from the tool catalog the
plugin ships. Two consequences:

1. **Author intent is lost.** A plugin author who knows their `read`
   tool is safe and their `chmod` tool is risky cannot communicate
   that to the MCP client; every user has to discover and configure
   the right posture themselves.
2. **No plugin-defined dynamic gating.** Even when a plugin author
   *can* compute "is this call safe?" cheaply (e.g. "is this path
   inside CWD?"), there's no way to surface that decision short of
   bouncing through the MCP request/response and a tool-level
   error.

Moxy already solved this for moxin-defined native tools via the
`perms-request` field with values `always-allow`, `each-use`,
`delegate-to-client`, `dynamic` (the latter paired with a
`[dynamic-perms]` hook script). We want the same vocabulary
available to clown plugins so they can carry per-tool posture
through the plugin-host pipeline into Claude Code.

## Reference Vocabulary (from moxy)

`internal/native/config.go:19-29`:

| Value | Behavior |
|-------|----------|
| `delegate-to-client` (default) | Let the MCP client decide. |
| `always-allow` | Skip the permission prompt. |
| `each-use` | Force user confirmation every time. |
| `dynamic` | Run a per-call hook script that returns `allow`, `ask`, or `deny`. |

`dynamic` is paired with a `[dynamic-perms]` block on the tool that
declares the hook `command` (and optional args/timeout). Before each
tool call, moxy invokes the hook with the same arg-order and
stdin-param routing the main tool would receive — the hook sees the
actual arguments of the pending call. The hook's exit code maps to the
decision:

| Exit code | Decision | Notes |
|-----------|----------|-------|
| `0` | `allow` | |
| `1` | `ask` | Prompt the user. |
| `2` | `deny` | |
| any other | `ask` | The unmapped-code message is surfaced. |
| timeout (default 2s) | `ask` | |
| spawn failure | `ask` | E.g. binary missing, permission denied. |

The hook's stdout (truncated) is captured as the human-readable reason
surfaced to the user when `ask` or `deny` is rendered.

The `""` (fall-through) decision exists in the moxy code for the case
where no `[dynamic-perms]` spec is configured at all; it is never
returned from a successful exit-code mapping.

## Design space (open)

**Where does the plugin declare per-tool perms?**

- Inline in `clown.json` next to each `httpServers`/`stdioServers`
  entry as a server-level default, plus an optional per-tool override
  map?
- Sidecar manifest at `<plugin>/clown-perms.json` so plugin authors
  can ship perms without touching the server-launch manifest?
- Inside the plugin's own MCP `tools/list` response (annotation
  field), so each tool carries its own posture?

**How does clown apply the declared posture?**

- Materialize into Claude Code's `settings.local.json`
  `permissions.allow` / `permissions.ask` / `permissions.deny` arrays
  at session start (mirroring how mkCircus already injects managed
  settings)?
- Stay out of `settings.local.json` and instead intercept tool calls
  via a clown-side gate before they reach Claude Code?
- Both — plugin-declared `always-allow` becomes a permission rule;
  `dynamic` runs in a clown-side gate?

**Reconciliation with user-set rules.**

- User deny always wins over plugin allow (safety floor).
- User allow over plugin each-use? Probably — the user is the final
  authority for their session.
- What about when a plugin upgrades `each-use` → `always-allow` in a
  new version? Surface the diff at install/load?

**Granularity.**

- Tool name only (`mcp__plugin_foo_foo__bar`)?
- Tool + structured-input pattern (e.g. `bar` is `each-use` when
  `input.path` is outside CWD, `always-allow` otherwise)? This
  collapses into the `dynamic` state in practice.

**Third-party stdio MCPs bridged via FDR 0002.**

- A plugin author wraps a third-party stdio MCP server and wants to
  declare `always-allow` for its read-only tools. The author may
  not know which tools the third-party server exposes. Solutions:
  declarations apply server-wide by default with per-tool
  overrides; declarations match by tool-name pattern (`bar.read*` →
  `always-allow`).

## Provisional sketch

A `clown.json` server entry gains an optional `permissions` block:

```json
{
  "version": 1,
  "httpServers": {
    "my-server": {
      "command": "/abs/path/bin/my-server",
      "permissions": {
        "default": "delegate-to-client",
        "tools": {
          "read":  "always-allow",
          "write": "each-use",
          "exec":  { "request": "dynamic", "command": "bin/check-exec" }
        }
      }
    }
  }
}
```

`clown-plugin-host` reads the block at discovery time and emits
matching entries into the compiled plugin manifest (or
`settings.local.json`, TBD per "How does clown apply").

## Open questions for a future revision

- Pin "where does the plugin declare per-tool perms".
- Pin "how does clown apply the declared posture".
- Decide whether `dynamic` (with hook script) is in scope for v1 or
  deferred to v2.
- Decide reconciliation rules between plugin-declared and
  user-declared permissions.
- Decide per-tool granularity (name vs name+input pattern).
- Spec the wire format precisely (JSON schema additions to
  `clown-json(5)`, normative MUST/SHOULD/MAY language for the
  reconciliation rules).

Tracked in [#38](https://github.com/amarbel-llc/clown/issues/38).

## References

- Moxy's `perms-request` field — `internal/native/config.go:19-29`
  in amarbel-llc/moxy.
- RFC 0002 — Clown Plugin Protocol (where this would extend the
  `clown.json` schema).
- FDR 0002 — Stdio MCP Bridge (the third-party stdio case above).
- Claude Code managed-settings (where plugin-derived rules might
  land at runtime).
