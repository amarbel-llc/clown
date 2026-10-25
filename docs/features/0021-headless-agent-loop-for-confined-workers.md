---
status: exploring
date: 2026-10-20
issue: clown#271
related: FDR 0019 (juggler agent substrate), FDR 0020 (agent scope confinement), spinclass FDR 0032 / 0034 / 0035 / 0036, ringmaster FDR-0001, clown#252 #255 #258 #260 #261 #263 #274 #276 #277 #280 #281, moxy#484 #485 #486 #487, spinclass#383 #384, circus#351 #353
promotion-criteria: >
  proposed once the operator has answered the open questions marked
  "operator" below, moxy's session has decided moxy#486 (serve-time enforce
  mode) at least in shape, and spinclass's session has accepted the three
  contract changes in §4.6 (a non-claude confined harness, a unit-based
  agent census, the hello written by a non-hook harness). experimental once
  one confined worker runs a real brief to a merged commit on the headless
  loop on flic, with the confinement check passing, the no-waiting-path test
  of §3.1 green against a fake model, and the toolcost families arriving in
  stats-me from inside the unit.
---

# Headless agent loop for confined workers

> **Design record only.** Nothing here is implemented. Claims not read in
> source or measured are marked **UNVERIFIED**. Claude Code's own source was
> not read; its behaviour is taken from clown's code, hook payloads and the
> cited records.

## History

- **First pass, 2026-10-20 (commit 01aba92).** Written by a Sonnet session
  by mistake: the profile that should have selected a Fable model was
  overridden by the clownfile's default model (clown#281). Its structure and
  most of its findings stand.
- **Second pass, 2026-10-20 (this revision), Fable.** Same file, same
  number. Revised in place as the design owner. What changed, in order of
  weight:
  1. The first draft's central UNVERIFIED claim is now settled against it:
     moxy does **not** enforce its permission tiers on a plain `tools/call`
     (moxy#485, test bb446c3; read in `internal/proxy/proxy.go`). FDR 0019's
     "any tier but always-allow resolves to deny" is false on moxy's side.
     §3 is rewritten around that fact and says where enforcement must live
     (the unit, an in-unit moxy in enforce mode, then an embedded moxy) and
     why the loop's own policy is defence in depth, not a boundary.
  2. spinclass FDR 0036 §9 moved git for confined workers into the frontend
     `serve`, which stamps authorship. §5 (identity) is reshaped: the unit
     needs no git identity at all; the options are about the value the
     frontend stamps, and circus#351 keeps that undecided.
  3. The census problem is named precisely: spinclass finds agents by
     process name (`DefaultClaudeComms`), so a loop worker would be invisible
     to F11. §4.4 says what must change.
  4. Six UNVERIFIED items are resolved by reading source (§7). The 360 s SSE
     drop and the Claude-login question remain open.
  5. The spawn-selection section accounts for clown#276, #277, #280, #281 and
     spinclass#383, #384, and circus#353 is answered for where the loop's trust
     boundary sits.

Where this pass disagrees with the first, the text says so at the point of
disagreement.

## Problem Statement

Claude Code is built around a human at the terminal. A permission question
is answered on the tty; its auto mode documents a fall-back to asking; clown's
hooks, the attach surface and spinclass's hello assume a surface someone can
reach. A confined worker (FDR 0020 agent scope) has no such human by
construction: spinclass FDR 0035 F8a/F8b make the operator's tty and posh
attach socket unreachable from the unit.

On 2026-10-20 the first confined worker sat on a permission prompt for over
30 minutes until the operator attached and answered by hand (spinclass FDR
0035, "A confined worker never blocks on a permission prompt"). The mitigation
is `--permission-mode dontAsk`, spliced into a confined spawn's provider args
(`spawn.SpliceDontAskMode`). moxy#484 then showed a second door: under dontAsk
a `[perms: dynamic]` moxy tool left a `PermissionRequest` unanswered and the
agent idled on the call indefinitely. moxy 18ea283 closed that door (the hook
now answers deny for anything that is not an explicit allow or deny when the
session is in dontAsk; `internal/hook/hook.go:229-240,250-256`).

Each fix on the Claude Code path closes one door; the harness owns the others.
And the doors are not only in Claude Code: moxy#485 records that moxy's own
call funnel never consults its permission resolver, so a client with no Claude
Code hooks in front of it runs `each-use` and `dynamic` tools unchecked. Any
headless client, this loop included, inherits that gap unless it is closed
where the tools run.

The direction recorded in clown#271 is that clown can run a worker on a
**headless agent loop backed by juggler alone**, with no Claude Code process in
the unit: the loop reads a brief, calls the model through juggler, dispatches
tool calls to the in-unit moxy and the frontend-class MCP servers, applies the
session's permission policy itself (refuse, never ask), and emits what
spinclass, ringmaster, troupe and the toolcost metrics consume today.

This record lists what the Claude Code path provides that such a loop must
reproduce and what it may drop, what juggler already is, how the permission
policy works and who enforces it, how a confined worker fits, the identity
options, and a staged path. It does not decide the operator-owned questions.

## Goals and non-goals

Goals:

1. A confined worker has **no code path that waits for an answer from a
   human**. This is a property of the loop, testable with a fake model, not a
   property of a mode flag in a third-party client.
2. Permission enforcement is **authoritative outside the model's own process
   wherever the architecture allows it**, and the loop's own policy is a second
   line that gives better errors and bounded behaviour. The loop runs inside
   the unit as the model's own process and is not itself a trust boundary.
3. The loop is a first-class **harness** in clown's matrix (FDR 0006),
   selectable per spawn through a profile, coexisting with the Claude Code
   path.
4. spinclass, ringmaster, troupe and stats-me see the same facts they see
   today (hello, session lifecycle, turn stream, metrics) from a worker on
   either path.
5. The loop's tool surface is shaped for where the fleet is going: git through
   spinclass's frontend store tools (FDR 0036), file IO through a spinclass
   plugin later, moxy as a Go library (moxy#487) later. Nothing here should
   have to be undone for those.

Non-goals (v1):

- Replacing Claude Code for interactive, unconfined, human-driven sessions.
- Re-creating Claude Code's TUI, skills, slash commands, image/PDF reading,
  web fetch/search, in-process subagents.
- Deciding identity, signing, billing or lane questions that belong to the
  operator, spinclass, moxy or ringmaster (listed under Open questions).

## 1. What the Claude Code path provides today

Each row says where the capability lives, who consumes it, and the proposed
disposition: **reproduce** (the loop must provide an equivalent), **replace**
(consumers move to a different source), or **drop**.

### 1.1 Transcript JSONL

Claude Code writes the session transcript to
`~/.claude/projects/<slug>/<session>.jsonl` and per-subagent files under
`subagents/agent-*.jsonl` (`docs/plans/2026-10-20-unused-tools-per-session.md`
§1a; `internal/sessions/claude.go` lists sessions by walking
`~/.claude/projects`).

Readers inside the repo:

| Reader | What it takes from the transcript | Source |
|---|---|---|
| `clown-hook-toolcost` | per-response `message.usage`, `tool_use`/`tool_result` pairing (grouped by `message.id`), `compact_boundary`; offsets committed per transcript under `$XDG_STATE_HOME/clown/hook-toolcost/` | `internal/toolcost`; `cmd/clown-hook-toolcost` |
| `clown-hook-tee` | assistant `text` blocks since a persisted byte cursor; one message per turn into the per-worktree MUC | `cmd/clown-hook-tee` |
| `clown sessions` / resume listing | session discovery by mtime | `internal/sessions/claude.go` |
| freud (a moxy moxin) | session list, summary, tool usage, messages from `~/.claude/projects` | moxy's lane; internals not read |
| spinclass `PostToolUse` hook | appends the raw hook payload to `$XDG_LOG_HOME/spinclass/tool-uses/<session>.jsonl` | spinclass `internal/hooks/hooks.go:1013` |

Disposition: **replace**. The loop owns its transcript. FDR 0019 gives the loop
a stanza-shaped `Turn` (`internal/jugglerloop/turn.go`) posted to the run's MUC
per turn, but on disk the turns exist only as the final ledger (written once,
at `finish`) and `traffic.jsonl` (per call, durable at once). A SIGKILL leaves
traffic lines and no ledger (FDR 0019 §4). The loop therefore needs a
**durable, append-only local turn log** (one JSON line per `Turn`, flushed per
write) as the source for resume (1.9), the tee (1.2) and freud-class tooling.
Consumers are re-pointed at it; the Claude Code transcript reader stays for
Claude Code sessions.

### 1.2 Hooks

clown registers hooks through the synthesized `clown-builtin-jobs` plugin's
`hooks/hooks.json` (`cmd/clown/jobmonitor.go`):

| Event | Handler | What it does | Disposition |
|---|---|---|---|
| `PreToolUse` (matcher `.*`) | `clown-hook-allow` | allow `/nix/store` Read/Glob/Grep and the `clown-builtin-jobs` MCP tools; rewrite the `Agent` tool's `Explore` subagent to clown's read-only `Discover` | **reproduce as policy** (§3): the allow rules become entries in the loop's policy; the Explore rewrite is **dropped** with the Agent tool |
| `Stop`, `SessionEnd` | `clown-hook-tee` | tee assistant text into the group MUC | **reproduce in-process**: the loop already posts every turn to a room (FDR 0019 §7); the tee's cursor/settle machinery exists only because Claude Code flushes its transcript lazily |
| `Stop`, `SubagentStop`, `SessionEnd` | `clown-hook-toolcost` | statsd families `clown.tool_result.*`, `clown.tokens.*`, `clown.responses.*`, `clown.context.*`, `clown.compaction.*` | **reproduce in-process** (clown#263, §1.12) |

spinclass registers its own hooks in the worker (`hooks/hooks.json`, every
event handled by `${CLAUDE_PLUGIN_ROOT}/hooks/handler`): `SessionStart`
(materialises session state, records the worker's own principal, adopts the
PID, sends the spawn hello; `internal/hooks/hooks.go:96-112`), `SessionEnd`
(implicit-session cleanup), `PostToolUse` (tool-use log), and the sweatfile
edit guard (`deny-sweatfile-edits`, stage 1 of confinement, 8943ed0). moxy
registers a PreToolUse/PermissionRequest hook that returns the moxin
`perms-request` verdict to the client; under dontAsk it decides instead of
deferring (18ea283).

**The SessionStart hello** is a file, not a chat message:
`spawnhandshake.SendHello` atomically writes
`<dir>/<sha256(worker\0driver)[:16]>.json` containing
`{worker, driver, sent_at, posh_session_id?}`, where `worker` is the state's
session key verbatim and `driver` is the spawner's principal; a confined
worker finds `<dir>` in `SPINCLASS_HANDSHAKE_DIR`, a per-session directory the
frontend binds read-write (`internal/spawnhandshake/handshake.go:31-36,57,96`).
The driver polls it for 60 s by default (`spawn.DefaultHelloDeadline`,
`internal/spawn/spawn.go:28`). The sender is the SessionStart hook. Who sends
it when no hook runs is §4.5.

Disposition: the hello is **reproduce**; `SessionEnd` cleanup and
`PostToolUse` logging are **reproduce or replace** (the unit's exit and the
turn log respectively) and are spinclass's contract to restate.

### 1.3 Permission model and the allow list

Claude Code decides per tool call: allow rules, hook verdicts, mode
(`default`/`dontAsk`/`auto`/…), and, when nothing decides, **ask the human**.
spinclass keeps its allow list as Claude rules in a `settings.local.json`
(`permissions.allow`) and in tier files (`internal/perms/tiers.go`); rules are
in Claude Code's rule grammar (`Bash(...)`, `mcp__…`). clown adds
`--disallowed-tools` from a file (`internal/provider/claude.go`) and the hook
above. Argument-sensitive checks exist only as spinclass's `AlwaysAsk`
predicate and moxy's `dynamic` moxin predicates; spinclass FDR 0036 §2 notes
that `BuildPermissionString` discards MCP tool input, so a tier rule cannot
discriminate on arguments.

Disposition: **reproduce with a different semantics**: two-valued (`allow`,
`deny`) with no `ask`. Design in §3. The Claude-rule grammar is not carried
over; the loop's allow list is the brief's `tools` plus a rule table, and
spinclass's tier files need a translation or a second list (Q7, and
clown#276's profile-level permission scheme is the natural home).

### 1.4 Tool search / deferred tools

Claude Code defers tool definitions and loads them on demand (the session
system prompt says "some tools are deferred"; the unused-tools plan names the
betas `inline-tools-2026-09-15` and `mid-conversation-tool-changes-2026-07-01`
and observed the loaded set change mid-session). The wire behaviour is
**UNVERIFIED** and not needed here.

Disposition: **drop**. The loop sends an explicit, fixed `tools` list from the
brief plus `cannot_complete` (`jugglerloop.toolsWithCannotComplete`); there
is nothing to defer. A worker that needs a broad catalogue can be offered the
three-verb aggregator (`clown-mcp-collapse`) as its one tool, if its own
permission story is settled (today `--mcp-collapse` is refused with
`--agent-scope`, FDR 0020). The loaded set is known exactly, which makes the
unused-tools plan's `LoadedTools` seam trivial (§1.12).

### 1.5 Compaction

Claude Code compacts the conversation and writes `compact_boundary`. The
juggler loop has **no compaction**: it re-sends the whole transcript every step
(`jugglerloop.run.wireTurns`, `loop.go:226-229`) under `MaxSteps` (default 12,
`loop.go:15`) and moxy's blobbing of oversized results. That is adequate for
FDR 0019's 2–4-call agents and not for a worker that edits code for hours.

Disposition: **reproduce (Stage 2)**: a context budget measured from provider
usage (1.12), with a documented policy (truncate old tool results to
`{truncated, sha256, bytes}` markers, the shape `roomStanza` already uses,
before summarising). Until then a worker has a hard step and token cap, and
exceeding it is a `failed` run, not silent degradation.

### 1.6 Subagents

Claude Code's `Agent` tool runs in-process subagents with their own
transcripts; clown ships `subagents/discover.md` and the Explore rewrite.
juggler's equivalent is a separate process (`juggler spawn`, retiring under
clown#260) and the older single-turn `juggler-prompt` tool.

Disposition: **drop in-process subagents for v1**. A confined unit cannot
spawn sessions: `confinedPreflight` refuses a confined spawn from inside an
agent unit because the child's frontend would start inside the parent's unit
(`internal/spawn/confine.go:120`, clown#280). Until clown#280 defines a
frontend-side spawn path, a worker that needs help asks its driver by chat.
When #280 lands, the loop's spawn would be a tool call to the frontend
`spinclass serve`, the same way git goes to the frontend under FDR 0036 §9,
not an in-unit `systemd-run`.

### 1.7 MCP client behaviour

What clown hands Claude Code is a compiled manifest (`internal/pluginhost`):
HTTP URLs for servers clown launched, stdio servers wrapped by
`clown-stdio-bridge` (FDR 0002), a per-server `timeout`, and, in agent scope,
loopback ports reserved inside the unit and spliced to frontend unix sockets
(`clown agent-relay`, FDR 0020 "Forwards replace loopback URLs"). Claude
Code's own client provides the streamable-HTTP session, the server-initiated
announcement channel (RFC 0002), reconnection and its default per-tool
timeout.

**The 360 s SSE drops.** The operator has seen SSE streams dropped at 360 s in
confined units. That figure appears nowhere in clown's or moxy's source or
docs (searched `360`, `idle`, `KeepAlive` in clown and `moxy/internal/streamhttp`).
Treat it as an operator observation with cause **UNVERIFIED**; candidates are
the in-unit `agent-relay` splice, the stdio bridge's heartbeat mode, moxy's
`serve-http`, or the client's idle policy. §6 Stage 0 lists the measurement.

juggler's client (`internal/jugglertools/client.go`) is deliberately smaller:
exactly **one** upstream; POST-only JSON-RPC through `mcphttp.PostJSONRPC`
(which accepts both `application/json` and `text/event-stream` answers to a
POST, `internal/mcphttp/client.go:51,71`); `Mcp-Session-Id` captured and
echoed; 30 s per-request timeout (`client.go:49`); 1 MiB response cap; no GET
stream, no server-initiated requests, no reconnection, no re-initialise after a
rejected session id (read: `rpc` never retries `initialize`). Because it holds
no long-lived GET stream it is not exposed to idle-stream drops between calls,
but a tool call answered as SSE is one held connection for the call's
duration, bounded by the 30 s timeout, so a 360 s idle policy could not be hit
by this client today; a longer per-tool timeout would re-expose it.

Disposition: **reproduce what workers use, drop the rest**: per-tool timeouts
from the manifest; session re-initialise on an invalid-session error; connect
to more than one upstream (the in-unit moxy plus the forwarded frontend-class
ringmaster, troupe and `spinclass serve`, which are plain loopback HTTP inside
the unit); keep dropping server-initiated requests. Wakes (1.10) do not need
the announcement channel. If moxy becomes a library (moxy#487), the moxy
upstream disappears from this list and the remaining upstreams are the
frontend-class forwards only.

### 1.8 The system prompt clown composes

For Claude, clown builds `--system-prompt-file` (replace),
`--append-system-prompt-file` (fragments from `.clown/system-prompt.d`, the
repo's `system-prompt-append.d/`, and plugin servers that opt in with
`systemPrompt: true`), `--agents`, `--disallowed-tools` and `--settings`
(`internal/provider/claude.go`). In agent scope, dynamic fragments from
in-unit servers are **not** fetched (FDR 0020 "What a confined worker loses").
Claude Code adds its own base prompt, `CLAUDE.md`/`AGENTS.md`, skills and
memory on top. A juggler agent's system prompt is `brief.system` verbatim.

**Resolved (re-read 2026-10-25):** clown's `system-prompt-append.d/` holds
five fragments. `00-identity.md` names Claude Code only as what Clown is a
fork of, and directs the agent to the `man.*` MCP tools, which the in-unit
moxyfile allows (`agentscope.InUnitMoxyfile` lists `man`).
`01-runtime-claims.md` now also says to convert epochs with moxy's `env_date`
tool or `date -d @N`; `env` is in the in-unit allow-list, and a loop with no
shell is covered by the fragment's own fallback (report the epoch raw).
`02-pii-redaction.md` and `03-todo-issue-refs.md` are harness-neutral.
`04-no-file-memory.md` names a Claude Code feature, the file-based memory
under `~/.claude/projects/<project>/memory/` and `MEMORY.md`, but no tool: a
juggler agent has no such memory section to override, so the rule is inert
there, and its prohibition (do not read or write that directory) is still
right for an agent whose folio tools could reach it. No fragment names a
Claude-Code-only tool, so the conclusion stands: the composed prompt can be
reused as `brief.system` with no per-fragment exclusion. There is no
mechanism to exclude a fragment from the brief; if a future fragment should
not reach juggler agents, the composer needs one (a per-fragment
harness tag, say) and this section must say so. Plugin fragments that opt in
with `systemPrompt: true` still need a look when the loop is first pointed at
them.

Disposition: **reproduce selectively**. The composer (`internal/promptwalk`)
runs in clown's frontend and produces the brief's `system`. Loading the repo's
`AGENTS.md` is **required** (workers rely on it); skills and auto-memory are
**dropped** for v1. The brief should also carry a short harness-specific
preamble stating what the loop is (no Bash, no Skill tool, denials are final,
`cannot_complete` exists), since the model will otherwise reach for tools the
fragments assume.

### 1.9 Resume and session identity

`clown resume clown://claude/<id>` re-enters a session; clown binds the
harness session id to the per-instance key (FDR 0018); spinclass substitutes
`{principal}` in the resume entry (FDR 0035 "The principal across `sc resume`").

Disposition: **reproduce (Stage 3)**: a resumed loop replays the local turn
log into the transcript and keeps the principal. v1 has no resume: a confined
worker is restarted from its brief and its worktree.

### 1.10 The attach / terminal surface and job wakes

Claude Code is a TUI. In agent scope clown always runs the `ptysuspend`
relay, allocates a pty the unit owns, and `clown agent-exec` does `setsid` +
`TIOCSCTTY` before exec'ing claude (FDR 0020 "The terminal"). Wakes (job
completions, chat) arrive via the plugin monitors (`ringmaster monitor`,
`troupe receive`) whose stdout lines Claude Code turns into user input. Those
monitors still run **inside** the unit today (FDR 0020 "Not built yet:
frontend monitors"; no `frontend-monitors` capability exists in the Go tree).

Disposition: **drop the pty**; **reproduce the wake**. A loop needs no tty.
The unit record's `pty` may be empty (`ParseUnitRecord` accepts `""`,
spinclass `internal/confinement/record.go:95`; FDR 0035 "empty if none"). It
needs an **inbox**: between steps it reads wake lines and chat addressed to its
principal and injects them as user turns. The cheapest faithful shape is to
start the same two monitor processes the `clown-builtin-jobs` plugin starts
for Claude Code and read their stdout; when the monitors move to the frontend
the loop reads the relay instead, as Claude would. For observation, the
per-turn MUC post and the local turn log are the watch surface; `posh attach`
has nothing to attach to (Q11).

A semantic difference matters here. FDR 0019's agents are **task-shaped**:
`end_turn` ends the run and the evaluator decides success. spinclass workers
are **session-shaped**: after finishing a turn they idle until the driver
messages them and end only on close. The loop needs two modes (§2.3).

### 1.11 Built-in tools

Claude Code supplies Read/Write/Edit/Bash/Glob/Grep/WebFetch/WebSearch/Agent/
Skill/NotebookEdit and more. The confined in-unit moxy allows
`folio, grit, rg, jq, man, chix, env, batch, async, async-result, async-cancel,
status, restart` (`internal/agentscope/inunit.go:66`): files, git, search and
nix, but **no general shell**. FDR 0019 forbids `restart`, `batch`, `async*`
and `status` in a brief (§5), so a loop worker loses background execution.

Two fleet moves change this list for the loop and should be designed to:

- **git leaves the unit.** spinclass FDR 0036 §9: for a confined caller the
  `grit_*` tools run in the frontend `spinclass serve`, which writes the main
  `.git` and stamps authorship; the unit keeps a read-only `.git`. The loop's
  git tools are therefore the forwarded `spinclass serve`'s, not moxy's `grit`
  moxin; clown#274 narrows to dropping `grit` from the in-unit moxyfile once
  FDR 0036 step 8 lands.
- **file IO follows.** FDR 0036 §9.5 and spinclass#384 record the operator's
  longer view: `folio` moves into a spinclass plugin with path visibility and
  worktree enforcement done there, later with alternative backends. The loop
  must not bake moxy-specific assumptions into how it names, permits or
  blobs file tools: tool names come from `tools/list`, permissions come from
  the server's verdict plus the loop's rule table, and results are opaque
  content the loop passes through (URIs extracted by the existing top-level
  rule).

Disposition: **replace** file/search tools by moxy's now and by spinclass's
later; **replace** git by the frontend store tools; **open** for a shell (Q3):
`chix` develop-run, the `just-us` recipes server (recipes are the repo's
"paved paths"), or a bounded exec tool whose argv allowlist is the policy.
Note that `just-us run_recipe` is a shell by another name; under enforce mode
it needs its own tier.

### 1.12 Metrics (clown#263 and the unused-tools plan)

Today statsd lines come from parsing the transcript after the fact (1.1).
The loop currently discards what those metrics need: neither codec returns
provider `usage` (`ModelReply` carries only `Turns` and `Stop`,
`jugglerloop/codec.go:48-51`; `anthropic.go` decodes no `usage` field), there
is no `cache_control` and no streaming.

Disposition: **reproduce natively, same names**: the loop emits
`clown.tool_result.*`, `clown.tokens.*`, `clown.responses.*`, `clown.context.*`,
`clown.compaction.*` and the plan's `clown.tools.{loaded,called,unused,…}` from
its own state, exactly (no `message.id` grouping, no late-flush offsets). In a
confined unit they leave through the statsd relay (FDR 0020 "statsd relay",
capability `statsd-relay`). Prerequisite (Stage 1): capture `usage` in both
codecs. Add a harness segment or a parallel family so the two paths can be
compared without breaking dashboards.

### 1.13 Things the Claude Code path provides that are easy to miss

- **Retry/back-off** on 429/5xx/overloaded: `jugglerloop.RoundTrip` makes one
  attempt and any non-2xx ends the run `model_error` (`codec.go:79-105`).
  **Reproduce.**
- **Streaming** and long outputs: the loop is non-streaming with
  `DefaultMaxTokens = 4096`. **Reproduce** when code-writing workloads need it;
  whether the egress proxy tolerates long non-streaming responses is
  **UNVERIFIED** (the proxy is a `CONNECT` tunnel, so this is a question about
  its idle handling, not about parsing).
- **Prompt caching**: none in the loop. **Reproduce** (cost, Q1).
- **Extended thinking**: not in the codecs. **Drop** for v1.
- **Credentials**: Claude Code uses the operator's login credentials, bound
  into the unit (`.credentials.json`, FDR 0020 socket table). A juggler loop
  needs a provider API key from the model registry (`JUGGLER_MODELS_PATH`,
  FDR 0019 §10). Whether Claude login credentials may be used by a custom loop
  is **UNVERIFIED** and presumed not (Q1).
- **Pre-merge attestation**: `merge-this-session` requires a
  `nothing-but-the-truth` attestation naming skills. The attestation is an MCP
  tool on the forwarded `spinclass serve` and is callable by the loop; the
  Skill tool is not available, so `used:false` with reasoning is the only
  truthful answer. Whether that is acceptable is Q7.

## 2. What juggler already is

Read from FDR 0019 and the code, not inferred.

### 2.1 Existing pieces

| Piece | What it is | Source |
|---|---|---|
| `juggler run` | headless process: reads a brief, drives the tool-use loop against one moxy upstream, records ledger and `traffic.jsonl`, writes the ringmaster terminal | FDR 0019 §1; `internal/jugglerrun/agent.go` `RunAgent`; `cmd/juggler/run.go:197-255` |
| the loop | `jugglerloop.Run`: step cap (12), wall clock (2 m), `cannot_complete`, `StopWhen` evaluator hook, stanza-shaped `Turn`s, two codecs (Anthropic Messages, OpenAI-compatible) | `internal/jugglerloop/loop.go`, `codec.go` |
| the brief | `schema, principal, parent, room, model, system, task, moxyfile, env, tools, evaluator, limits`; **unknown fields are rejected at parse** (`jugglerbrief/brief.go:228`) | FDR 0019 §3 |
| tool plane | juggler launches `moxy serve-http --name-template '{server}_{tool}'` from the brief's inline moxyfile, with `MOXY_PARENT_MOXYFILE` pinning the spawner's effective moxyfile; `allow-tools` is **derived from `tools`** | FDR 0019 §5; `agent.go:287-321` |
| call-time allowlist | the loop refuses any tool name not offered (clown#261) | `loop.go:315-328` |
| arguments audit | `traffic.jsonl`, per call, full arguments, keyed by principal and job (clown#255) | FDR 0019 §4 |
| lifecycle | ringmaster job, advisory lock, `ExecStopPost=juggler exit-wake`, `systemd-run --user` transient unit | FDR 0019 §6, §9 |
| identity | per-run troupe mint, a MUC per run, the run root | FDR 0019 §1, §2, §7 |
| HTTP client | `http.DefaultClient` for `run` and `decide` (`cmd/juggler/main.go:35,84`) | |
| decisions | `juggler decide` router; not a loop | FDR 0019 §1 |

`--provider juggler` in `clown` is a **stub that exits 1**
(`cmd/clown/main.go:2247-2257`, pointing at FDR 0011). `--agent-scope` is
refused for any provider but claude (`main.go:804-806`).

### 2.2 Gaps against §1

| Need (from §1) | juggler today | Gap |
|---|---|---|
| durable turn log, resume | ledger at end, traffic per call | append-only turn log; replay |
| session shape (park on inbox) | task-shaped; `end_turn` ends the run | session mode |
| wakes/chat inbox | none; one-shot | inbox between steps |
| compaction, context budget | none; full history every step | budget + policy |
| provider usage, cache control, streaming, retry | none | codecs + `RoundTrip` |
| native metrics | none | emitter behind the `LoadedTools` seam; statsd |
| multiple MCP upstreams, session re-init, per-tool timeouts | one upstream, 30 s flat, no re-init | client work |
| system prompt composition | `brief.system` only | reuse `promptwalk` output |
| permission policy with argument-sensitive rules, enforced where tools run | exact tool names only; moxy enforces nothing beyond `allow-tools` | §3 |
| hello for the spawn driver | none | §4.5 |
| confinement | FDR 0019: namespaces deferred; clown#252 open | reuse FDR 0020's unit (§4) |
| git identity | none, and none needed under FDR 0036 §9 | §5 |
| a shell | none | Q3 |
| brief fields for mode and policy | parser rejects unknown fields | schema 2, or a parser that tolerates additive fields under schema 1 (§Interface) |

### 2.3 The two loop modes

- **task mode** (FDR 0019 as built): `end_turn` ends the run; the brief's jq
  evaluator measures success; one ringmaster job per run.
- **session mode** (new): `end_turn` parks the loop on its inbox; the run ends
  on `close`/cancel, a wall-clock or budget limit, or `cannot_complete`. The
  evaluator is optional (a spinclass worker's "success" is a merged commit,
  which spinclass already knows).

Both share the loop, the policy, the ledger and the turn log.

### 2.4 Related records and where they stand

- **clown#261 (closed):** `tools` is enforced at call time and by the derived
  `allow-tools`. This is the mechanism §3 builds on. It does **not** cover
  arguments, and `allow-tools` is enforced by moxy's funnel while the
  permission tier is not (moxy#485).
- **clown#255 (closed):** `traffic.jsonl`. Becomes part of the turn log's
  neighbourhood; keep it.
- **clown#252 (open):** juggler agents run unconfined as the launcher's user.
  Its last comment redirects the hardening to ringmaster FDR-0001 step 2's
  launch document. The confined-worker path here gets confinement from FDR
  0020's unit instead; that answers #252 for this path only.
- **clown#260 (open):** under ringmaster FDR-0001 juggler shrinks to
  `juggler run`; `spawn`, `exit-wake`, the in-process job lock and the
  `ExecStopPost` hook retire at service step 2; env construction becomes the
  launch document (FDR-0001 §9). Consequence: nothing here may depend on
  `juggler spawn` or the `ExecStopPost` hook. A confined worker launched
  through clown does not (§4.3). At step 4 (FDR-0001 §11, §17) sessions
  themselves become spawned jobs with request identity read from the unit
  (§15); the loop should be a plain ringmaster producer so that relocation is
  mechanical.
- **clown#258 (open, deferred):** extract juggler into its own repo; "stays
  unentangled from clown internals in the meantime". This design adds reasons
  to reach into clown (`agentscope`, `promptwalk`, `pluginhost`, `staging`);
  §6 keeps those on clown's side of a narrow seam: the brief is the seam.
- **clown#263 (open):** native metrics; §1.12.
- **clown#274 (open):** staged git identity and the read-only main `.git`.
  FDR 0036 §9 answers the read-write half (git through the frontend) and
  narrows #274 to dropping `grit` from the in-unit moxyfile.
- **moxy#484 (closed, 18ea283):** dontAsk hook decisions. **moxy#485 (open):**
  no server-side enforcement. **moxy#486 (open):** enforce mode proposal.
  **moxy#487 (open):** moxy as an embeddable Go library, naming this FDR as
  the first consumer. All four shape §3.

## 3. Permission policy: refuse, never ask

### 3.1 Rule

The loop's policy function has two outcomes, `allow` and `deny`. There is no
`ask`, no pending state and no code path that waits for an answer. Any input
that would be an `ask` elsewhere (a moxin's `each-use`, a `dynamic` verdict of
`ask`, a tool the policy does not mention, an argument outside a rule's scope,
a resolver that errors or times out) is a `deny`. The default is deny.

Property to test (hermetic, with a fake model): a model that emits every class
of unlisted or denied call terminates the run in bounded time with every call
answered by a tool_result error and a ledger entry. This is the structural
fix for the hang class that dontAsk and moxy#484 patch one door at a time.

### 3.2 What moxy actually enforces today (moxy#485, read this pass)

`Proxy.callToolV1` (`moxy/internal/proxy/proxy.go:1024-1072`) checks, in
order: the `--expose` category filter, the dynamic exclude set, and the
moxyfile's `allow-tools`. It then dispatches. It never calls
`permcheck.Resolver.Resolve`. The resolver (`internal/permcheck/permcheck.go:55-75`,
returning `Allow`/`Ask`/`Deny`/`Unknown`) is consumed only by the Claude Code
hook adapter and by the `batch`/`async` meta tools (`proxy.go:261-267`).
moxy's skipped bats `zz-tests_bats/server_side_perms.bats` (bb446c3) encodes
the wanted behaviour and fails today: an `each-use` tool and a `dynamic` tool
whose predicate exits 2 both run on a plain `tools/call`.

Consequences:

- FDR 0019 §5's sentence "any moxy permission tier other than `always-allow`
  resolves to deny for a juggler agent" and its Tuning Levers row "headless
  permission posture" describe an intent, not a behaviour. The
  `jugglertools` package doc (`client.go:18-22`) assumes that intent. Both
  should be corrected to cite moxy#485 (a follow-up edit to FDR 0019, not
  made here, since this record is design only).
- Every juggler agent that lists a non-`always-allow` tool in `tools` has
  been running it ungated, bounded only by the brief's `tools` list and the
  unit it runs in. For the Pebble agents this is moot (their tools are
  always-allow wrappers); for a worker with `folio_*` it is not: folio's
  `dynamic` path classifier is what refused a write to `$HOME` in FDR 0020's
  in-unit evidence, and that refusal came through the **hook**, which the loop
  does not have.

### 3.3 Where enforcement lives

Four layers, from the one that is a boundary to the one that is advice:

1. **The unit** (FDR 0020): mount, pid, network and devpts namespaces. A
   policy bug anywhere below cannot reach `$HOME`, the operator's ptys, the
   session bus or another session's MCP. This is the only layer that holds
   against a hostile process; everything else assumes the model's own process
   is honest about which code it runs. The loop, moxy and the moxins all run
   **inside** this layer, as the operator's uid.
2. **The tool server's own verdict, enforced in the server**. Today: moxy's
   `allow-tools` funnel, derived from `tools` (clown#261). Needed:
   **moxy#486's `enforce` mode** on the loop's moxy, so that `each-use` is
   denied, `dynamic` runs its predicate and maps `ask`→deny, and `unknown`
   (no `perms-request`, `delegate-to-client`) is denied. Later, the same
   verdict from the **frontend** for tools that move there: `spinclass serve`
   already classifies `grit_*` by ownership and, for a confined caller, refuses
   in the tool body what `Owned` rejects (FDR 0036 §9.4); a folio-in-spinclass
   would do the same for paths. Those frontend verdicts are outside the unit
   and are the authoritative ones for the tools they cover.
3. **The loop's policy**, before `Exec.Call`: the offered set (exists), plus a
   rule table from the brief (3.4). It exists for three reasons the server
   verdict does not give: a denial message that names the rule and the ways
   forward, a denial budget, and coverage of tools that have no server-side
   verdict at all (the frontend-class MCP servers, `cannot_complete`). It is
   **not** a boundary: it runs in the model's process, in the unit.
4. **Offer-time**: only the brief's `tools` are sent to the model (exists).

Where the loop differs from the first draft: the first draft recommended
"the funnel mode as the authority, with the loop pre-check only for a clearer
error message" without knowing moxy did not resolve at all, and otherwise
treated the loop's policy as the main fix. This pass says the loop's policy
cannot be the main fix, because it is inside the trust domain it polices, and
makes moxy#486 (or its library successor) a **gate** rather than a nice-to-have.

**Enforce mode: what the loop needs from it** (input to moxy's session, not
decided here):

- `enforce` is set by the spawner on the agent's moxy and is **locked under
  narrowing** so the brief's own moxyfile cannot flip it back to `delegate`
  (moxy#486 already proposes this).
- Mapping: `always-allow` → run; `each-use` → deny; `dynamic` → predicate,
  with `ask`, a crash, a timeout or a missing `[dynamic-perms]` → deny;
  no `perms-request` / `delegate-to-client` → **deny**. The cost moxy#486
  names ("tools with no declared permission are refused under enforce") is
  the right default for a headless client: an unannotated tool has never been
  reviewed for a world with no human. A spawner that wants one anyway grants
  it explicitly in the brief's moxyfile (a per-tool override key is moxy's to
  design), and that grant is then part of the signed brief.
- The denial must be an `isError` tool result with a reason the agent can act
  on, in the same shape as the dontAsk hook's reason (18ea283,
  `dontAskDenyReason`), not a JSON-RPC error: `jugglertools.Call` already
  treats `isError` as the model's problem and a transport error as the run's.
- `batch`/`async` stay out of a loop brief (FDR 0019 §5), so the "consent must
  travel with an async job" cost of moxy#486 does not arise here.

**Embed (moxy#487) or call over MCP?** Recommendation: **MCP with enforce mode
for Stage 1; embed when the library exists, as the end state.** Reasons:

- The library does not exist; enforce mode is a smaller change to one
  function (`callToolV1`) and ships the gate sooner.
- MCP keeps juggler unentangled (clown#258): the loop depends on a wire
  protocol, not on moxy's internal packages, and the same loop binary serves
  a non-moxy tool plane (spinclass's plugin) without a rebuild.
- What embedding buys is real and should be taken later: no loopback endpoint
  at all (the out-of-band `127.0.0.1:/mcp` path of FDR 0019 §5 disappears),
  in-process verdicts with a caller identity and a policy object passed by the
  loop, no SSE framing, no 30 s client timeout, no session id, and one place
  for blob references. It also changes the process picture: the loop becomes
  moxy's host and supervises the moxins' child processes, which under
  FDR 0019 §9's runtime/agent scope split moves the enforcer *into* the agent
  scope. That is acceptable only because the unit, not moxy, is the boundary
  (§3.3 layer 1); it must be said plainly when #487 is designed.
- Either way the loop's rule table (3.4) is the same code; only the executor
  behind `ToolExecutor` changes.

### 3.4 Expressing the allow list

Tool names are the brief's `tools` (exact, `<server>_<tool>`). Argument
constraints are new. Two sources, not mutually exclusive:

- **(a) The server's verdict** (3.3 layer 2): moxy's `dynamic` predicates for
  moxin tools; spinclass's `Classify` for store tools. The loop treats any
  `isError` result as a denial for budgeting purposes when its content carries
  the server's permission marker (a fixed prefix or a structured field; moxy's
  to define with #486), and otherwise as an ordinary tool error.
- **(b) A loop-owned rule table** in the brief (new field, §Interface) for
  what no server verdict covers or for tightening it: path scopes for
  `folio_*` (today's hook-side rule "reads outside the worktree ask" becomes
  "deny"), repo/branch scopes for the store tools beyond ownership, argv
  allowlists for an exec tool, and the `clown-hook-allow` rules (`/nix/store`
  reads). Evaluated in the loop; every miss is deny. spinclass's tier files
  (Claude rule grammar) are translated by spinclass or replaced by a second
  format; clown#276 (a profile-level permission scheme that spinclass composes
  with the sweatfile, narrowing only) is where that format belongs, so the
  loop reads one scheme rather than two.

The brief's `tools` list stays the authoritative **shape** of the surface:
spinclass#383 (adding individual tools back to a profile's selection, granted
only when they are covered by the always-allow set) composes with it directly,
because `tools` is a per-tool list already. A spawner adds the named tools to
`tools`; the always-allow condition is checked by spinclass against the moxin
tiers before the brief is written.

### 3.5 Denials are information, not a stall

A denial returns a tool_result error with a fixed shape the model can act on:
which rule or tier denied, that this session cannot ask, and the two ways
forward (use an allowed tool, or call `cannot_complete`). The ledger records
`{tool, kind:"denied", ok:false, rule}`. To stop a model from spinning on
denials (the loop's analogue of auto mode's "repeated blocks" fall-back, but
ending instead of asking), a **denial budget** (default 5 per run,
brief-configurable) ends the run `failed` with reason `denial_budget`.

### 3.6 Escalation without waiting

"Refuse, never ask" does not forbid *telling* someone. A worker that is
blocked may message its driver (chat) and, in session mode, park on its inbox;
the driver decides whether to re-spawn with a wider `tools` list. The loop
never holds a pending call open: the denied call is already answered. A
dedicated non-blocking `request_permission` tool that records the request and
returns at once is Q6. Note the direction in spinclass#383/#384: escalation
is being designed as card-rooted grants, so a recorded request is the right
artefact for that mechanism to consume later.

### 3.7 Under circus#353

circus#353 explores moving principals and enforcement into system or kernel
space (a system service with its own uid, a system-manager launcher, LSM or
Landlock policy bound to the principal). For this design: layer 1 would get
stronger (a real uid, kernel-enforced path policy) and the authoritative
tool-plane verdicts would move further out of the unit (a frontend-scope or
system-scope policy point, as FDR-0001 §15 already reads request identity
from the unit). The loop's own policy (layer 3) stays exactly what it is now,
defence in depth with better errors. Nothing in §3 assumes the loop is
trusted, so #353 changes where the gate is, not what the loop does. The one
thing to keep from foreclosing: the loop must identify itself to tool servers
by its **unit**, never by a name it asserts (§4.4), so that a system-level
policy point can bind on it.

## 4. How a confined worker fits

### 4.1 Shape

clown stays the launcher and frontend; spinclass stays the session manager.
The only change in the unit is the process: `juggler run` (session mode)
instead of `claude`. clown synthesises a **brief** from what it already has
(the spawn prompt as `task`, the composed system prompt, the resolved
profile's model and credential reference, the per-worktree room as `room`,
the driver as `parent`, its own session key as `principal`).

```
spinclass spawn-session  ──(profile)──>  clown --agent-scope <spec> [harness=juggler-run]
   frontend: clown, troupe owner, egress proxy, statsd relay, frontend MCP sockets
             (ringmaster, troupe, spinclass serve incl. grit_* for confined callers)
   unit:     clown agent-exec ──> juggler run (session mode)
                 ├─ moxy serve-http (launched by juggler; enforce mode; parent-pinned)
                 ├─ forwarded ringmaster/troupe/spinclass MCP (loopback, frontend-class)
                 ├─ wake monitors (in-unit today; relays once frontend monitors land)
                 └─ model API over HTTPS_PROXY (egress allowlist)
```

### 4.2 Which FDR 0020 / 0035 assumptions are Claude-Code-specific

| Assumption | Where | With the loop |
|---|---|---|
| provider must be claude | `cmd/clown/main.go:804`; spinclass `SpliceDontAskMode` refuses non-claude (`internal/spawn/dontask.go:29-30`) and the splice is unconditional for a confined spawn (`spawn.go:136`) | both must learn the harness. For the loop the splice is skipped: there is nothing to splice and nothing that can ask. spinclass resolves the provider from `--provider`/`--profile` before `--` (`internal/spawn/model.go:44-71`), so a profile naming the harness is enough for spinclass to branch on |
| a pty, `ptysuspend`, `TIOCSCTTY` shim | FDR 0020 "The terminal" | not needed; `pty` empty in the unit record. F3 then requires that **no** tty fd exists in the unit, which holds only if clown hands the unit `/dev/null` or pipes, never its own stdio through `systemd-run --pipe` (a spawn driven from a posh session would otherwise leak the operator's tty into the unit). F8b holds trivially |
| `CLAUDE_CONFIG_DIR`, credentials file bind, transcript-dir bind | FDR 0020 socket table | not needed; replaced by a model credential reference (Q1) and the turn log |
| `ANTHROPIC_*`, `CLAUDE_CODE_*` env pass prefixes | FDR 0020 Environment | model/credential env comes from the registry entry instead; the prefixes are harmless but unused |
| Claude Code hooks run in the agent scope | FDR 0020 scope table | no hooks; in-process equivalents (§1.2) |
| **agents are found by `comm`** | spinclass `DefaultClaudeComms = {"claude", ".claude-wrapped"}` (`internal/confinement/confinement.go:106`), used by F11 (`probes.go:943-973`) and by the ancestry walk (`probes.go:350-358`) | a `juggler` process is **not** an agent runtime to spinclass: a loop worker is invisible to F11 whether confined or not. §4.4 |
| claude's `--session-id` == principal | FDR 0018, `resume_hint.go` | the loop takes the principal from the brief; FDR 0019 already requires `env == brief.principal`; the unit record's `principal` is still written by the frontend clown, so F1/F15 are unchanged |
| in-unit moxy via `clown agent-relay --serve` | FDR 0020 "In-unit servers" | not Claude-specific; but the loop launches its own moxy per brief (4.3) and the relay'd one is not started for this harness |
| the spec's `env{}`, binds, sockets, `egress_allow`, the capability query (`RequiredCapabilities` = `agent-unit`, `private-network`, `frontend-mcp-sockets`, `in-unit-moxy`; spinclass `internal/confinement/mapping.go:178`) | FDR 0020, 0035 | unchanged. `in-unit-moxy` is satisfied by the brief-launched moxy; the loop adds `headless-loop` |
| frontend-class MCP forwards; statsd relay; egress proxy; `PrivateNetwork`; `ProtectHome=tmpfs`; the spec/result files | FDR 0020, 0035 | **not Claude-specific**; unchanged |
| unit record's `principal`, F1, F15 | FDR 0035 | unchanged; clown still writes the record |

### 4.3 Tool plane and supervision inside the unit

Two ways to reach moxy: (i) the relay'd moxy clown already starts for Claude
Code, via `--moxy-url`; (ii) the loop launches its own moxy from the brief's
moxyfile. (i) forfeits the derived `allow-tools` (FDR 0019 §5: the
`--moxy-url` path derives none, `agent.go:287-321` read) so the broad
`InUnitMoxyfile` list would be the only moxy-side limit. **Recommendation
(ii)**, with `MOXY_PARENT_MOXYFILE` set to the `InUnitMoxyfile`-based reference
so the brief's tools must narrow the unit's own ceiling, and `enforce` set and
locked on it (§3.3).

Supervision: FDR 0019's `juggler spawn` and `ExecStopPost` are not used. The
unit is clown's (`systemd-run --user --wait --collect`), clown's frontend
waits for the exit code and can emit the exit wake itself, and the ringmaster
job is created by the worker's own identity. This is the shape that survives
clown#260 (service step 2 retires `ExecStopPost`) and that FDR-0001 §11
relocates at step 4 without reshaping. The loop must still write the terminal
record on every ending it controls; the host-wide reaper covers the rest.

### 4.4 The agent census: identify agents by unit, not by name

spinclass's F11 ("every live agent on the host is confined") enumerates
`/proc` and matches `comm` against `DefaultClaudeComms`; its own Limitations
already call the name match a stopgap and name the end state: "F11 enumerates
the units under `clown-agent.slice` plus every process in the login session's
scope that is not the frontend, and name matching goes away" (FDR 0035
Limitations). The ancestry walk that refuses an agent descending from another
agent runtime uses the same list.

What must change, in spinclass's lane (Q7), stated so the loop does not ship
blind:

1. **Stage 1 stopgap:** add the loop's `comm` to the list (`juggler`, and the
   wrapped name nix gives it, to be read from a real unit; both 15-byte
   truncations). Cheap, and it keeps F11 honest the day a loop worker exists.
2. **The real fix:** invert the census. A session is confined iff its unit
   record exists, its cgroup is under `clown-agent.slice` and
   `cgroup.procs` is non-empty (F1 already does this per session). F11 then
   becomes: every live session has such a record, **and** no process outside
   `clown-agent.slice` in the operator's login scope is an agent, where "is an
   agent" is decided by what the frontend launched (clown knows which pid it
   exec'd and can report it in the record) rather than by `comm`. The
   residual, a process that was never launched by clown at all, is the
   hostile-process case FDR 0035 already accepts as out of scope for F11.
3. **Self-identification to tool servers** follows the same rule: the loop
   never names its harness to moxy or spinclass; the frontend's unit record
   and `SO_PEERCRED` + cgroup (FDR 0035 F12, FDR-0001 §15) are the identity.
   moxy#486's rejected alternative (switching on the client name in
   `initialize`) is rejected here for the same reason.

### 4.5 Hello

The driver waits on a file in a directory the unit can write
(`SPINCLASS_HANDSHAKE_DIR`), keyed by `(worker session key, driver principal)`,
for 60 s. Today the SessionStart hook writes it **and** records the worker's
own principal and adopts the PID into session state in the same call
(`hooks.go:111-112`). Three ways for a non-hook harness:

- **(a) The loop invokes spinclass's hook handler** at the point its tools are
  connected and its first model call is about to go out, with a synthetic
  `SessionStart` payload (`cwd`, `session_id`). This reuses the exact code
  path, including the principal record and PID adoption, and keeps the contract
  in spinclass's hands. Cost: the loop knows a Claude-Code-shaped payload and
  the handler's path (`${CLAUDE_PLUGIN_ROOT}/hooks/handler`, which clown would
  have to supply as an absolute path in the brief's launcher-owned env).
- **(b) clown's frontend writes the hello** when the unit is active and the
  loop reports ready over the frontend MCP socket or a readiness file. The
  frontend knows both principals and the directory. Cost: the hello then
  means "unit up and loop connected", which is earlier than "tools connected
  and model reachable", and the principal record/PID adoption still need a
  path.
- **(c) A `spinclass hello` CLI verb** (or an MCP tool on the forwarded
  `spinclass serve`) that does what the hook does, callable by any harness.
  Cleanest contract; one more spinclass change.

Recommendation: (c), with (a) as the Stage 1 bridge if spinclass prefers not
to add a verb yet. Whichever lands, the contract is `SendHello`'s record and
is spinclass's to restate for a non-hook harness (Q9); `posh_session_id` is
empty for the loop and the driver's auto-attach is skipped, which
`WaitForHelloIn` already tolerates.

### 4.6 What spinclass must accept for this to run (summary of Q7)

1. A confined spawn whose harness is the loop: skip `SpliceDontAskMode` and
   `SpliceAutoModeFlag` for it (both already branch on the resolved
   provider), write the same spec, require the same capabilities plus
   `headless-loop`.
2. The census change of §4.4 (stopgap first, inversion second).
3. The hello path of §4.5.

And, not blocking but adjacent: a loop worker calling `grit_*` on the
forwarded `spinclass serve` is a confined caller under FDR 0036 §9.4, so the
tool body's ownership check applies; the body learns "confined" from the unit
record for the principal, which exists for every loop worker launched this
way.

### 4.7 Egress, credentials and local models

**Resolved (read this pass):** `juggler run` uses `http.DefaultClient`
(`cmd/juggler/main.go:35`), whose transport is Go's `DefaultTransport`, which
reads `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` from the environment (stdlib
behaviour, not re-read here). So the loop's model calls honour the unit's
`HTTPS_PROXY` without a code change; this is unmeasured inside a unit and is
on the Stage 1 list. The model host must be on the sweatfile's `egress_allow`
(today's default names `api.anthropic.com` and the forges; an OpenRouter entry
needs its host added).

A model API key inside the unit is readable by the agent (it is in the
environment or a bound file). The registry's credential reference scheme
(FDR 0019 §10) should point at a file bound read-only and scoped to one key
with a spend limit (FDR 0019 Tuning Levers; OpenRouter per-key limits). A
**local** llama-server is unreachable (FDR 0020: loopback is the unit's own),
so confined loop workers use remote registry entries until a relay exists.

## 5. Identity, commit authorship and signing

Context: the first confined worker invented a `.gitconfig` to commit
(clown#274). Since then spinclass FDR 0036 §9 decided that a confined worker's
git runs in the **frontend** `spinclass serve`, which **stamps author and
committer** from one spinclass-side setting read at commit time (proposed
home: sweatfile `[store]`); an unset value falls back to git's own resolution
and fails the commit visibly. circus#351 ("whether agent sessions should
commit under their own identities") is open and deliberately undecided;
`commit.gpgsign` is unset on flic.

### 5.1 What the loop changes

1. **The unit needs no git identity.** With `grit_*` served from the frontend,
   `GIT_*` in the unit is irrelevant to attribution; `GIT_*` is already a
   launcher-owned key group a brief may not set (FDR 0019 §3). The first
   draft's option "set `GIT_AUTHOR_*` in moxy's tool-server environment" is
   withdrawn: it would only matter for the in-unit `grit` moxin, which
   clown#274 is dropping.
2. **Every commit is a recorded tool call twice over**: in the loop's ledger
   and `traffic.jsonl` with full arguments, and in the frontend's own tool log,
   attributable to a principal and a turn. Provenance does not depend on the
   git author field.
3. **The agent has a natural identity to attribute to** (per-instance key,
   clown name, troupe JID), so whichever option circus#351 picks is cheap to
   implement in the stamping setting. FDR 0036 §9.2 asks for a single value;
   §5.2's options are candidate values for it.
4. **Signing stops being ambient, and moves out of the unit with git.** Under
   FDR 0036 §9 a signature, if any, is made by the frontend's git, whose
   signer is the operator's piggy-agent and prompts the operator. A headless
   worker's commit must therefore not request a signature unless the signer is
   one that does not need an operator at the keyboard (the agent-class signer
   of spinclass FDR 0032 D10 / piggy FDR 0006). "Refuse, never ask" extends to
   signing: the stamping path must fail the commit, not prompt, when signing
   is on and no non-interactive signer is configured.
5. **Hooks on proxied commits** (FDR 0036 §9.3, undecided): if the frontend
   runs the per-commit hook for a confined worker it executes worker-authored
   code outside the unit. For the loop the conservative default (skip hooks on
   proxied commits, rely on the merge gate) is the right one; the loop should
   not depend on authoring-time repair.

### 5.2 Options for the stamped value (not decided; circus#351)

| Option | Author / committer | Signed | Notes |
|---|---|---|---|
| A. Operator identity (the minimal #274 answer) | operator / operator | no | simplest; attributes agent work to the human; provenance only in troupe stanzas, the ledger and the frontend tool log |
| B. Agent identity | agent / agent | no | `name` from the clown name or session key; `email` form is the operator's to choose (a noreply form on a fleet domain, or the troupe JID); the operator named by a `Co-authored-by` trailer or not at all |
| C. Split | agent / operator | no | git models this natively; whether `merge-this-session`'s rebase in the frontend rewrites the committer is **UNVERIFIED** and now moot for attribution, since the frontend stamps both fields explicitly |
| D. Agent identity, signed | agent / agent | yes, by an agent-class key | needs the non-interactive signer above; not available today |

Independent of the option: how a worker's identity relates to **who pushes**
(the frontend's `merge-this-session`, with operator credentials, or FDR 0028's
per-session forge credential) is spinclass's design.

## 6. Staged path

### Stage 0 — settle the gates (no code)

- **moxy:** decide moxy#486 in shape (the mapping in §3.3, locked under
  narrowing, `isError` denials). **Gate for any tool not `always-allow`.**
  Note the ordering with moxy#487: enforce mode first, library later.
- **spinclass:** the three items of §4.6. Plus the FDR 0036 step 8 timing
  (confined callers through the frontend `serve`), since Stage 2's commits
  depend on it.
- **operator:** Q1 (credentials/billing), Q2 (session mode in v1), Q5 (how a
  spawn selects the harness), the identity value (§5, circus#351).
- **Measure the baseline on the Claude path** so Stage 1 has something to
  beat: confined workers' hang count and time-to-hello (spinclass lifecycle
  log and hook trace), which builtin and moxy tools workers actually call
  (`clown.tool_result.*`; the unused-tools plan once built), tokens and cost
  per session (`clown.tokens.*`). This tells us whether Bash/Edit, which the
  loop lacks, dominate worker traffic.
- **Measure the 360 s drop**: a long-running always-allow tool inside a
  confined unit, called from Claude Code, with timestamps on the stdio bridge,
  the relay and moxy, to attribute the drop before the loop inherits any of
  those components.

### Stage 1 — task-mode confined worker, read-mostly

- Capture provider `usage` in both codecs; emit the toolcost families
  natively, names unchanged (clown#263 slice), through the statsd relay.
- Durable local turn log.
- Policy layer with deny-only semantics, the rule table, the denial budget,
  and the hermetic "no waiting path" test (3.1) against a fake model.
- New harness in clown's matrix (selection per Q5), brief synthesis from the
  spawn prompt and the **resolved named profile** (not the forwarded
  `--model`, see "How a spawn chooses"), `--agent-scope` accepts it,
  capability `headless-loop`, clown writes the unit record with `pty: ""` and
  hands the unit no tty.
- Hello per §4.5 (bridge or verb).
- Briefs in this stage list only tools whose moxin tier is `always-allow`
  (`man`, `rg`, `jq`, `env`, folio's reads inside the worktree if folio
  classifies them so; to be read from the moxin definitions) **until
  moxy#486 lands**; no commits.
- Measured: the confinement check passes against a loop worker (F1–F15 as
  `sc confinement-check` reports them, with the §4.4 stopgap in); the hang
  class is gone by the test, not by absence of observation; metrics parity
  vs the Claude path on a replayed brief (same families present, per-response
  token totals within the provider's own accounting); time-to-hello; steps,
  denial counts, proxy-refusal count in the frontend log.

### Stage 2 — session mode and the write path

- Inbox (wakes and chat), `end_turn` parks, `close` ends.
- moxy in enforce mode; `dynamic` tools (folio writes) offered.
- Retry/back-off, prompt caching, streaming, context budget and compaction.
- A shell decision (Q3); commits through the frontend `grit_*` once FDR 0036
  step 8 lands and the stamping value is chosen (§5); `AGENTS.md` loading in
  the composed prompt.
- Measured: merged-change rate and cost per merged change vs the Claude path
  on comparable briefs; context growth per step; retry counts; denials per
  merged change (a proxy for how often the allow list is too tight).

### Stage 3 — parity and default

- Resume from the turn log; the attestation path; a watch/steer surface;
  moxy as a library if moxy#487 has landed.
- Only then consider making the loop the default for confined spawns. The
  Claude Code path remains for interactive, unconfined and human-attached
  sessions regardless.

### How a spawn chooses

spinclass's `spawn-session` accepts a clown **profile** and places
`--profile <name>` among clown's flags, refusing unknown or skewed registries
(spinclass FDR 0034; `pkg/profilelookup`). Two ways to make the loop
selectable:

- **Harness value (recommended).** The profile's `provider` field is the
  harness axis in FDR 0006's mental model (its valid values today are
  `claude`, `opencode`, `crush`, `openrouter`; `internal/profile/profile.go:150-155`).
  Add a value (e.g. `juggler-run`; the bare name `juggler` is taken by the
  stub) with its `validCombos` backends (`anthropic`, `gateway`). No new
  profile field. **Resolved (read this pass):** `pkg/profilelookup`'s
  canonical form includes `provider` as a string and `ProtocolVersion` versions
  "the profiles.toml schema, the registry stamp and the canonical form"; a new
  *value* changes none of the three, so no bump is needed. The skew that does
  arise is benign and fail-closed: a spinclass linked against an older clown
  rejects the profile at `profile.Validate` (`TestResolveRejectsInvalidDefinition`
  pins this) and refuses to spawn. Announce it to spinclass anyway.
- **A new `loop` profile field.** Cleaner conceptually (harness, provider,
  model, API stay orthogonal) but adds a canonical-form field, which per
  `AGENTS.md` means a `ProtocolVersion` bump announced to consumers first.

In both, the profile fixes the model (a registry name) and the credential
reference, and the harness-specific permission scheme (clown#276) rides the
same profile.

**clown#281 is a cautionary tale for this exact path.** A named profile's
model was overridden by the clownfile's default model because
`applyClownfileProfile` runs before `applyNamedProfile` and the latter yields
to an existing `--model` (`cmd/clown/main.go:400,510`). The brief synthesis
must take `model` from the **resolved profile** (`profilelookup.Resolved.Model`),
never from the forwarded provider args, and the loop must record the model it
actually called in the ledger and the unit record, so a mis-selection is
visible in `sc list` and not discovered 118 turns later.

**clown#277 (profile-management MCP tool):** it must never appear in a loop
brief's `tools`; a profile is spawner input, and a worker that could edit
profiles could widen its own next spawn. The brief-level reservation list
(FDR 0019 §5's six moxy meta tools) should name it when it exists.

### Coexistence

Both paths share the launcher, the unit, the identity, the confinement check
and the spawn handshake. They differ only in the process in the unit and in
where transcript, hooks and metrics originate. A worker's metrics carry no
harness segment today; add one (or a separate family) so the two can be
compared without dashboard changes breaking.

## 7. UNVERIFIED items from the first draft, and their status

| Item | Status | Where |
|---|---|---|
| moxy consults its resolver on a plain `tools/call` | **Settled: it does not** (moxy#485; `proxy.go:1024-1072`; bb446c3) | §3.2 |
| FDR 0019's "non-always-allow resolves to deny" | **Settled: false on moxy's side**; FDR 0019 needs an erratum | §3.2 |
| `jugglerbrief` strictness for additive fields | **Settled: unknown fields are rejected** (`brief.go:228`) | §Interface |
| the juggler HTTP client honours `HTTPS_PROXY` | **Settled by reading: `http.DefaultClient`** (`cmd/juggler/main.go:35`); unmeasured in a unit | §4.7 |
| which system-prompt fragments name Claude Code tooling | **Settled: none of the three in-repo fragments** | §1.8 |
| F3 with no pty | **Settled: holds iff the unit has no tty fd**, so clown must not pass its stdio | §4.2 |
| a new `provider` value needs a `ProtocolVersion` bump | **Settled: no**; older consumers fail closed at `Validate` | §6 |
| client behaviour when moxy invalidates the session id | **Settled: no re-initialise path** (`client.go:244-287`) | §1.7 |
| the 360 s SSE drop's cause | **Open**; not in any source; measurement in Stage 0 | §1.7 |
| Claude login credentials usable by a custom loop | **Open**; presumed not | Q1 |
| `merge-this-session`'s rebase rewriting the committer | **Open, and moot** for attribution under FDR 0036 §9 | §5.2 |
| egress proxy vs long non-streaming responses | **Open**; a CONNECT tunnel's idle handling | §1.13 |
| what `sc list`/posh show for a session with no pty | **Open** | Q11 |

## Risks

1. **Capability and quality regression.** Claude Code's scaffolding (prompts,
   tool descriptions, edit tools, compaction) is substantial; a thin loop may
   complete fewer tasks. Mitigation: Stage 1 is read-mostly, Stage 2 compares
   on the same briefs, the Claude path stays.
2. **Billing change.** A loop calls a provider with an API key; the Claude
   Code path uses the operator's login. Cost per task is unmeasured.
3. **A key inside the unit.** The agent can read what the loop reads.
   Per-run, spend-limited keys bound it.
4. **Ungated tools until moxy#486 lands.** Today every non-always-allow tool
   a juggler agent lists runs with no verdict (§3.2). Stage 1 is restricted to
   always-allow tools for exactly this reason, and FDR 0019's existing agents
   should be re-read for the same exposure.
5. **The loop is inside the boundary.** Any design that leans on the loop's
   policy as a guarantee is wrong; §3.3 and §3.7 keep the authoritative gates
   outside the model's process where the architecture allows and name the
   unit as the only hard boundary today.
6. **Two harnesses to maintain**, and dashboards that must treat their
   metrics as one family.
7. **Extraction entanglement** (clown#258): the loop must not import
   `cmd/clown`; `promptwalk`/`agentscope`/`staging` stay on clown's side,
   passed in as strings and file paths (the brief, the spec). An embedded moxy
   (moxy#487) adds a second entanglement in the other direction; sequence it
   with the extraction decision.
8. **Long tool calls vs a 30 s client timeout; no session re-initialise; no
   retry.** Each is a flaky-run source until fixed.
9. **No resume, no compaction in Stage 1**: long sessions fail rather than
   degrade; this is deliberate and visible.
10. **F11 blind spot** until spinclass's census change lands (§4.4): a loop
    worker, confined or not, is not counted as an agent.
11. **Loss of async/batch** (reserved from briefs by FDR 0019) for loop
    workers; `just-us` recipes are the likely replacement for background
    builds and need their own tier under enforce.
12. **Profile/model mis-selection** (clown#281) reproduces silently on this
    path unless the brief records the model it resolved and the ledger the
    model it called.

## Open questions

Operator:

- **Q1. Credentials and billing.** Is a per-token API key acceptable for
  confined workers? Which provider (Anthropic direct, OpenRouter), and
  per-run keys with spend limits? Is Claude-login use by a custom loop
  permitted at all (UNVERIFIED)?
- **Q2. Session mode in v1?** Or only task-shaped workers until a use case
  demands parking?
- **Q3. A shell.** Which of moxy `chix` develop-run, the `just-us` recipes
  server, or a bounded exec tool, if any, belongs to the loop's toolset?
- **Q4. Identity and signing** (§5.2, circus#351): the value the frontend
  stamps; and whether a commit may request a signature at all from a
  headless worker.
- **Q5. How a spawn selects the loop:** new harness value or a `loop` profile
  field (§6)?
- **Q6. Non-blocking escalation:** only chat-and-park, or a dedicated
  `request_permission` tool that records the request and returns at once, as
  input to the card-rooted grant mechanism (spinclass#383/#384)?
- **Q8. Where does the loop live** while juggler is mid-extraction
  (clown#258): keep it in `internal/jugglerloop` behind the brief as the seam
  (recommended), or a clown-native package?
- **Q13. Embed moxy (moxy#487) as the end state?** This record assumes yes
  and sequences enforce mode first; confirm, since #487 is a moxy
  restructuring with its own cost.

Other lanes (not decided here):

- **Q7. spinclass:** the three items of §4.6; FDR 0036 step 8 timing;
  translate or replace the Claude-grammar allow tiers (clown#276's scheme);
  how an attestation naming skills is satisfied honestly by a harness without
  skills. **moxy:** moxy#486's mapping for unannotated tools and the denial
  shape (§3.3); the ordering of #486 and #487.
- **Q9. Hello:** which of §4.5's three shapes; is `SendHello`'s record the
  whole contract.
- **Q10. ringmaster FDR-0001 timing:** the loop as a plain producer with one
  job per worker is assumed to survive steps 1–2 and to relocate at step 4;
  confirm, and confirm that `job_run`'s launch document can express FDR
  0020's unit (FDR-0001 §9 says the service enforces the frontend/agent split
  as an input).
- **Q11. Observation:** what `sc list` and posh show for a session with no
  pty (UNVERIFIED), and whether a watch/steer surface is wanted beyond the
  MUC.
- **Q12. The 360 s SSE drops**: attribute them (Stage 0 measurement) before
  the loop inherits the component responsible.

## Interface (proposed, not built)

- harness `juggler-run` (name tentative); `clown --agent-scope <spec>`
  accepts it and `clown agent-scope-capabilities` advertises `headless-loop`.
- brief gains `mode = "task" | "session"`, a `policy` rule table (§3.4(b)), a
  `denial_budget` limit, and a `harness` preamble flag. Because the parser
  rejects unknown fields (`brief.go:228`), this is **schema 2**, or schema 1
  with the parser changed to tolerate a named set of additive fields; the
  former is cleaner and the brief's `schema` field exists for it.
- moxy launched by the loop with enforce mode set and locked (moxy's flag or
  moxyfile key, per moxy#486), `MOXY_PARENT_MOXYFILE` as today.
- local turn log: `<agent state dir>/turns.jsonl`, one `Turn` per line,
  outside moxy's CWD for the same reason `traffic.jsonl` is (FDR 0019 §4).
- ledger entry kinds added: `denied`, `denial_budget`; `end.reason` added:
  `denial_budget`, `closed`; the ledger records the model actually called.
- unit record: `pty` empty; a new optional `harness` field naming the
  process clown exec'd (input to §4.4's census inversion).

## Limitations of this record

- Claude Code's internals were not read; its behaviour is inferred from
  clown's hook code, the toolcost plan's read of one 2.1.301 transcript, and
  spinclass's records.
- moxy's source was read for the call funnel, the resolver, the hook's
  dontAsk path and the skipped bats; not for `serve-http`'s streaming or
  session handling.
- The moxin tier annotations of folio, rg, jq, man and env were not read;
  Stage 1's "always-allow only" list must be derived from them.
- No measurement was taken. Stage 0 lists the measurements that would replace
  the guesses here.
- FDR 0019's erratum (§3.2) is noted, not applied.

## More Information

- clown FDR 0019 (juggler agent substrate); FDR 0020 (agent scope
  confinement); FDR 0006 (matrix); FDR 0018 (clown name and session lineage);
  FDR 0002 (stdio bridge); RFC 0017 (dumbo mock model API, the loop's test
  double).
- `docs/plans/2026-10-20-unused-tools-per-session.md` (the `LoadedTools` seam).
- spinclass FDR 0032 (principals, handles, provenance), FDR 0034 (spawn with a
  clown profile), FDR 0035 (session confinement; the hello, F1–F15, the dontAsk
  decision, the two confinement switches), FDR 0036 (grit store tools; §9
  confined callers), FDR 0006 (spawn sibling sessions; the hello).
- ringmaster FDR-0001 (host service; §9 launch documents, §11 sessions, §15
  request identity, §17 sequencing), via clown#260.
- moxy#484 (closed, 18ea283), #485, #486, #487; moxy
  `zz-tests_bats/server_side_perms.bats` (bb446c3).
- clown#252, #255, #258, #260, #261, #263, #269, #274, #276, #277, #280, #281;
  spinclass#383, #384; circus#351, #353.
