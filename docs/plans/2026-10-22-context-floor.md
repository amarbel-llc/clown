# Context floor of spawned workers

Measured (circus/neat-sycamore, from transcripts): a spawned worker carries
~182K tokens (4.6) before any work, ~84% tool definitions it never calls. This
note records what clown does about it and what is deliberately left for later.

## Knobs (in a profile's `env` table; no profile-protocol bump)

| Env key | Effect |
|---|---|
| `CLOWN_DISABLE_CLAUDEAI_CONNECTORS=1` | `--settings` gets `disableClaudeAiConnectors: true` and its env block `ENABLE_CLAUDEAI_MCP_SERVERS=false` (names read from the claude 2.1.306 binary). An explicit operator `ENABLE_CLAUDEAI_MCP_SERVERS` wins. |
| `CLOWN_DISALLOWED_TOOLS=A,B,...` | Extra `--disallowed-tools`, layered after the global set, deduplicated. |

The digest of a profile already covers `env`, so spinclass sees these without a
protocol change.

## Templates

`reviewer`, `porter`, `canary` in `internal/profile/builtin.toml`: claude /
anthropic / claude-sonnet-5-5, a saved cheap-context selection (servers + moxy
tool exclusions + spinclass exclusions for porter), both knobs on. Lists are
from worker transcripts against moxy's 156-tool catalog; a test checks each
template's keep and exclude sets partition it.

## Guard

`clown-hook-toolcost` judges the first response of a transcript (its total input
is the floor): gauge `clown.context_floor.<host>.<repo>.<model>` always, counter
`clown.context_floor_over...` and a `systemMessage` warning when the floor is
over `CLOWN_CONTEXT_FLOOR_PCT` (60) percent of `CLOWN_CONTEXT_WINDOW` (200000).
No profile disables auto-compact by default; set `DISABLE_AUTO_COMPACT=1` in a
profile's env to opt in.

## Not verified / follow-ups

- Which connector-off switch actually bites (setting vs env var) was not
  measured: a headless run inside a confined worker loads no connectors at all.
  Both are set; `just explore-claude-connector-switches` measures it on a host
  where the connectors load.
- **First-class profile keys** (`disable_connectors`, `disallowed_tools`) instead
  of env keys: a profile-protocol bump (1 -> 2) that spinclass must relink for,
  since the canonical digest gains fields and an older library fails closed on a
  registry stamped 2. Do it together with that relink.
- The loop guard warns; it cannot know the floor before the first response.
