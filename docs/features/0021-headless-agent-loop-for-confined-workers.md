---
status: exploring
date: 2026-10-20
issue: clown#271
related: FDR 0019 (juggler agent substrate), FDR 0020 (agent scope confinement), spinclass FDR 0032 / 0034 / 0035, ringmaster FDR-0001, clown#252 #255 #258 #260 #261 #263 #274, moxy#484
promotion-criteria: >
  proposed once the operator has answered the open questions marked
  "operator" below and the two UNVERIFIED claims that gate Stage 1 (what
  moxy does on a plain `tools/call` for an ask/dynamic tool; whether the
  spinclass hello and attestation contracts can be met by a non-hook
  harness) have been settled by their owning repos' sessions. experimental
  once one confined worker runs a real brief to a merged commit on the
  headless loop on flic, with the confinement check passing and no permission
  prompt reachable by construction.
---

# Headless agent loop for confined workers

> **Design record only.** Nothing here is implemented. Claims that were not
> read in source or measured are marked **UNVERIFIED**. The Claude Code
> behaviours described come from clown's own code, hook payload handling and
> the cited records, not from Claude Code's source, which this session did not
> read.

## Problem Statement

Claude Code is built around a human at the terminal. A permission question is
answered on the tty; its auto mode documents a fall-back to asking; clown's
hooks, the attach surface and spinclass's hello all assume a surface someone can
reach. A confined worker (FDR 0020 agent scope) has no such human by
construction: spinclass FDR 0035 F8a/F8b make the operator's tty and posh attach
socket unreachable from the unit.

On 2026-10-20 the first confined worker sat on a permission prompt for over 30
minutes until the operator attached and answered by hand (spinclass FDR 0035,
"A confined worker never blocks on a permission prompt"). The near-term
mitigation is `--permission-mode dontAsk`, spliced into a confined spawn's
provider args (`spawn.SpliceDontAskMode`, spinclass `internal/spawn/dontask.go`).
It hides the mismatch rather than removing it, and moxy#484 shows the hiding is
incomplete: under dontAsk a PreToolUse and then a PermissionRequest hook event
for a moxy `[perms: dynamic]` tool were logged, no decision was ever recorded,
and Claude Code never wrote a tool_result, so the agent idled on that call
indefinitely, while a builtin Read outside the allow list was refused at once.
That is the same failure class (a path where the harness waits for an answer
nobody can give) surfacing through a different door. Each fix on the Claude Code
path closes one door; the harness still owns the others.

The direction recorded in clown#271 is that clown can run a worker on a
**headless agent loop backed by juggler alone**, with no Claude Code process in
the unit: the loop reads a brief, calls the model through juggler, dispatches
tool calls to the in-unit moxy and the frontend-class MCP servers, applies the
session's permission policy itself (refuse, never ask), and emits what
spinclass, ringmaster, troupe and the toolcost metrics consume today.

This record lists what the Claude Code path provides that such a loop must
reproduce, what it may drop, what juggler already is, how the permission policy
works, how a confined worker fits, the identity questions it opens, and a staged
path. It does not decide the operator-owned questions.

## Goals and non-goals

Goals:

1. A confined worker has **no code path that waits for an answer from a human**.
   This is a property of the loop, testable with a fake model, not a property of
   a mode flag in a third-party client.
2. The loop is a first-class **harness** in clown's matrix (FDR 0006), selectable
   per spawn, coexisting with the Claude Code path.
3. spinclass, ringmaster, troupe and stats-me see the same facts they see today
   (hello, session lifecycle, turn stream, metrics) from a worker on either
   path.

Non-goals (v1):

- Replacing Claude Code for interactive, unconfined, human-driven sessions.
- Re-creating Claude Code's TUI, skills, slash commands, image/PDF reading,
  web fetch/search.
- Deciding identity, signing, billing or lane questions that belong to the
  operator, spinclass, moxy or ringmaster (listed under Open questions).

## 1. What the Claude Code path provides today

Each row says where the capability lives, who consumes it, and the proposed
disposition: **reproduce** (the loop must provide an equivalent), **replace**
(consumers move to a different source), or **drop**.

### 1.1 Transcript JSONL

Claude Code writes the session transcript to `~/.claude/projects/<slug>/<session>.jsonl`
and per-subagent files under `subagents/agent-*.jsonl`
(`docs/plans/2026-10-20-unused-tools-per-session.md` §1a; `internal/sessions/claude.go:48-52`
lists sessions by walking `~/.claude/projects`). Record types seen in the plan's
read of a 2.1.301 transcript include `user`, `assistant`, `attachment`,
`api-request-shape`, `api-request`, `api-request-blob`, `last-prompt` and
`compact_boundary`.

Readers inside the repo:

| Reader | What it takes from the transcript | Source |
|---|---|---|
| `clown-hook-toolcost` | per-response `message.usage`, `tool_use`/`tool_result` pairing (grouped by `message.id`), `compact_boundary`; offsets committed per transcript under `$XDG_STATE_HOME/clown/hook-toolcost/` | `internal/toolcost/toolcost.go` package doc; `cmd/clown-hook-toolcost/main.go` |
| `clown-hook-tee` | assistant `text` blocks since a persisted byte cursor; posts one message per turn into the per-worktree MUC | `cmd/clown-hook-tee/main.go` package doc |
| `clown sessions` / resume listing | session discovery by mtime | `internal/sessions/claude.go` |
| freud (a moxy moxin) | session list, summary, tool usage, messages from `~/.claude/projects` | moxy child server `freud`; its internals are moxy's lane, UNVERIFIED beyond the tool descriptions |
| spinclass `PostToolUse` hook | appends the raw hook payload to `$XDG_LOG_HOME/spinclass/tool-uses/<session>.jsonl` | spinclass `internal/hooks/hooks.go` `runPostToolUseLog` |

Disposition: **replace**. The loop owns its transcript. FDR 0019 already gives
the loop a stanza-shaped `Turn` (`internal/jugglerloop/turn.go`) posted to the
run's MUC per turn, but today the turns reach disk only as the final ledger
(written once, at `finish`) and as `traffic.jsonl` (per call, durable
immediately). A SIGKILL leaves traffic lines and no ledger (FDR 0019 §4). The
loop therefore needs a **durable, append-only local turn log** (one JSON line per
`Turn`, flushed per write) as the source for resume (1.9), the tee (1.2) and
freud-class tooling. Consumers are re-pointed at it; the Claude Code transcript
reader stays for Claude Code sessions.

### 1.2 Hooks

clown registers hooks through the synthesized `clown-builtin-jobs` plugin's
`hooks/hooks.json` (`cmd/clown/jobmonitor.go:373-446`):

| Event | Handler | What it does | Disposition |
|---|---|---|---|
| `PreToolUse` (matcher `.*`) | `clown-hook-allow` | allow `/nix/store` Read/Glob/Grep and the `clown-builtin-jobs` MCP tools; rewrite the `Agent` tool's `Explore` subagent to clown's read-only `Discover` (`cmd/clown-hook-allow/main.go`) | **reproduce as policy** (§3): the allow rules become entries in the loop's policy; the Explore rewrite is **dropped** with the Agent tool |
| `Stop`, `SessionEnd` | `clown-hook-tee` | tee assistant text into the group MUC | **reproduce in-process**: the loop already posts every turn to a room (FDR 0019 §7, `postTurn`); the tee's cursor/settle machinery exists only because Claude Code flushes its transcript lazily and is not needed |
| `Stop`, `SubagentStop`, `SessionEnd` | `clown-hook-toolcost` | statsd families `clown.tool_result.*`, `clown.tokens.*`, `clown.responses.*`, `clown.context.*`, `clown.compaction.*` | **reproduce in-process** (clown#263, §1.12) |

spinclass registers its own hooks in the worker (`internal/hooks/hooks.go`
dispatch): `SessionStart` (materialises session state, rewrites PID/`HelloSentAt`,
and sends the spawn hello), `SessionEnd` (implicit-session cleanup),
`PostToolUse` (tool-use log). moxy registers a PreToolUse/PermissionRequest hook
that returns the moxin `perms-request` verdict to the client (moxin(7)
PERMISSIONS; moxy#484 names the events).

**The SessionStart hello** is a file, not a chat message, in the confined case:
`spawnhandshake.SendHello` atomically writes `<handshake dir>/<sha256(worker\0driver)[:16]>.json`
containing `{worker, driver, sent_at, posh_session_id?}`; a confined worker finds
the directory in `SPINCLASS_HANDSHAKE_DIR` (spinclass `internal/spawnhandshake/handshake.go:31-37,57,96`;
FDR 0035 "A confined worker never blocks…"). The driver blocks on it (60 s default,
spinclass FDR 0006). Who sends it when no hook runs is an open question (Q9).

Disposition: the hello is **reproduce** (the loop, or clown on its behalf,
writes it at the same point: harness up, tools connected); `SessionEnd` cleanup
and `PostToolUse` logging are **reproduce or replace** (the unit's exit and the
turn log respectively), and are spinclass's contract to restate.

### 1.3 Permission model and the allow list

Claude Code decides per tool call: allow rules, hook verdicts, mode
(`default`/`dontAsk`/`auto`/…), and, when nothing decides, **ask the human**.
spinclass keeps its allow list as Claude rules in a `settings.local.json`
(`permissions.allow`, `internal/perms/settings.go:15-19`) and in tier files
(`Tier{Allow []string}`, `internal/perms/tiers.go:11-13`); rules are in Claude
Code's rule grammar (`Bash(...)`, `mcp__…`). clown adds `--disallowed-tools`
from a file (`internal/provider/claude.go:52-58`) and the hook above.

Disposition: **reproduce with a different semantics**: three-valued (`allow`,
`deny`) with no `ask`. Design in §3. The Claude-rule grammar is not carried over;
spinclass's tier files need a translation or a second list (Q7).

### 1.4 Tool search / deferred tools

Claude Code defers tool definitions and loads them on demand (the session system
prompt says "some tools are deferred"; the plan's §3 notes betas
`inline-tools-2026-09-15` and `mid-conversation-tool-changes-2026-07-01`, with the
actual wire behaviour **UNVERIFIED**). The loaded set changes within a session
(the plan observed 287 → 306 tools when a connector came online).

Disposition: **drop**. The loop sends an explicit, fixed `tools` list from the
brief (FDR 0019 §3) plus `cannot_complete`; there is nothing to defer. A worker
that needs a broad catalogue can be offered the three-verb aggregator
(`clown-mcp-collapse`, `internal/mcpcollapse`) as its one tool. The loaded set
is known exactly, which makes the unused-tools plan trivial (§1.12).

### 1.5 Compaction

Claude Code compacts the conversation and writes `compact_boundary`
(toolcost counts `clown.compaction`). The juggler loop has **no compaction**: it
re-sends the whole transcript every step (`jugglerloop.run.wireTurns`,
`loop.go:226-229`) and relies on `MaxSteps` (default 12, `loop.go:15`) and
moxy's blobbing of oversized results. That is adequate for FDR 0019's 2–4-call
agents and not for a worker that edits code across hours.

Disposition: **reproduce (Stage 2)**: a context budget measured from provider
usage (1.12), with a documented policy (truncate old tool results to
`{truncated, sha256, bytes}` markers — the shape `roomStanza` already uses —
before summarising). Until then a worker has a hard step and token cap, and
exceeding it is a `failed` run, not silent degradation.

### 1.6 Subagents

Claude Code's `Agent` tool runs in-process subagents with their own transcripts;
clown ships `subagents/discover.md` and the Explore rewrite. juggler's
equivalent is a separate process: `juggler spawn` (a transient unit, a
ringmaster job, a handle) and the older single-turn `juggler-prompt` tool.

Disposition: **drop in-process subagents for v1**. A confined unit cannot spawn
sessions (spinclass FDR 0035: `sc spawn` from inside a unit is refused); a worker
that needs help asks its driver by chat.

### 1.7 MCP client behaviour

What clown hands Claude Code is a compiled manifest (`internal/pluginhost`,
`clown-plugin-host`): HTTP URLs for servers clown launched, stdio servers
wrapped by `clown-stdio-bridge` (FDR 0002), a per-server `timeout` forwarded into
the manifest (`internal/pluginhost/config.go:49-53,309-317`), and, in agent scope,
loopback ports reserved inside the unit and spliced to frontend unix sockets
(`clown agent-relay`, FDR 0020 "Forwards replace loopback URLs"). Claude Code's
own client provides the streamable-HTTP/SSE session, the server-initiated
announcement channel (RFC 0002), reconnection and its default per-tool timeout.
The operator has seen SSE streams dropped at 360 s in confined units; that figure
is **not** in clown's source or docs (searched) — treat as an operator
observation, cause **UNVERIFIED** (candidates: the egress proxy, the relay, or
the client's own idle policy).

juggler's client (`internal/jugglertools/client.go`) is deliberately smaller:
exactly **one** upstream; POST-only JSON-RPC through `mcphttp.PostJSONRPC`;
`Mcp-Session-Id` captured and echoed; 30 s per-request timeout
(`DefaultCallTimeout`, `client.go:49`); 1 MiB response cap (`:45`); no GET
stream, no server-initiated requests, no reconnection. Because it holds no
long-lived stream it is not exposed to idle-SSE drops, but a long tool call is
bounded by 30 s and a response delivered as SSE is still one held connection.
**UNVERIFIED:** behaviour when moxy restarts and the session id is invalidated
(no re-initialise path was found in `client.go`).

Disposition: **reproduce what workers use, drop the rest**: per-tool timeouts
from the manifest; session re-initialise on a 404/invalid-session; connect to
more than one upstream (moxy plus the forwarded frontend-class ringmaster and
troupe servers, which are plain loopback HTTP inside the unit); keep dropping
server-initiated requests and progress notifications. Wakes (1.10) do not need
the announcement channel.

### 1.8 The system prompt clown composes

For Claude, clown builds `--system-prompt-file` (replace), `--append-system-prompt-file`
(fragments from `.clown/system-prompt.d`, the repo's `system-prompt-append.d/`,
and plugin servers that opt in with `systemPrompt: true`, fetched by `prompts/get`
at launch within a 3 s budget, `internal/pluginhost/host.go:26`), `--agents`,
`--disallowed-tools` and `--settings` (`internal/provider/claude.go:52-99`). In
agent scope, dynamic fragments from in-unit servers are **not** fetched (FDR 0020,
"What a confined worker loses": the frontend cannot reach in-unit servers).
Claude Code adds its own base prompt, project instructions (`CLAUDE.md`/`AGENTS.md`),
skills listing and memory on top. A juggler agent's system prompt is
`brief.system` verbatim.

Disposition: **reproduce selectively**. The composer (`internal/promptwalk`) can
run in clown's frontend and produce a string for the brief's `system`; fragments
that describe Claude Code tooling or runtime claims are harness-specific and
must be reviewed per fragment (the content of `system-prompt-append.d/` was not
read for this record: UNVERIFIED which of them name Claude Code features).
Loading the repo's `AGENTS.md` into the prompt is **required** (workers rely on
it); skills and auto-memory are **dropped** for v1.

### 1.9 Resume and session identity

`clown resume clown://claude/<id>` re-enters a session; clown binds the harness
session id to the per-instance key (`CLOWN_SESSION_ID` == `--session-id`,
`cmd/clown/resume_hint.go`, FDR 0018); spinclass substitutes `{principal}` in the
resume entry (FDR 0035 "The principal across `sc resume`").

Disposition: **reproduce (Stage 3)**: a resumed loop replays the local turn log
(1.1) into the transcript and keeps the principal. v1 has no resume: a confined
worker is restarted from its brief and its worktree.

### 1.10 The attach / terminal surface and job wakes

Claude Code is a TUI. In agent scope clown always runs the `ptysuspend` relay,
allocates a pty the unit owns, and `clown agent-exec` does `setsid` +
`TIOCSCTTY` before exec'ing claude (FDR 0020 "The terminal"). Wakes (job
completions, chat messages) arrive via the plugin monitors (`ringmaster monitor`,
`troupe receive`) whose stdout lines Claude Code turns into user input.

Disposition: **drop the pty**; **reproduce the wake**. A loop needs no tty (the
unit record's `pty` is allowed to be empty: FDR 0035 Interface). It needs an
**inbox**: between steps it reads wake lines and chat addressed to its principal
and injects them as user turns. For observation, the per-turn MUC post and the
local turn log are the watch surface; `posh attach` has nothing to attach to
(Q11).

A semantic difference matters here. FDR 0019's agents are **task-shaped**:
`end_turn` ends the run and the evaluator decides success. spinclass workers are
**session-shaped**: after finishing a turn they idle until the driver messages
them and end only on close. The loop therefore needs two modes (§2.3).

### 1.11 Built-in tools

Claude Code supplies Read/Write/Edit/Bash/Glob/Grep/WebFetch/WebSearch/Agent/
Skill/NotebookEdit and more. The confined in-unit moxy allows `folio, grit, rg, jq,
man, chix, env, batch, async, async-result, async-cancel, status, restart`
(`internal/agentscope/inunit.go:66`), which covers files, git, search and nix but
has **no general shell**. FDR 0019 also forbids `restart`, `batch`, `async*` and
`status` in a brief (§5), so a loop worker loses background execution.

Disposition: **replace** file/search tools by moxy's; **open** for Bash (Q3):
options are moxy's `chix` develop-run, the `just-us` recipes server (recipes
are the repo's "paved paths", CLAUDE.md "Justfile recipes"), or a bounded exec
tool whose argv allowlist is the policy.

### 1.12 Metrics (clown#263 and the unused-tools plan)

Today statsd lines come from parsing the transcript after the fact
(1.1). The loop currently discards what those metrics need: neither codec
returns provider `usage` (`ModelReply` carries only `Turns` and `Stop`,
`codec.go:48-51`), there is no `cache_control` and no streaming. clown#263 asks
for the same families emitted natively with identical names; the plan's
`LoadedTools` seam (§5) becomes trivial because the loop composes each request
and knows its tool list exactly.

Disposition: **reproduce natively, same names**: the loop emits
`clown.tool_result.*`, `clown.tokens.*`, `clown.responses.*`, `clown.context.*`,
`clown.compaction.*` and the plan's `clown.tools.{loaded,called,unused,…}` from
its own state, exactly (no `message.id` grouping, no late-flush offsets). In a
confined unit they leave through the existing statsd relay (FDR 0020 "statsd
relay", capability `statsd-relay`), as that record already directs for future
in-unit metrics. Prerequisite (Stage 1): capture `usage` in both codecs.

### 1.13 Things the Claude Code path provides that are easy to miss

- **Retry/back-off** on 429/5xx/overloaded: `jugglerloop.RoundTrip` makes one
  attempt and any non-2xx ends the run `model_error` (`codec.go:79-105`).
  **Reproduce.**
- **Streaming** and long outputs: the loop is non-streaming with
  `DefaultMaxTokens = 4096`. **Reproduce** when real code-writing workloads
  need it; whether the egress proxy tolerates long non-streaming responses is
  **UNVERIFIED**.
- **Prompt caching**: none in the loop. **Reproduce** (cost, Q1).
- **Extended thinking**: not in the codecs. **Drop** for v1.
- **Credentials**: Claude Code uses the operator's login credentials, bound into
  the unit (`.credentials.json`, FDR 0020 socket table). A juggler loop needs a
  provider API key from the model registry. This is a billing and
  confinement change (a key readable inside the unit), Q1.
- **Pre-merge attestation**: spinclass's `merge-this-session` requires a
  `nothing-but-the-truth` attestation naming skills (simplify, review, …).
  The attestation is an MCP tool and callable by the loop; the Skill tool is not
  available, so `used:false` with reasoning is the only truthful answer today
  (spinclass accepts any non-empty reasoning; the tool description says "lenient
  on content"). Whether that is acceptable is Q7.

## 2. What juggler already is

Read from FDR 0019 and the code, not inferred.

### 2.1 Existing pieces

| Piece | What it is | Source |
|---|---|---|
| `juggler run` | headless process: reads a brief, drives the tool-use loop against one moxy upstream, records ledger and `traffic.jsonl`, writes the ringmaster terminal | FDR 0019 §1; `internal/jugglerrun/agent.go` `RunAgent` |
| the loop | `jugglerloop.Run`: step cap (12), wall clock (2 m), `cannot_complete` tool, `StopWhen` evaluator hook, stanza-shaped `Turn`s, two codecs (Anthropic Messages, OpenAI-compatible) | `internal/jugglerloop/loop.go`, `codec.go` |
| the brief | `schema, principal, parent, room, model, system, task, moxyfile, env, tools, evaluator, limits` | FDR 0019 §3; `internal/jugglerbrief` |
| tool plane | juggler launches `moxy serve-http --name-template '{server}_{tool}'` from the brief's inline moxyfile, with `MOXY_PARENT_MOXYFILE` pinning the spawner's effective moxyfile; `allow-tools` is **derived from `tools`** | FDR 0019 §5; `agent.go:292-321` |
| call-time allowlist | the loop refuses any tool name not offered (clown#261, closed) | `loop.go:315-328` (read) |
| arguments audit | `traffic.jsonl`, per call, full arguments, keyed by principal and job (clown#255, closed) | FDR 0019 §4 |
| lifecycle | ringmaster job, advisory lock, `ExecStopPost=juggler exit-wake`, `systemd-run --user` transient unit | FDR 0019 §6, §9 |
| identity | per-run troupe mint, a MUC per run, the run root | FDR 0019 §1, §2, §7 |
| decisions | `juggler decide` router; not a loop | FDR 0019 §1 |

`--provider juggler` in `clown` is a **stub that exits 1**
(`cmd/clown/main.go:2247-2257`, pointing at FDR 0011); the matrix FDR 0006 lists
the harness axis as claude/codex/circus/opencode/clownbox. `--agent-scope` is
refused for any provider but claude (`main.go:805`).

### 2.2 Gaps against §1

| Need (from §1) | juggler today | Gap |
|---|---|---|
| durable turn log, resume | ledger at end, traffic per call | append-only turn log; replay |
| session shape (park on inbox) | task-shaped; `end_turn` ends the run | session mode |
| wakes/chat inbox | none; one-shot | inbox between steps |
| compaction, context budget | none; full history every step | budget + policy |
| provider usage, cache control, streaming, retry | none | codecs + `RoundTrip` |
| native metrics | none | emitter behind the `LoadedTools` seam; statsd |
| multiple MCP upstreams, session re-init, per-tool timeouts | one upstream, 30 s flat | client work |
| system prompt composition | `brief.system` only | reuse `promptwalk` output |
| permission policy with argument-sensitive rules | exact tool names only | §3 |
| hello for the spawn driver | none | §4 |
| confinement | FDR 0019 limitation: namespaces deferred; clown#252 open | reuse FDR 0020's unit (§4) |
| git identity | none | §5 |
| a shell | none | Q3 |

### 2.3 The two loop modes

- **task mode** (FDR 0019 as built): `end_turn` ends the run; the brief's jq
  evaluator measures success; one ringmaster job per run.
- **session mode** (new): `end_turn` parks the loop on its inbox; the run ends on
  `close`/cancel, a wall-clock or budget limit, or `cannot_complete`. The
  evaluator is optional (a spinclass worker's "success" is a merged commit, which
  spinclass already knows).

Both share the loop, the policy, the ledger and the turn log.

### 2.4 Related records and where they stand

- **clown#261 (closed):** `tools` is enforced at call time and by the derived
  `allow-tools`; verified in `loop.go:315`. This is the allow-list mechanism §3
  builds on. It does **not** cover arguments.
- **clown#255 (closed):** `traffic.jsonl`. Becomes part of the turn log's
  neighbourhood; keep it.
- **clown#252 (open):** juggler agents run unconfined as the launcher's user. The
  confined-worker path in §4 gets confinement from FDR 0020's unit instead, which
  answers #252 for that path only; the Pebble webhook path is unchanged.
- **clown#260 (open):** under ringmaster FDR-0001 juggler shrinks to `juggler run`;
  `spawn`, `exit-wake`, the in-process job lock and the `ExecStopPost` hook retire
  at service step 2; the launch document replaces env construction. Consequence
  for this design: nothing here may depend on `juggler spawn` or the
  `ExecStopPost` hook. A confined worker launched through clown does not
  (§4.5).
- **clown#258 (open):** extract juggler into its own repo, "stays unentangled
  from clown internals in the meantime". This design adds reasons to reach into
  clown (`agentscope`, `promptwalk`, `pluginhost`, `staging`); §6 keeps those on
  clown's side of a narrow seam.
- **clown#263 (open):** native metrics; §1.12.
- **clown#274 (open):** staged git identity; §5.

## 3. Permission policy: refuse, never ask

### 3.1 Rule

The loop's policy function has two outcomes, `allow` and `deny`. There is no
`ask`, no pending state and no code path that waits for an answer. Any input that
would be an `ask` in another system (a moxin's `each-use`, a `dynamic` verdict of
`ask`, a tool the policy does not mention, an argument outside a rule's scope) is
a `deny`. The default is deny.

Property to test (hermetic, with a fake model): a model that emits every class
of unlisted or denied call terminates the run in bounded time with every call
answered by a tool_result error and a ledger entry. This is the structural fix
for the hang class that dontAsk and moxy#484 patch one door at a time.

### 3.2 Layers

1. **Offer-time**: only the brief's `tools` are sent to the model (exists).
2. **Call-time, in the loop**: a name not in the offered set is refused before the
   executor (exists, clown#261). New: an argument-sensitive check (3.3) before
   `Exec.Call`.
3. **At the tool server**: the agent's moxy carries `allow-tools` derived from
   `tools` and is parent-pinned to a superset reference (exists; FDR 0019 §5).
   Authoritative against a loop bug and against out-of-band loopback calls.
4. **The unit**: mount, pid, network and devpts namespaces (FDR 0020). A policy
   bug cannot reach `$HOME`.

### 3.3 Expressing the allow list

Tool names are the brief's `tools` (exact, `<server>_<tool>`). Argument
constraints are the part that is new, because the Claude path had them (a Read
outside the allow list is refused; moxy's folio classes paths `dynamic`). Two
options, not mutually exclusive:

- **(a) Reuse moxy's verdict.** moxy already resolves per-call verdicts for its
  moxins: `permcheck.Resolver.Resolve(tool, args, cwd)` returns allow, ask, deny
  or unknown (`moxy/internal/permcheck/permcheck.go:52-75`), consumed today by the
  PreToolUse/PermissionRequest hook adapter and the `batch`/`async` meta-tools.
  The loop maps ask and unknown to deny. This needs moxy to expose the resolver
  to a non-hook client: either a headless mode in which moxy's own call funnel
  resolves and denies (authoritative, enforced where the tool runs, the same
  place `allow-tools` is enforced, `proxy.go:1056-1069`), or a check endpoint the
  loop consults before every call. Recommendation: the funnel mode as the
  authority, with the loop pre-check only for a clearer error message. This is
  moxy's lane (Q7).
- **(b) A loop-owned rule table** in the brief (new field) for what moxy cannot
  express: path scopes for `folio_*`, command scopes for an exec tool, repo scopes
  for `grit_*`. Evaluated in the loop; every miss is deny. spinclass's tier files
  (`Allow []string`, Claude rule grammar) would be translated by spinclass or
  replaced by a second tier format.

**Important and UNVERIFIED:** FDR 0019 states that "any moxy permission tier other
than `always-allow` resolves to **deny** for a juggler agent" and `jugglertools`
assumes moxy denies and returns an `isError` result (`client.go` package doc). In
moxy's source I searched for "headless" and "juggler" and found no hits, found
the resolver used by `batch`/`async` and the hook adapter, and found the plain
call funnel enforcing `allow-tools` but did **not** find it consulting the
resolver. If that reading is right, a `[perms: each-use]` or `dynamic` tool
listed in `tools` is callable by a juggler agent **without any verdict**, which is
the opposite of the posture the record claims. This must be settled by moxy's
session before the loop is offered anything that is not `always-allow` (Stage 1
gate). Until then Stage 1 briefs list only tools whose moxin tier is
`always-allow`.

### 3.4 Denials are information, not a stall

A denial returns a tool_result error with a fixed shape the model can act on:
which rule denied, that this session cannot ask, and the two ways forward (use an
allowed tool, or call `cannot_complete`). The ledger records `{tool, kind:"denied",
ok:false, rule}`. To stop a model from spinning on denials (the loop's analogue
of the auto-mode "repeated blocks" fall-back, but ending instead of asking), a
**denial budget** (default 5 per run, brief-configurable) ends the run `failed`
with reason `denial_budget`.

### 3.5 Escalation without waiting

"Refuse, never ask" does not forbid *telling* someone. A worker that is blocked
may message its driver (chat) and, in session mode, park on its inbox; the driver
decides whether to re-spawn with a wider allow list. The difference from asking
is that the loop never holds a pending call open: the denied call is already
answered. Whether a dedicated non-blocking `request_permission` tool is worth
having is Q6; FDR 0019 currently states "No ask-back" for task mode.

## 4. How a confined worker fits

### 4.1 Shape

clown stays the launcher and frontend; spinclass stays the session manager. The
only change in the unit is the process: `juggler run` (session mode) instead of
`claude`. clown synthesises a **brief** from what it already has (the spawn
prompt as `task`, the composed system prompt, the profile's model, the per-worktree
room as `room`, the driver as `parent`, its own session key as `principal`).

```
spinclass spawn-session  ──(profile)──>  clown --agent-scope <spec> [harness=juggler-run]
   frontend: clown, troupe owner, egress proxy, statsd relay, frontend MCP sockets
   unit:     clown agent-exec ──> juggler run (session mode)
                 ├─ moxy (launched by juggler, parent-pinned to InUnitMoxyfile)
                 ├─ forwarded ringmaster/troupe MCP (loopback, frontend-class)
                 └─ model API over HTTPS_PROXY (egress allowlist)
```

### 4.2 Which FDR 0020 / 0035 assumptions are Claude-Code-specific

| Assumption | Where | With the loop |
|---|---|---|
| provider must be claude | `cmd/clown/main.go:805`; spinclass `SpliceDontAskMode` refuses non-claude | new harness must be accepted by both; the dontAsk splice is unnecessary (the loop cannot ask) but today a non-claude provider is *refused* for a confined spawn, so spinclass must learn the new harness (Q7) |
| a pty, `ptysuspend`, `TIOCSCTTY` shim | FDR 0020 "The terminal" | not needed; `pty` empty in the unit record (FDR 0035 allows "empty if none"); F8b holds trivially, F3's "controlling tty must be the unit's own pty" clause presumes a pty, so spinclass must confirm F3 with none (Q7) |
| `CLAUDE_CONFIG_DIR`, credentials file bind, transcript-dir bind | FDR 0020 socket table, "claude itself" row | not needed; replaced by a model credential reference (Q1) and the turn log |
| `ANTHROPIC_*` env allowlist | FDR 0020 Environment | model/credential env comes from the registry entry instead |
| Claude Code hooks run in the agent scope | FDR 0020 scope table | no hooks; in-process equivalents (§1.2) |
| **F11 finds agents by `comm`** (`claude`, `.claude-wrapped`) | FDR 0035 F11, Limitations | a `juggler` process is **not** recognised as an agent runtime; an *unconfined* loop worker would be invisible to F11. spinclass must add the name (Q7) |
| claude's `--session-id` == principal | FDR 0018, `resume_hint.go` | the loop takes the principal from `CLOWN_SESSION_ID`/the brief; FDR 0019 already requires `env == brief.principal` |
| in-unit moxy via `clown agent-relay --serve` | FDR 0020 "In-unit servers" | **not Claude-specific**; but the loop should launch its own moxy per brief (below) |
| frontend-class MCP forwards; statsd relay; egress proxy; `PrivateNetwork`; `ProtectHome=tmpfs`; the spec/result files; capability query | FDR 0020, 0035 | **not Claude-specific**; unchanged |
| unit record's `principal`, F1, F15 | FDR 0035 | unchanged; clown still writes the record |

### 4.3 Tool plane inside the unit

Two ways to reach moxy: (i) the relay'd moxy clown already starts for Claude Code,
via `--moxy-url`; (ii) the loop launches its own moxy from the brief's moxyfile.
(i) forfeits the derived `allow-tools` (FDR 0019 §5: the `--moxy-url` path
derives none) so the broad `InUnitMoxyfile` allowlist
(`inunit.go:66`) would be the only moxy-side limit. **Recommendation (ii)**, with
`MOXY_PARENT_MOXYFILE` set to the `InUnitMoxyfile`-based reference so the brief's
tools must narrow the unit's own ceiling. The agent-relay'd moxy is then not
started for this harness.

### 4.4 Egress, credentials and local models

The loop's `http.Client` must honour `HTTPS_PROXY` (Go's default transport does
when the client is `http.DefaultClient`; UNVERIFIED for the custom client
`AgentDeps.HTTPClient` the cmd wires). The model host must be on the sweatfile's
`egress_allow`. A model API key inside the unit is readable by the agent; the
registry's credential reference scheme (FDR 0019 §10, "a credential reference,
never a secret") should point at a file bound read-only and scoped to one key with
a spend limit (FDR 0019 Tuning Levers). A **local** llama-server is unreachable
(FDR 0020: loopback is the unit's own), so confined loop workers use remote
registry entries until a relay exists.

### 4.5 Supervision

FDR 0019's supervision (systemd transient unit with `ExecStopPost`) is not used:
the unit is clown's (`systemd-run --user --wait --collect`), clown's frontend waits
for the exit code and can emit the exit wake itself, and the ringmaster job is
created by the worker's own identity. This is the shape that survives clown#260
(service step 2 retires `ExecStopPost`). The loop must still emit the terminal
record on every ending it controls; the host-wide reaper covers the rest (FDR 0019
§6).

### 4.6 Hello

At the point the loop has connected its tools and is ready for its first model
call it writes the handshake file (§1.2). The writer should be **clown's
frontend**, not the loop, if the contract allows: the file lives in a directory
the unit can write and the driver reads, and the frontend already knows both
principals. Whichever side writes it, the contract (path, JSON fields) is
spinclass's and must be restated for a non-hook harness (Q9).

## 5. Identity, commit authorship and signing

Context: confined workers have no git identity today. The operator's
`~/.gitconfig` is hidden by the private home, so the worker invents one
(clown#274: "Author identity unknown", the first confined worker wrote its own
`.gitconfig`). `commit.gpgsign` is currently unset on flic (operator statement).
The operator is deciding whether agents commit under their **own** identities
rather than the operator's. This record does not decide that.

### 5.1 What the loop changes

1. **Identity becomes a launcher input, not something a tool discovers.** The loop
   (or clown) sets `GIT_AUTHOR_*`/`GIT_COMMITTER_*` in the environment of moxy's
   tool servers; `GIT_*` is already a launcher-owned key group that a brief may
   not set (FDR 0019 §3). Environment overrides config, so an agent that writes a
   `.gitconfig` into its agent home (what the first worker did) does not change who
   the commit is attributed to. Under the Claude path the same env could be set,
   but git runs from claude's Bash, where the agent controls the environment.
2. **Every commit is a recorded tool call.** With git behind `grit_*`, a commit is a
   ledger entry and a `traffic.jsonl` line with its full arguments, attributable to
   a principal and a turn.
3. **The agent has a natural identity to attribute to**: a per-instance key
   (`CLOWN_SESSION_ID`), a clown name, and a troupe JID. That makes "own identity"
   cheap to implement; whether it is *wanted* is the operator's call.
4. **Signing stops being ambient.** The piggy-agent socket is passed into the unit
   and every act prompts the *operator* (FDR 0020 socket table). A headless worker
   running `git commit` with signing on would block on a prompt nobody is watching:
   the hang class again, one layer down. "Refuse, never ask" therefore extends to
   signing: either the commit does not request a signature, or the signer is one
   that does not need an operator at the keyboard.

### 5.2 Options (not decided)

| Option | Author / committer | Signed | Notes |
|---|---|---|---|
| A. Operator identity, staged by the launcher (the minimal #274 fix) | operator / operator | no | simplest; attributes agent work to the human; provenance only in troupe stanzas and the ledger |
| B. Agent identity | agent / agent | no | `name` from the clown name or session key; `email` form is the operator's to choose (a noreply form on a fleet domain, or the troupe JID); the operator is named by a `Co-authored-by` trailer or not at all |
| C. Split | agent / operator | no | git models this natively; **UNVERIFIED** that `merge-this-session`'s rebase (run in the frontend with the operator's config) would rewrite the committer to the operator, which would yield C for merged commits without extra work |
| D. Agent identity, signed | agent / agent | yes, by an agent-class key | needs a signer that authenticates by cgroup and signs without an operator prompt (FDR 0032 D10 / piggy FDR 0006 lane); not available today |

Independent of the option: how a worker's identity relates to **who pushes** (the
frontend's `merge-this-session`, with operator credentials) is spinclass's design,
and the second half of #274 (the main `.git` is read-only in the unit, so ref
updates after a commit fail loudly) is unrelated to authorship.

## 6. Staged path

### Stage 0 — settle the gates (no code)

- moxy: what a plain `tools/call` does for `each-use`/`dynamic`/unknown tools, and
  whether a headless funnel mode is acceptable (§3.3). **Gate for any tool not
  `always-allow`.**
- spinclass: the hello contract for a non-hook harness; F11 name matching;
  acceptance of a non-claude confined provider; the attestation story (§1.13).
- operator: Q1 (credentials/billing), Q2 (session mode), Q5 (how a spawn selects
  the harness), identity option (§5).
- Measure the baseline on the Claude path so Stage 1 has something to beat:
  confined workers' hang count and time-to-hello (spinclass lifecycle log), which
  builtin tools and moxy tools workers actually call (toolcost
  `clown.tool_result.*`, and the unused-tools plan once built), tokens and cost
  per session (`clown.tokens.*`). This tells us whether Bash/Edit, which the
  loop lacks, dominate worker traffic.

### Stage 1 — task-mode confined worker, read-mostly

- Capture provider `usage` in both codecs; emit the toolcost families natively,
  names unchanged (clown#263 slice), through the statsd relay.
- Durable local turn log.
- Policy layer with deny-only semantics, denial budget, and the hermetic
  "no waiting path" test (3.1).
- New harness in clown's matrix (selection per Q5), brief synthesis from the spawn
  prompt, `--agent-scope` accepts it, capability `headless-loop` in
  `agent-scope-capabilities`.
- Hello written at connect time.
- Briefs in this stage: tools that are `always-allow` and file/search/git-read
  based; no commits.
- Measured: confinement check passes against a loop worker (F1–F15 as reported
  by `sc confinement-check`); the hang class is gone by the test, not by
  absence of observation; metrics parity vs the Claude path on a replayed brief
  (same families present, per-response token totals within the provider's own
  accounting); time-to-hello; steps and denial counts.

### Stage 2 — session mode and the write path

- Inbox (wakes and chat), `end_turn` parks, `close` ends.
- Retry/back-off, prompt caching, streaming, context budget and compaction.
- A shell decision (Q3); commits through `grit_*` once the identity option is
  chosen (§5); `AGENTS.md` loading in the composed prompt.
- Measured: merged-change rate and cost per merged change vs the Claude path on
  comparable briefs; context growth per step; retry counts.

### Stage 3 — parity and default

- Resume from the turn log; the attestation path; a watch/steer surface.
- Only then consider making the loop the default for confined spawns. The Claude
  Code path remains for interactive, unconfined and human-attached sessions
  regardless.

### How a spawn chooses

spinclass's `spawn-session` already accepts a clown **profile** and places
`--profile <name>` among clown's flags, refusing unknown or skewed registries
(spinclass FDR 0034; `pkg/profilelookup` is the shared resolver). Two ways to make
the loop selectable:

- **Harness value (recommended).** The profile's `provider` field is the harness
  axis in FDR 0006's mental model. Add a harness (e.g. `juggler-run`; the name
  `juggler` is taken by the stub at `main.go:2247`) and a `Validate` combination
  for it. No new profile field. `pkg/profilelookup`'s canonical form already
  includes `provider`, so existing digests are unchanged; whether a new *value*
  needs a `ProtocolVersion` bump is for the `profilelookup` package doc to say
  (its doc is the normative spec) and spinclass to confirm. UNVERIFIED.
- **A new `loop` profile field.** Cleaner conceptually (harness, provider, model,
  API stay orthogonal) but adds a canonical-form field, which per `AGENTS.md`
  means a `ProtocolVersion` bump announced to consumers first.

In both, a profile also fixes the model (a registry name) and the credential
reference.

### Coexistence

Both paths share the launcher, the unit, the identity, the confinement check and
the spawn handshake. They differ only in the process in the unit and in where
transcript, hooks and metrics originate. A worker's metrics carry no harness
segment today; add one (or a separate family) so the two can be compared without
dashboard changes breaking.

## Risks

1. **Capability and quality regression.** Claude Code's scaffolding (prompts,
   tool descriptions, edit tools, compaction) is substantial; a thin loop may
   complete fewer tasks. Mitigation: Stage 1 is read-mostly, Stage 2 compares on
   the same briefs, the Claude path stays.
2. **Billing change.** A loop calls a provider with an API key; the Claude Code
   path uses the operator's login. Whether login credentials may be used by a
   custom loop is **UNVERIFIED** and presumed not. Cost per task is unmeasured.
3. **A key inside the unit.** The agent can read what the loop reads. Per-run,
   spend-limited keys (FDR 0019 Tuning Levers) bound it.
4. **Reliance on moxy's permission behaviour** that I could not confirm (§3.3).
   Worst case today: tools beyond `always-allow` run ungated.
5. **Two harnesses to maintain**, and dashboards that must treat their metrics as
   one family.
6. **Extraction entanglement** (clown#258): the loop must not import `cmd/clown`;
   `promptwalk`/`agentscope`/`staging` stay on clown's side, passed in as strings
   and file paths (the brief, the spec).
7. **Long tool calls vs a 30 s client timeout; no session re-initialise; no
   retry.** Each is a flaky-run source until fixed.
8. **No resume, no compaction in Stage 1**: long sessions fail rather than
   degrade; this is deliberate and visible.
9. **F11 blind spot** if an unconfined loop worker exists before spinclass learns
   the process name.
10. **Loss of async/batch** (reserved from briefs by FDR 0019) for loop workers.

## Open questions

Operator:

- **Q1. Credentials and billing.** Is a per-token API key acceptable for confined
  workers? Which provider (Anthropic direct, OpenRouter), and per-run keys with
  spend limits (FDR 0019 Tuning Levers)? Is Claude-login use by a custom loop
  permitted at all (UNVERIFIED)?
- **Q2. Session mode in v1?** Or only task-shaped workers until a use case
  demands parking?
- **Q3. A shell.** Which of moxy `chix` develop-run, the `just-us` recipes server,
  or a bounded exec tool, if any, belongs to the loop's toolset?
- **Q4. Identity and signing** (§5.2): options A–D; and whether a commit may
  request a signature at all from a headless worker.
- **Q5. How a spawn selects the loop:** new harness value or a `loop` profile
  field (§6)?
- **Q6. Non-blocking escalation:** only chat-and-park, or a dedicated
  `request_permission` tool that records the request and returns at once?
- **Q8. Where does the loop live** while juggler is mid-extraction (clown#258):
  keep it in `internal/jugglerloop` behind a narrow seam (recommended, per
  clown#263), or a clown-native package?

Other lanes (not decided here):

- **Q7. spinclass:** accept a non-claude harness for confined spawns and stop
  splicing dontAsk for it; add the loop's process name to F11; confirm F3 with no pty; translate or
  replace the Claude-grammar allow tiers; how an attestation naming skills is
  satisfied honestly. **moxy:** what a plain `tools/call` does for
  `each-use`/`dynamic`/unknown; a headless mode or check endpoint (§3.3).
- **Q9. Hello:** which side writes the handshake file for a non-hook harness, and
  is `SendHello`'s record the whole contract? Also whether the driver needs
  `posh_session_id` when there is no posh session.
- **Q10. ringmaster FDR-0001 timing:** the loop's job lifecycle (one ringmaster
  job per worker, terminal-of-last-resort) is assumed to survive service step 2;
  confirm.
- **Q11. Observation:** what `sc list` and posh show for a session with no pty
  (UNVERIFIED), and whether a watch/steer surface is wanted beyond the MUC.
- **Q12. The 360 s SSE drops** seen in confined units: where they come from
  (proxy, relay, or client) and whether the loop's POST-only client is truly
  immune for tool calls delivered as SSE.

## Interface (proposed, not built)

- harness `juggler-run` (name tentative); `clown --agent-scope <spec>` accepts it
  and `clown agent-scope-capabilities` advertises `headless-loop`.
- brief gains `mode = "task" | "session"` and a policy table (§3.3(b)); both are
  additive to schema 1 only if unknown-field handling allows it, else schema 2
  (UNVERIFIED: `jugglerbrief` strictness was not read for this).
- local turn log: `<agent state dir>/turns.jsonl`, one `Turn` per line, outside
  moxy's CWD for the same reason `traffic.jsonl` is (FDR 0019 §4).
- ledger entry kinds added: `denied`, `denial_budget`; `end.reason` added:
  `denial_budget`, `closed`.

## Limitations of this record

- Claude Code's internals were not read; its behaviour is inferred from clown's
  hook code, the toolcost plan's read of one 2.1.301 transcript, and spinclass's
  records.
- moxy's source was read only to the extent of the permission paths cited.
- The system-prompt fragments, `jugglerbrief` parsing and the egress behaviour of
  the juggler HTTP client were not read; marked above.
- No measurement was taken. Stage 0 lists the measurements that would replace the
  guesses here.

## More Information

- clown FDR 0019 (juggler agent substrate); FDR 0020 (agent scope confinement);
  FDR 0006 (matrix); FDR 0018 (clown name and session lineage); FDR 0002 (stdio
  bridge); RFC 0017 (dumbo mock model API, the loop's test double).
- `docs/plans/2026-10-20-unused-tools-per-session.md` (the `LoadedTools` seam).
- spinclass FDR 0032 (principals, handles, provenance), FDR 0034 (spawn with a
  clown profile), FDR 0035 (session confinement; the hello, F1–F15, the dontAsk
  decision), FDR 0006 (spawn sibling sessions; the hello).
- ringmaster FDR-0001 (host service), via clown#260.
- clown#252, #255, #258, #260, #261, #263, #269, #274; moxy#484.
