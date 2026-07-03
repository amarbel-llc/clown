---
status: exploring
date: 2026-07-03
promotion-criteria: |
  exploring → proposed: the repo boundaries below are ratified and clown + at
  least one producer (moxy or spinclass) have a reviewed flake-wiring plan.
  proposed → experimental: both repos exist, ringmaster builds standalone, troupe
  builds against ringmaster as an input, clown consumes both, and one producer
  pins ringmaster in place of clown.
---

# Job platform as standalone repos (ringmaster base + troupe)

## Problem Statement

RFC-0015 split the job platform into `ringmaster` (job control) and `troupe`
(messaging) binaries, but they ship only inside clown's flake. Producers (moxy,
spinclass) that need to pin these binaries reproducibly must add clown as a
flake input and reference `clown.packages.${system}.ringmaster` — dragging
clown's entire heavy input closure (llm-agents, claude-code, codex,
nixpkgs-llama, treefmt-nix, several pinned nixpkgs) into their `flake.lock` for a
tiny Go binary. The platform is a well-specified, multi-consumer substrate that
wants its own lightweight, independently-pinnable home.

## Interface

The proposed shape: **two repos**, layered, with `ringmaster` as the base.

### `ringmaster` — the base repo (platform core)

`jobwake` and `jobmcp` fold **into ringmaster**. It owns:

- the channel substrate (`jobwake`: journal, UDS nudge, presence, output spool,
  and the chat records),
- the MCP server (`jobmcp`, both the ringmaster and troupe tool surfaces),
- the `ringmaster` CLI: job control
  (`start`/`progress`/`done`/`read`/`status`/`wait`/`whoami`/`spool-path`/`monitor`),
  operator verbs (`ls`/`tail`/`cancel`), and the FDR-0010 llama-server
  control-plane daemon (decision below: it stays here).

Flake inputs: roughly just `igloo` + `bats` — a small closure. Output:
`packages.${system}.ringmaster`.

### `troupe` — the downstream repo (messaging layer)

`troupe` takes **ringmaster as a flake input** and builds on it. It owns:

- the messaging CLI (`send`/`read`/`list`/`message` + `mcp`), built on
  ringmaster's `jobwake` substrate (chat records + presence ride the same
  channel) and ringmaster's `jobmcp` (the `troupe mcp` surface).

Flake inputs: `ringmaster` (+ `igloo`/`bats` via `follows`). Output:
`packages.${system}.troupe`.

### Dependency direction (cycle-free by construction)

Verified against all three flakes this session: **clown's flake inputs neither
moxy nor spinclass**, so the graph is a clean DAG:

- **`ringmaster`** is the leaf (inputs ~`igloo`+`bats`; imports nothing
  downstream).
- **`troupe` → `ringmaster`** (downstream messaging on the base substrate).
- **clown → `ringmaster` + `troupe`**: clown burns both binaries into its
  monitor-synthesis + MCP-injection, and imports ringmaster's `jobwake`/`jobmcp`
  Go packages instead of vendoring them under `internal/` (dependency
  inversion — clown *contains* them today, *imports* them after).
- **moxy, spinclass → `ringmaster` only** — they emit job lifecycle and never
  message, so they pin the base binary alone (the smallest possible closure).

No consumer points back at `ringmaster`'s inputs, so no cycle can form.

## Examples

Producer (moxy/spinclass) pinning — the smallest lock, ringmaster only:

    inputs.ringmaster.url = "github:amarbel-llc/ringmaster";
    inputs.ringmaster.inputs.igloo.follows = "igloo";
    inputs.ringmaster.inputs.bats.follows = "bats";
    # reference ${ringmaster.packages.${system}.ringmaster}/bin/ringmaster

`troupe`'s own flake (downstream of ringmaster):

    inputs.ringmaster.url = "github:amarbel-llc/ringmaster";
    # troupe imports ringmaster's jobwake/jobmcp Go modules and builds the
    # messaging CLI + `troupe mcp` on them.

clown consuming both (inverted from "contains" to "imports"):

    inputs.ringmaster.url = "github:amarbel-llc/ringmaster";
    inputs.troupe.url = "github:amarbel-llc/troupe";
    # clown burns both binaries into its synthesized monitor + MCP servers and
    # imports ringmaster's jobwake/jobmcp Go packages.

## Limitations

- **The llama-server hat — decided: stays in `ringmaster`.** `ringmaster` keeps
  the FDR-0010 llama-server control-plane daemon (RFC-0015 §5, "local models are
  jobs, one hat"). A producer's `ringmaster` builds only the job verbs it uses;
  the model machinery sits behind the optional `LlamaServerPath` ldflag (empty ⇒
  the daemon errors clearly, the job verbs still work), so keeping one binary
  does not force llama-cpp into a producer's closure. Revisit only if the
  leaf-weight cost proves prohibitive.

- **XMPP transport ownership (open nuance).** RFC-0015 §4 specifies troupe's CLI
  as transport-abstract so a future XMPP backend can replace the journal. With
  the substrate (`jobwake`) in `ringmaster`, that swap lands either as a
  `ringmaster`-side channel backend (job wakeups *and* chat could ride XMPP) or
  as a `troupe`-side transport for chat alone (job wakeups stay on ringmaster's
  journal). Resolve when the XMPP work is scoped; not blocking for the
  extraction.

- **Interim vs target.** Until the repos land, moxy/spinclass pin
  `clown.packages.*.ringmaster` now (Option A, accepted closure bloat — decided
  this session). Migrating to the standalone `ringmaster` is a one-line input
  swap from that interim, not a rewrite.

- **Not a behavior change.** RFC-0015's CLI surface (the `ringmaster`/`troupe`
  verbs, exit codes, output) is unchanged; this is a packaging + Go-module move.

## More Information

- RFC-0015 — the `ringmaster`/`troupe` binary split, the XMPP
  transport-abstraction (§4), and the local-models-are-jobs framing (§5) that
  settles the llama-server-hat decision.
- FDR-0010 — the `ringmaster` llama-server control plane that rides along in the
  base repo.
- clown#155 — the rename-scope decision RFC-0015 resolved; this FDR is the
  packaging follow-through.
- The moxy/spinclass RFC-0015 migration (this session) that surfaced the
  pinning/closure cost motivating the extraction.
