---
status: proposed
date: 2026-10-06
promotion-criteria: >
  experimental once a real Pebble recording on the webhook host launches an issue-filer
  agent through `juggler spawn`, the agent files an issue through moxy, the
  run's transcript lands in the run's MUC, the ringmaster job terminalizes
  with the evaluator's verdict, and a forced failure (zero artifacts or
  `cannot_complete`) triggers the fallback script, end to end, at least once.
---

# juggler agent substrate — worktree-less agents under a certified principal

> **Parent record:** spinclass FDR 0032, *Principals, handles, and
> provenance* (`docs/features/0032-principals-handles-and-provenance.md`
> in spinclass). This FDR is that record's **slice 3** (D9) and fills the
> clown#245 placeholder. It MUST be read with FDR 0032; where the two
> disagree, the four seams listed under *FDR 0032 touch-points* are the
> known ones and are being taken to the operator by the spinclass session.

## Problem Statement

Every agent the fleet can launch today is a Claude Code harness inside a
spinclass worktree session. The Pebble webhook host (circus#288) needs
short, headless, tool-using agents with no worktree, no repo and no
terminal, launched by a system service, each under its own principal so
FDR 0032's handles, exit wakes and provenance apply to it. juggler already
owns model resolution and a single-turn prompt path but has no agent loop,
no tool execution, and no lifecycle or identity integration. This FDR
gives juggler that loop and makes it the substrate every future agent runs
on, with the frontend (Claude Code via clown, trapeze, or XMPP itself)
decoupled from it.

## Interface

### 1. The verbs

**`juggler run`** is the agent. It is a headless process that:

1. reads its **brief** (§3) — in v1 from the file `juggler spawn` staged
   in the agent's state directory, while the identical brief stanza in
   the run's MUC (§7) is the provenance record; reading the brief FROM
   the room is the XMPP-native form and arrives with signing;
2. drives the model's tool-use protocol in a loop (§8) until the model
   ends its turn, calls `cannot_complete`, hits the step cap, or hits
   the wall-clock budget;
3. executes every tool call through **one moxy upstream** (§5),
   recording each call's outcome in the **ledger** (§4);
4. appends every turn to the run's MUC as a stanza;
5. at the end, runs the brief's **evaluator** (§4) over the ledger and
   writes the ringmaster job's terminal record (§6) with the verdict.

It is a ringmaster **producer**: `job_start` at boot, `job_progress` per
turn, `job_done` at the end. It is NOT a ringmaster-spawned process.

**`juggler spawn`** is the launcher-side glue a spawner calls instead of
assembling the pieces by hand. Given a brief, it:

1. mints the agent's principal (a fresh UUID) and its JID via
   `troupe mint --session-key <uuid>`, recording the credential by
   reference (a password-file path, never the secret);
2. creates the run's MUC when the brief does not name an existing one,
   and drops the signed brief into the room as its first stanza;
3. starts `juggler run` as a **systemd transient unit** (§9) with the
   principal, the credential reference, the room JID and the ringmaster
   target in its environment, and with `ExecStopPost=juggler exit-wake`
   on the unit as the exit-wake emitter (§6);
4. records the spawner's **handle** on the job (§6) — the first grant;
5. returns the agent's JID and ringmaster job id to the caller.

If step 3 fails (systemd refuses the unit, D-Bus or polkit is down,
delegation is unavailable), `juggler spawn` MUST fail synchronously and
MUST clean up what steps 1–2 created, or leave them in a state a retry
with the same brief reuses idempotently (the mint already is; the room
and brief drop must be too). An orphan room holding a brief and no agent
is a bug, not an accepted state.

`juggler spawn --new-run` is the **issuer's** entry for a new tree: it
mints the run's **root principal** (§2) and certifies it under the
issuer, creates the run's MUC (`--room-domain <D> --operator-jid <jid>`:
the root runs `troupe muc create --room <run-key>@<D> --owner <jid>`,
§7; `--operator-jid` defaults to `$JUGGLER_OPERATOR_JID` and is
REQUIRED with `--room-domain`, because the root must never be the
room's only owner) or takes a pre-created one (`--room <jid>`), posts
the recording (or whatever the run's input is) as the room's root
stanza, starts a **run-level** ringmaster job owned by the root, and
returns the root's credential reference, room JID, run job id and, when
set, `"operator_jid"`. The root's principal is **derived from the run
key** (UUIDv5 of the key under juggler's fixed namespace
`b5a2c9e0-7d14-4f3a-9c6e-1f8d2a0b4e57`; children stay random), so a
`--new-run` that fails after the mint keeps the root — account and
password file — and the room it may have created (rooms are persistent),
withdraws a posted run input, fails a started run job, and saves a
**pending** run record. A retry with the same `--run-key` resumes it:
the same root is re-minted (same key and file: troupe answers
`created=false`), still owns `<run-key>@D` so `muc create` is
idempotent, re-posts the input and starts a fresh run job (see the
three run states in the glue-facing contract below). A pending run takes
no subagents and no route entries (`juggler decide --run-key` refuses it
before any model call). Every later step of the run — the
decision, the subagent spawns, the fallback — executes as the run root.
The run job's terminal record is written when the run is resolved
(`succeeded`, or `failed` when the fallback ran), and its exit wake goes
to the issuer.

**Glue-facing contract** (settled with circus FDR-0039's interface asks;
the glue is a short-lived webhook handler, not a daemon with a wake
channel, and it never parses the room):

- `--new-run --run-key <key>`: caller-supplied idempotency key (derived
  from the recording). A run key is in one of three states, and the
  output says which:
  - **new / resumed** — exit 0 with the full object; `"existing": false`,
    and `"resumed": true` when this call finished a pending run (absent
    otherwise).
  - **pending** — the call failed after the root's mint: exit 1, the
    cause on stderr, and ONE stdout line `{"run_key", "pending": true,
    "root_principal", "room"}`; the same call resumes it. (A failure of
    the mint itself saves nothing and prints nothing on stdout.)
  - **existing** — a complete run with that key is returned unchanged
    with `"existing": true`, nothing created, plus `"resolved"` and
    `"torn_down"` so a redelivery can finish a run whose first delivery
    crashed before `resolve` instead of dropping it.

  The output also
  carries `"root_stanza"`, the run-input stanza id `juggler decide
  --parent` takes. Every identity the run mints gets its own troupe
  password file under the run's state directory (`--password-file`), so
  a child's mint can never overwrite the root's credential. A subagent spawn
  with the same brief digest within a run is likewise idempotent. Without
  `--run-key` a fresh key is minted.
- `--wait [--timeout <dur>]`: block until the subagent's job
  terminalizes and print `{"jid","job","room","state","reason","message",
  "ledger":"<path>","artifacts":[{"tool","kind","uris"}],
  "cannot_complete":null|{"reason"}}` where `state` is the ringmaster
  state and `reason` the D6 reason (§6). Exit code mirrors the state:
  0 `succeeded`, 2 `failed`, 3 `aborted`, 4 `interrupted`, 1 usage. At
  `--timeout` the child job is asked to cancel (`ringmaster cancel`) and
  given `--stop-grace` (default 30s) to terminalize: exit 3 if it
  aborted in time, else 5 ("cancel requested, still running"), so a
  caller that falls back never races a late agent. Without `--wait`,
  the launch JSON `{"jid","job","room"}` is printed immediately.
- `juggler job-ledger <job>`: print a job's ledger (the result spool)
  for a run the caller did not wait on.
- **Templates.** Brief authors ship a template without `principal`,
  `parent`, `room` and `task`; `juggler spawn --brief <template> --task
  <file|->` fills those four (minted child key, the run root, the run's
  room, the task content) and then validates the filled brief strictly.
  `model`, `system`, `tools`, `evaluator` and `limits` must be in the
  template; `model` is the author's choice of registry entry, which the
  spawner's models file must define.
- **`juggler resolve <run-job> --state succeeded|failed --reason <text>
  [--fallback-artifacts <json>] [--stop-grace <dur>] [--result-line
  <text> --canary-room <jid>] [--keep-accounts]`**: the run's last call,
  always. In order:
  1. with `--result-line`/`--canary-room`, post the one-line result into
     the canary room **as the run root** (`troupe muc send --source
     juggler-resolve`) — first, because the root's account dies in step
     4; the post is recorded at once (a `canary-posted` marker in the
     run's state directory, then a `canary` ledger note
     `{tool:"canary", kind:"note", ok, reason, stanza_id}`), so a resolve
     retried after any later failure never posts it again; a failed post
     is a not-ok note, writes no marker, and changes nothing else;
  2. cancel every child job of the run that is not yet terminal and wait
     up to the stop grace for each (one `subagent_stop` ledger entry per
     child, `stopped_by_resolve`);
  3. append the `fallback` entry (the fallback's own outcome) to the run
     ledger, write the run job's terminal record and emit the run's exit
     wake to the issuer;
  4. **tear the run's accounts down** (default; `--keep-accounts` skips
     it): for each child whose job is terminal, `troupe muc affiliate
     --affiliation none --jid <child>` as the root (rooms the run created;
     best effort), `troupe mint-revoke --session-key <child>
     --password-file <its file>`, then delete the file; then, once no
     child account remains, the root sets its own affiliation to `none`
     (only when the run created the room with an operator owner — the
     last owner cannot step down; otherwise skipped and said so in the
     entry), revokes itself and deletes its file. troupe cannot tell "no
     such account" from "wrong password", so a SASL login refusal at
     revoke is "already gone" and juggler deletes the file itself; any
     other revoke failure — including a TLS or stream failure during
     negotiation — keeps the account and its file. A child still running after
     the grace keeps its account (`ok:false`, `"still running"`), and the
     root keeps its own while any child account remains. One entry per
     account: `{tool:"teardown", kind:"account", ok, principal, jid,
     reason}`. `RunRecord.torn_down` is set when every account is gone.
     **The minter credential is required here:** troupe's `mint-revoke`
     takes the privilege-free self-removal path only when a minter
     credential is named, and juggler hands troupe its own environment,
     so `juggler resolve` (like `spawn --new-run` and `spawn --brief`,
     whose mints need it too) MUST run with `TROUPE_MINT_PASSWORD_FILE`
     (and `TROUPE_MINT_USER`) set. Without `--keep-accounts`, resolve
     refuses (exit 1) when `TROUPE_MINT_PASSWORD_FILE` is unset, before
     the canary post and before stopping any child. This is the
     spawner's side only: the same variables are stripped from every
     agent's unit and tool servers and rejected in a brief's `[env]`
     (§3, §5).

  Resolving a resolved run rewrites nothing, posts no canary line and
  sends no wake: it only performs a teardown still pending (accounts with
  an ok entry are skipped), so a re-run after the stuck child ended
  finishes the job. A retried *partial* resolve (one that failed after
  posting, e.g. on the exit wake) posts no second canary line either and
  relays the terminal record already written. `juggler spawn --brief`
  holds the run lock from its resolved check through the child's record
  and unit launch, so a spawn racing `resolve` is either refused or seen
  (and stopped, or kept as still running) by the teardown — never left
  live in a torn-down run. stdout gains `"torn_down"`, `"teardown"` (this
  call's entries; absent when it handled no account) and, when
  `--canary-room` was given, `"canary": {"room", "posted", "stanza"}` —
  `posted` is true also when an earlier attempt posted the line, and
  `stanza` is then that post's id. (Not named `run-…` to avoid confusion with `juggler
  run`.)
- Every reason is on stdout and in a ledger: `cannot_complete.reason`
  in the subagent ledger and the `--wait` output; `juggler decide`'s
  reason in its stdout object and the decision stanza.
- The run root has **no MCP tools**: it is a principal with a credential
  and a budget slice, not an agent. The result line to the fleet canary
  room is a `troupe muc send` as the root, not a tool call; `juggler
  resolve --result-line --canary-room` makes it, so the glue never needs
  the root's credential after `resolve`.

**`juggler decide`** is the router. It is a synchronous verb, not an
agent: one request to a **Decisions-API**-style model (OpenRouter's
`POST /api/alpha/decisions`, which the first bullet's router model
`typesafe/jev-1.13` runs on; it generates no text and has no tool
calls), no loop, no ringmaster job of its own. Given the Decisions
request shape on stdin minus `model` and auth (`{"state": …,
"questions": {…}}`, exactly as OpenRouter documents it), plus
`--model <registry name>`, `--room`, `--parent <recording stanza id>`
and `--min-confidence <0..1>`, it:

1. resolves the model from the registry (a new `style = "decisions"`,
   URL and credential reference from the entry, §10's daemon-free path);
2. sends the request and parses `answers`;
3. applies the confidence threshold;
4. posts ONE **decision stanza** into the run's room as the run root,
   provenance parent = the recording, body = {model, digest of `state`,
   the `questions`, the full `answers` incl. confidence and
   per-option probabilities, the threshold and its verdict, usage};
5. prints the full `answers` object (plus `id`, `model`, `usage`) on
   stdout and exits with one of:

| exit | meaning | glue's action |
|---|---|---|
| 0 | usable choice (confidence ≥ threshold), stanza posted | spawn the chosen subagent; its brief's provenance parent is the decision stanza |
| 4 | valid choice but **below threshold**; stdout and stanza carry choice + confidence | fallback note recording the router's choice and confidence |
| 2 | **no choice**: HTTP failure, unparseable response, or `choice` not in `criteria`; stdout carries `{reason, http_status?, raw?}`; a stanza records the attempt | fallback note with the reason |
| 3 | decided but the room post failed (provenance chain broken) | treat as failure |
| 1 | usage/config error before any call | treat as failure |

Every non-zero exit is a `failed` entry of kind `route` in the **run
ledger**, recorded by the lifecycle owner (the glue in v1, §11), and the
run resolves to the fallback. The router therefore keeps "success is
measured, never reported": its outcome is a recorded, mechanical fact,
not a model's claim.

The spawner is the FDR 0032 **D2 issuer** for the tree it starts. juggler
never invents a principal and never touches troupe certificates.

### 2. Identity

The agent's principal is the per-instance key (FDR 0032 D1), resolved
exactly as clown resolves its own: explicit `CLOWN_SESSION_ID` wins, so
the spawner supplies it. The key reaches `juggler run`'s own runtime and
is **stripped** from the environment handed to moxy and every tool
server (clown#136 hygiene). The parent's principal and JID are passed in
the environment too (FDR 0032 D7), so the agent knows whom its exit wake
and any certificate request go to.

**The run root.** Per FDR 0032 D2, each run (one recording, one tree)
gets a **fresh root principal**, minted and certified by the issuer
(`juggler spawn --new-run`); it is fresh per run but deterministic per
run key (UUIDv5, §1), so a retried `--new-run` resumes the same root. The root is a member of the run's room
(the operator is its owner), signs the decision stanza and every
subagent brief, holds the handles on the subagents, carries the run's
D19 budget slice, and expires with the run; D5's cap means its lifetime
bounds every child's. The issuer's long-lived process acts as several
run roots concurrently by holding one credential reference per run.
This is preferred over the issuer acting as root for every run because
it gives per-run budgets and handles, bounds blast radius to one run,
and makes each provenance DAG single-rooted. **The root's lifetime ends
at `juggler resolve`**: its account (and every child's) is revoked and
its password file deleted there (§1), after it has posted the run's
result line. The room outlives it, owned by the operator, and is swept
at the run's retention point (circus: 30 days). The troupe certificate
record that names the root MUST outlive the account, so the run's
signed stanzas stay verifiable after teardown; keeping it is troupe's
lane.

### 3. The brief

The brief is the contract the spawner signs. It travels as one stanza
into the run's MUC and is the agent's ONLY instruction source. Fields:

| Field | Meaning |
|---|---|
| `schema` | brief schema version (integer, starts at 1) |
| `principal` | the agent's per-instance key the spawner minted |
| `parent` | the spawner's principal |
| `room` | the run's MUC JID |
| `model` | a juggler registry name (local GGUF or remote entry) |
| `system` | the system prompt |
| `task` | the task text |
| `moxyfile` | the agent's **inline moxyfile** as a TOML string (§5) |
| `env` | string→string table passed into the agent's unit environment; how per-agent tool configuration (e.g. a target mode, `MOXIN_PATH`) reaches moxy and its moxins, which moxyfile(5) cannot carry. `CLOWN_SESSION_ID`, `TROUPE_XMPP_*`, `TROUPE_MINT_*` (the spawner's minter credential) and `JUGGLER_*` are reserved and rejected |
| `tools` | REQUIRED allowlist of exact tool names as moxy advertises them (`<server>_<tool>`); `juggler run` offers the model only these. A listed name moxy does not advertise is a startup error; the list may not be empty |
| `evaluator` | `{kind, program, stop_on_pass}`; first kind is `jq` (§4). `stop_on_pass` (default false) ends the run with `evaluator_pass` as soon as the program first passes after a successful tool result (§4) |
| `limits` | `{steps, wall_clock, sandbox}`; `sandbox` is RESERVED and unused in this slice (§9) |

At slice-0 strength the stanza is unsigned; the field set is what later
gets signed under the operator chain (FDR 0032 D10), so nothing is
reshaped when signing arrives.

### 4. Ledger and evaluator — success is measured, never reported

The model is NOT responsible for reporting its own success. The runtime
keeps a **ledger**: one entry per tool call with the tool name, the
artifact `kind` the tool server declares for it, the success flag, the
returned URIs and any error, plus the terminal facts (how the loop
ended, whether `cannot_complete` was called and with what reason). The
URIs of a successful MCP call are read first from the CallToolResult's
`structuredContent` (MCP's typed result), then from its content blocks
(text blocks that parse as JSON, `resource_link` uris, embedded
resource uris); structuredContent and JSON text blocks both by the
top-level-field rule (`uri`/`url`, `uris`/`urls`), each URI once. The
ledger carries a `schema` version and, when the loop has any, `notes`
(facts that decide nothing, e.g. a per-step evaluator check that
errored). `end.reason` is one of `end_turn`,
`cannot_complete`, `step_cap`, `timeout`, `tool_error` (a tool's
transport failed), `model_error` (the model endpoint returned a
non-2xx or unparseable reply before the deadline), `max_tokens` (the
provider cut the reply off at its token limit with no tool call to act
on), or `evaluator_pass` (the brief set `evaluator.stop_on_pass` and the
evaluator, run over the in-progress ledger after a successful tool
result, first returned true). Only `end_turn` and `evaluator_pass` can
lead to `succeeded` — both through the same final evaluation, run once
over the final ledger; every other reason is `failed` at the job level.
With `stop_on_pass`, the per-step check uses the same evaluator and
budget as the final one over a ledger of the final shape whose
`end.reason` is still empty; an error in it is a ledger note and does
not stop the run; the step cap, wall clock and `cannot_complete` are
unchanged. A tool call whose arguments are not valid
JSON is answered with an error tool result the model can recover from,
never a transport failure, under both codecs. The brief's wall clock
starts when `juggler run` starts, so it also covers model resolution,
the moxy launch and the tool handshake and always expires before the
unit's `RuntimeMaxSec` backstop.
The task itself is not a transcript turn — it is the brief stanza's
content — so the agent's first recorded turn has the brief as its
provenance parent.

Every agent is given a mandatory **`cannot_complete(reason)`** tool. Calling
it ends the run; the reason is recorded in the ledger and in the job's
message. The word *abort* is never used for this tool: `aborted` is
reserved for a holder cancelling the job (§6).

The brief's **evaluator** collapses the ledger into a boolean. The first
kind is a **jq program** run embedded through gojq, eBPF-style:

- input: the ledger JSON document;
- output: exactly one boolean; an error, no output or a second output
  evaluates as `false`;
- no environment loader, no `input`/`inputs`, no module loader; a
  wall-clock budget via context cancellation. gojq has no exec or I/O
  surface to disable.

`evaluator.kind` is an explicit field so `predicate` (a compiled
count-of-kind clause) and `agent` (a judging model call) can be added
later without reshaping the brief. The same program is runnable by any
verifier over the same ledger: the runtime over the job's result spool
on the host, troupe over the per-agent slice of the room's archive. That
is FDR 0032 D16's mechanical attestation.

The **router's choice is a `route` entry in the run ledger**, recorded
from `juggler decide`'s exit (§1) by the lifecycle owner, so the one
rule has no exception and the router is a live unit test of the whole
path. (An earlier draft had the router as a model-emitted `route(choice)`
tool call inside an agent loop; the first bullet's router model runs on
a Decisions API with no tool calls, so the choice is recorded by the
glue instead — same ledger shape, different recorder.)

The ledger carries both the tool **name** and the declared **kind**, so
an evaluator may count by either. circus FDR-0039 settles that the
Pebble tools are narrow purpose-built wrappers (never cutting-garden's
generic create/patch/delete), so counting successful `create_issue` or
note-write calls by name is sufficient there; `kind` is for briefs whose
toolset is not hand-built per agent.

### 5. Tool plane — moxy, narrowed per agent

`juggler run` speaks to exactly one MCP upstream: a **moxy** instance
over streamable HTTP (`moxy serve-http`, or `clown-stdio-bridge` around
`moxy serve-mcp`). moxy owns child-server lifecycle, the moxyfile
cascade, permission tiers and large-output blobbing. juggler links only
the HTTP JSON-RPC client clown already has (`internal/mcphttp`), not
`internal/pluginhost`.

The brief's **inline moxyfile is the agent's toolset**. It MUST be a
subset of the spawner's own effective moxyfile. That is FDR 0032 D13's
"ambient rights a subset of the parent's" with a concrete artifact:

- checked **launcher-side** by `juggler spawn` for a fast, legible
  failure;
- enforced **moxy-side**, authoritatively, as a narrowing-only step in
  moxy's existing cascading merge (alongside `moxy validate`). How moxy
  learns a principal's effective moxyfile is moxy's lane and is handed
  to a moxy session at the operator's timing.

Headless posture: any moxy permission tier other than `always-allow`
resolves to **deny** for a juggler agent; the agent sees the denial as a
tool error it can route around or `cannot_complete` on.

**How `juggler run` reaches moxy.** It writes the brief's moxyfile to the
agent's state directory and launches `moxy serve-http
--name-template '{server}_{tool}'` with that directory as CWD, so by
moxyfile(5)'s hierarchy the agent's file is the innermost layer and the
spawner's global moxyfile is the ceiling. moxy binds an ephemeral
loopback port and prints the clown-plugin handshake, which gives the URL.
The underscore template is mandatory: moxy's default `{server}.{tool}`
contains a dot, which neither provider's tool-name grammar accepts. Tool
names in the ledger and in the brief's `tools` allowlist are therefore
`<server>_<tool>`, e.g. `ring_create_issue`.

**Per-agent tool configuration travels in the brief's `env` table**, not
in the moxyfile: moxyfile(5) has no environment key, cannot configure a
moxin, and cannot set `MOXIN_PATH`. `juggler spawn` passes `env` into the
unit, and moxy and its moxins inherit it. (An earlier draft said "inside
the moxyfile as the servers' arguments or environment"; that is only
true for `[[servers]]` entries, not moxins, and is withdrawn.)

**Allowlist, not deny-list.** moxyfile narrowing is additive deny-lists
(`disable-moxins`, `disable-servers`), which fail OPEN when a tool is
added to a moxin later. The brief's REQUIRED `tools` allowlist, enforced
by `juggler run` before the model sees any tool, is what makes "anything
not explicitly allowed is denied" true for the first bullet; the
moxy-side narrowing merge (above) remains the second, authoritative
layer when it lands.

### 6. Lifecycle — ringmaster enforces, troupe authorises

The tension between a distributed identity layer and a host-local
process is resolved by splitting the handle into the authority and the
object it names:

- The **ringmaster job is the host-local object and the enforcement
  point**. Observe is `job_wait`/subscribe, close is `job_cancel`, exit
  is the terminal record, crash is the liveness reaper's `interrupted`.
  Only the host that owns the process can see it die.
- A **handle is a signed grant stanza** under troupe's identity chain
  (FDR 0032 D12/D13). Grants pass as stanzas over XMPP; a holder on
  another host never reads a journal.
- The **handle table on any host is the set of grant stanzas that
  host's ringmaster has accepted**, stored on the job's journal as a
  cache of verifiable facts, rebuildable from the stanzas, never the
  source of truth.
- Exercise flows **inward**: holder → stanza to the agent's JID → the
  runtime verifies the right → ringmaster. Exit flows **outward**:
  ringmaster terminal record → the unit's post-stop hook → an exit-wake
  stanza to every holder listed on the job.

ringmaster is the file-descriptor table on the host; troupe is the
capability token. Account teardown at the end of a run (§1 `juggler
resolve` step 4) needs the spawner's minter credential
(`TROUPE_MINT_PASSWORD_FILE`/`TROUPE_MINT_USER`) in `resolve`'s
environment, as the mints in `spawn --new-run`/`--brief` do; the agents
themselves never see it — it is stripped from their units and tool
servers. At slice-0 strength (one host, no crypto) the grant
stanza degenerates to a journal record carrying the holder's principal.

**Terminal states** are ringmaster's four, derived by the runtime:

| state | when |
|---|---|
| `succeeded` | the loop ended cleanly AND the evaluator returned `true` |
| `failed` | evaluator `false`; or `cannot_complete`; or step cap; or wall clock; or an unrecovered tool error |
| `aborted` | a holder cancelled the job |
| `interrupted` | the reaper found the producer dead (crash, SIGKILL) |

The launcher's one rule: anything other than `succeeded` runs the
fallback. The completion facts (created artifact URIs, the reason, the
room JID) ride the job's `result_ref` spool so the launcher reads them
without parsing prose.

**Mapping to FDR 0032 D6's exit reasons** (five, as the operator settled
them): `succeeded` = `normal`, `failed` = `failed` ("ended on its own
without doing what it was asked", measured by the runtime, never reported
by the agent), `aborted` = `shutdown`, `interrupted` = crash.

**The exit-wake emitter is the unit's post-stop hook, and it is the
only emitter.** `juggler run` never emits the wake itself: on the paths
where it is alive it writes the ledger verdict into `job_done` and
exits. The transient unit carries `ExecStopPost=juggler exit-wake`,
which systemd runs as a fresh process after the main process is gone,
on EVERY ending (clean exit, non-zero exit, signal death, `RuntimeMaxSec`
expiry, OOM kill), with the unit's result in `$SERVICE_RESULT` and
`$EXIT_CODE`/`$EXIT_STATUS` (systemd.service(5), systemd.exec(5)). The
hook reads the job's holders from the journal, writes the terminal
record if the main process did not, and sends one reason-tagged
exit-wake stanza to every holder. It runs inside the unit's cgroup, so
it is in the runtime scope (§9).

The mapping from the unit's result is mechanical:

| `$SERVICE_RESULT` | ringmaster state | D6 reason |
|---|---|---|
| `success` | whatever `juggler run` already wrote (`succeeded` or `failed`) | `normal` or `failed`, relayed |
| `timeout` (`RuntimeMaxSec`) | `failed`, written by the hook | `failed` — the wall clock is the brief's own limit |
| `signal` | `interrupted`, written by the hook | `killed` (force-reap by a holder or the platform) |
| `exit-code`, `core-dump`, `oom-kill` | `interrupted`, written by the hook | crash |

ringmaster's four states therefore do not grow a fifth; D6's fifth
reason is derived at emission from the unit result. A force-reap IS
possible for a juggler agent (a holder stops or kills the unit) and is
what `signal` covers. The ringmaster liveness reaper remains the
backstop that writes `interrupted` when the hook itself cannot run
(systemd gone, hook crashed); on that path no D6 wake is sent (see
Limitations).

### 7. Transcript — one MUC per run

A run (webhook → router → issue-filer → maybe a note-filer agent) has
**one MUC**, created by the spawner and named for the run. The spawner's
first act is dropping the brief into it. Every agent in the run joins
with its own JID and posts its turns there. The room's MAM is the run
transcript and the one link the fallback note carries. Per-agent
provenance survives because each stanza carries its sender's JID (and,
later, its signature); the evaluator reads a per-agent slice by sender.

**Provisioning** (troupe ≥ 4c52b3b, run as the run root:
`TROUPE_XMPP_USER`/`TROUPE_XMPP_PASSWORD_FILE` are the root's):

1. `juggler spawn --new-run --room-domain D --operator-jid O`: the root
   runs `troupe muc create --room <run-key>@D --owner O`. The creator is
   owner automatically and the room is forced persistent; the call is
   idempotent on a room the caller owns and fails on one it does not.
   The operator is the second owner from the start because MUC's
   last-owner rule forbids the only owner stepping down.
2. Before each `juggler spawn --brief`, after the child's mint and
   before its brief is posted or its unit started, the root runs `troupe
   muc affiliate --room R --affiliation member --jid <child>`; a failed
   spawn undoes it with `--affiliation none`. Affiliation does not join
   the room, so the child need not be online.
3. At `juggler resolve` (§1) each child is set back to `none`, then the
   root sets ITS OWN affiliation to `none`, leaving the operator the sole
   owner of the archived room.

A pre-created `--room` is the operator's: juggler neither affiliates
children into it nor steps the root down from it (its membership is its
owner's business). Destroying rooms is the retention sweep's job
(circus), never juggler's.

A turn stanza travels in one `troupe muc send` argument, which Linux caps
at 128 KiB per argv element. When a marshalled turn exceeds 64 KiB, the
ROOM copy's tool-result content (or text) is replaced by
`{"truncated":true,"sha256":"…","bytes":N}`; the ledger, the result
spool and the model's own wire format are untouched, so the archive
stays attributable and the digest lets the full content be matched to
the spool later.

### 8. Internal representation and provider codecs

The loop is written against a **stanza-shaped** internal representation:
a turn is a message with a sender principal, a provenance link to its
parent turn, a signature slot, and a typed body (text, tool call, tool
result, terminal fact). The transcript is that representation written
out. Two thin **codecs** project it onto the provider wire shapes, both
from the start: OpenAI-compatible chat completions (remote OpenRouter
entries) and Anthropic Messages (local llama-server). The existing
`fake-llama-server` fixture and the dumbo mock API (RFC 0017) are the
test doubles, one per shape.

The registry's third style, `decisions`, is NOT a loop codec. It is used
only by `juggler decide` (§1): a decision has no turns, no text and no
tools, so it never enters the loop or the stanza-shaped turn type; its
only artefact is the decision stanza.

### 9. Supervision and scopes

`juggler run` is supervised by **systemd as a transient unit** per agent
(`RuntimeMaxSec` as the wall-clock BACKSTOP; the loop's own budget from
the brief is the primary limit and cancels itself first), started by
`juggler spawn`. No daemon is involved.

Two scopes (clown#244):

- **runtime scope** — the transient unit itself: `juggler run` (XMPP
  connection owner, ledger, evaluator, job records), the post-stop
  hook, **and moxy**. The enforcer belongs with the enforcers.
- **agent scope** — a scope under the unit for the tool servers moxy
  spawns: the only thing the model can drive.

The tier-2 signer (piggy) accepts callers in the runtime scope and
refuses the agent scope. The signer's rule stays binary. It MUST fail
closed: when it cannot positively verify a caller's scope it refuses,
because cgroup attribution is the one place systemd sits on the trust
path rather than the liveness path (see Limitations).

**How the agent scope is realised is deliberately left open** between a
sub-cgroup `juggler run` creates (needs `Delegate=yes` on the unit) and
a sibling transient unit the tool servers run in. Only the second can
carry systemd's namespace sandboxing (`PrivateTmp=`, `ProtectHome=`,
`ProtectSystem=`, `InaccessiblePaths=`, `SystemCallFilter=`, …), which
is DEFERRED from this slice but must stay reachable; the brief's
reserved `limits.sandbox` field is where per-brief unit properties will
land. Which shape, and whether moxy spawns each child through
`systemd-run`, is decided with the moxy session alongside the narrowing
merge (§5). Nothing in this slice may build the sub-cgroup in a way that
forecloses the sibling-unit shape.

This is juggler's answer only. FDR 0032 and piggy FDR 0006 keep "which
scope a tier-2 key binds to" fully open by the operator's choice; this
section MUST NOT be cited as closing that question, and the Claude Code
case (where git and the tee sign from under `claude`) remains undecided.

### 10. The daemon is optional for remote models

`juggler run` resolves a **remote** registry entry straight from the
models file (`JUGGLER_MODELS_PATH`) when no control socket is reachable,
so a system-service user needs only a models file and a credential
reference in its own state directory. The daemon remains required for
**local** llama-server instances, where it owns the process; a
system-service form of the daemon arrives when a host needs local
inference for these agents.

### 11. Lifecycle ownership — v1 juggler, v2 spinclass

**v1 (this record):** `juggler spawn` owns spawn, supervision, handles
and the post-stop hook for worktree-less agents, as §1, §6 and §9
describe. The run root (§2) is modelled in juggler and ringmaster;
spinclass has no group above sessions and cannot represent a session
without a repo today. FDR 0032 with these decisions is on spinclass
master (gate sha 3f42f8d, 2026-10-06); v2 is tracked as spinclass#354.
spinclass's own decomposition (v1: the handle-record contract as a
normative FDR 0032 section, D6's five reasons as constants, docs/tests;
v2: outline only) is `docs/plans/2026-10-06-fdr-0032-spinclass-decomposition.md`
in spinclass. circus's is in circus FDR-0039's lane.

**v2 (operator decision, 2026-10-06, recorded in FDR 0032 D7):** the
lifecycle moves to a **spinclass unit session** — a session kind with no
worktree and no branch whose process is a systemd transient unit — and
`juggler spawn` disappears; `juggler run` becomes that kind's
spawn-entry exactly as `clown` is the worktree kind's. Operator's words:
spinclass is "eventually … the sole isolation boundary / enforcer for
worktree and unit agents and scripts" — the end state covers non-agent
processes such as the fallback script too. Whether the run root is then
a unit session whose process is the glue, or a new group record, is open
in FDR 0032. The trigger for starting v2, per FDR 0032: **the first
handle held across the two kinds** (e.g. a worktree session holding a
handle on a juggler agent).

**Constraint on v1 so v2 is a relocation, not a migration** (FDR 0032
D7): v1's handle records and exit reasons MUST keep spinclass's field
meanings — a holder is a principal string; a granted handle is pending
and confers nothing until first use; release removes only the caller;
rights are recorded per holder as a comma-separated list defaulting to
`observe,close`; exit reasons are D6's five.

Note from spinclass's survey: spinclass's own exit-wake emitter does not
yet outlive the principal, and its spawn does not mint the child's
principal (the child's harness does) nor launch through systemd. §6's
post-stop hook is therefore the first emitter in the fleet that
satisfies D6's "must outlive the principal" rule, and launcher-minted
identity (§2, §3) is new for both sides.

## Examples

An issue-filer brief (unsigned, slice-0 shape):

    schema = 1
    principal = "3f1c…"           # minted by the spawner
    parent    = "pebble-webhook@example-host"
    room      = "pebble-9f3a@rooms.xmpp.example"
    model     = "openrouter/anthropic/claude-sonnet"
    system    = "You file one issue per actionable item in the transcription…"
    task      = "<transcription text>"
    [limits]
    steps = 12
    wall_clock = "2m"
    [evaluator]
    kind = "jq"
    program = '''
      .cannot_complete == null
      and ([.calls[] | select(.kind == "issue" and .ok)] | length) >= 1
    '''
    # the agent's moxyfile travels as a TOML STRING (canonical bytes, so it
    # can be signed verbatim), not as a nested table:
    moxyfile = '''
    # narrows the spawner's moxyfile to smith's three verbs, always-allow
    …
    '''

The ledger the evaluator sees:

    {
      "schema": 1,
      "calls": [
        {"tool": "list_repos",   "kind": null,    "ok": true,  "uris": []},
        {"tool": "create_issue", "kind": "issue", "ok": true,
         "uris": ["https://code.example/clown/issues/301"]}
      ],
      "end": {"reason": "end_turn"},
      "cannot_complete": null,
      "steps": 3,
      "elapsed_ms": 18340
    }

The launcher's side:

    $ juggler spawn --brief issue-filer.toml --target <webhook-principal>
    {"jid": "3f1c…@xmpp.example", "job": "job-7a21", "room": "pebble-9f3a@rooms.xmpp.example"}
    # … later, the webhook receives the exit wake:
    #   job-7a21 failed: cannot_complete: "no repo matches 'the router thing'"
    # → the fallback script writes the note, linking the room and the reason.

The router, before any subagent exists:

    $ echo '{"state": "file an issue on moxy that the restart tool should reconnect automatically",
             "questions": {"route": {"type": "choice",
               "instructions": "A short voice request. Which kind of capture is it?",
               "criteria": {"issue": "The speaker wants to file, open or log an issue, bug or ticket against a named software project or repository.",
                            "note":  "Anything else: a note, idea, question or thought to save as spoken."}}}}' \
      | juggler decide --model jev --room pebble-9f3a@rooms.xmpp.example \
          --parent <recording-stanza-id> --run-key <key> --min-confidence 0.5
    {"answers": {"route": {"type": "choice", "choice": "issue",
                 "probabilities": {"issue": 1, "note": 0}, "confidence": 1}},
     "id": "gen-dec-…", "model": "typesafe/jev-1.13-20260917", "usage": {...}}
    $ echo $?
    0
    # → decide itself appends {tool: "route", kind: "route", ok: true, choice,
    #   confidence, top_probability, …} to the run ledger (--run-key) and the
    #   glue spawns the issue-filer with the decision stanza as parent.
    # exit 4 (below 0.5) or 2 (no choice) → a failed route entry → fallback note.
    # (Shapes are the two live Jev calls circus made on 2026-10-06.)

## Limitations

- **Slice-0 strength.** Nothing is signed yet: briefs, grants and exit
  wakes are unsigned stanzas or journal records on one host. The shapes
  are fixed so signing (FDR 0032 slice 3 proper, troupe's lane) is
  additive.
- **No ask-back.** A run that cannot finish never waits on the operator;
  it fails and the fallback runs. circus FDR 0023's escalation
  conversation is not part of this slice.
- **Duplicates over shortfalls.** A runtime crash after the model
  succeeded but before `job_done` yields `interrupted`, so the fallback
  may duplicate an artifact. Accepted by the operator; the fallback links
  the transcript and every created URI so duplicates are resolvable.
- **gojq has no step counter.** A pathological evaluator is bounded only
  by its wall-clock budget. Acceptable for a trusted spawner; revisit
  before briefs arrive from less trusted principals.
- **Transient units need authority.** A system-service user cannot ask
  the system manager for a transient unit without a polkit rule or
  `Delegate=yes` on its own unit. Which one the webhook host uses is circus's lane.
- **What systemd failing costs, by case.** The success verdict (ledger +
  evaluator), tool permissions (moxy) and identity (troupe) do not
  depend on systemd and fail closed. What does depend on it:
  - *systemd dies* (= the host dies): every agent dies, no emitter is
    alive, no D6 wake is sent. Holders on other hosts learn nothing until
    they time out, and no holder-side timeout is designed. Liveness
    loss, not safety: a dead agent cannot act. Whether the reaper marks
    the open jobs `interrupted` after a reboot is unverified.
  - *systemd degraded* (refuses transient units, D-Bus/polkit down,
    delegation broken): `juggler spawn` fails synchronously before an
    agent exists and the fallback runs. A legible stop. `juggler spawn`
    SHOULD check `systemctl is-system-running` as a precondition.
  - *systemd regresses*: a wall clock not enforced loses only the
    backstop (the loop's own budget is primary); a post-stop hook not
    run loses the wake, not the verdict (the reaper still writes
    `interrupted`, and the launcher rule works from the journal state
    alone); a mislabelled result degrades the D6 reason tag, not the
    success decision; a misreported cgroup is the ONE safety loss,
    because the signer authenticates by cgroup — moot at slice-0 (nothing
    is signed), mitigated later by the fail-closed signer (§9) and by
    deny-by-reachability once namespace sandboxing lands.
- **Namespace sandboxing is deferred.** The tool servers in this slice
  are bounded by moxy's permission tiers and the narrowed moxyfile, not
  by namespaces; the accepted exposure is resource exhaustion by a tool
  server and reachability of host sockets from the agent scope. The
  deferral MUST be lifted when either signing lands or an agent's tool
  servers include anything that is not a fixed-surface MCP binary. Unit
  sandboxing is the intended weight (not a container: tent exists for
  arbitrary Bash, these are known binaries), chosen per brief and
  narrowed like the moxyfile, never a fixed image.
- **The Decisions API is alpha.** OpenRouter marks `/api/alpha/decisions`
  alpha, so the router's request/response shape may move; the
  `decisions` registry style isolates that to `juggler decide`. Its
  reported probabilities also vary by up to ~0.08 on identical input per
  OpenRouter's own cookbook, so the threshold is a soft gate, not a
  deterministic one. Two authenticated calls (circus, 2026-10-06)
  confirmed the documented request/response shape, that field order
  differs from the docs and is unstable, that integers appear where
  floats are expected, and that a chat-completions call to the slug is
  refused with HTTP 400; `juggler decide` parses by name only.
- **Per-run rooms accumulate; credentials end at resolve.** `juggler
  resolve` revokes every account of the run (§1), but the room outlives
  the run, owned by the operator, until the retention sweep (circus's
  30-day job, itself deferred). Until that sweep exists every recording
  leaves one room behind — and one root account too when a child was
  still running at resolve or `--keep-accounts` was passed, until a later
  `resolve` finishes the teardown. A run that never reaches `resolve`
  (the glue crashed and no redelivery came) keeps all its accounts.
- **A failed `--new-run` that is never retried leaks its root.** To make
  the retry resumable (§1: the root is derived from the run key and kept
  on failure), a `--new-run` that fails after the mint leaves the root's
  account, its password file, a pending run record and possibly the
  room. If no retry ever comes, nothing revokes them; like the rooms,
  they are the retention sweep's to remove. Because the root is a
  function of the run key, a run key must never be reused for a
  different run. The pending record drops the failed attempt's run job
  id: if failing that job during the undo itself fails, the job stays open
  and the resumed run opens a second one.
- **"Already gone" is inferred from stderr.** troupe 4c52b3b exits 1 for
  a refused login, an unreachable server and a TLS or stream failure
  alike, and wraps every negotiation failure as `xmpp: negotiate: <err>`.
  juggler treats as a login refusal only exit 1 whose stderr has the SASL
  `<failure/>` right after that prefix — mellium renders it as the
  server's text (Prosody: `Unable to authorize you with the
  authentication credentials you've sent.`) or the bare condition
  (`not-authorized`) — or `sasl: not-authorized`; then the account is
  gone and the file is deleted. Anything else keeps the account and its
  file. If troupe or Prosody change that wording, every teardown keeps its
  accounts (fails safe, not open).
- **The ringmaster reaper does not back-stop juggler jobs today.** RFC-0018
  reaps a job only when the producer's advisory lock is released, and
  there is no CLI verb for a producer to take that lock, so `juggler run`
  cannot. If the post-stop hook cannot run (systemd gone, hook crashed),
  NOTHING writes `interrupted` and the job stays open. §6's "reaper as
  backstop" is therefore aspirational until ringmaster grows a lock verb
  (ringmaster#27). The hook is idempotent and is the only emitter in
  practice.
- **ringmaster protocol ≥ 2 is required** for `aborted`, `cancel-requested`,
  `cancel` and `wait --on-cancel`; an older installed ringmaster (which
  spelled the state `cancelled`) will not interoperate. **troupe ≥ 4c52b3b
  is required** for `mint --password-file`, `mint-revoke --password-file`,
  `muc create` and `muc affiliate`; an older troupe rejects the flag and
  every mint fails. In v1 both binaries are found on PATH or via
  `JUGGLER_{RINGMASTER,TROUPE}_BIN`; the nix-pinned paths (brief 6,
  clown#246) must satisfy both minimums.
- **The hardened webhook host.** troupe 4c52b3b's privilege-free mint
  (troupe#43: a minter credential instead of `sudo -n prosodyctl
  register`, password to a file, domain and c2s inputs) is what a
  hardened system service mints with; juggler passes `--session-key` and
  `--password-file` and leaves the domain, c2s host/port and minter
  credential to the inherited `TROUPE_XMPP_*`/`TROUPE_MINT_*`
  environment. Deploying that on the webhook host is circus's lane.
  Supervision on such a host uses `juggler
  spawn --user` against a lingering user manager for the service user;
  a polkit grant for arbitrary transient-unit properties on the system
  manager is root-equivalent and is rejected. Exit wakes are slice-0 journal messages to the
  holder's session key (`troupe message`), not stanzas to a holder JID,
  and the parent's JID is not yet passed into the unit (FDR 0032 D7).
- **Toolset granularity is the moxyfile's.** A brief that wants fewer
  tools than a server exposes needs a tool allowlist in the runtime,
  which is a later addition.
- **Not verified yet:** whether llama-server's Anthropic endpoint
  implements tool use. If not, local falls back to its OpenAI-compatible
  endpoint and the codec becomes a registry field rather than a kind
  inference. Also unverified: the `jobwake.ScopeArgv` precedent clown#244
  cites; it is not in the clown tree and may live in ringmaster.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| default step cap | 12 (loop-enforced) | circus FDR-0039's first-bullet figure; the agents make 2–4 tool calls, the cap is headroom | real briefs routinely hit it |
| default wall clock | 2 min (loop primary, `RuntimeMaxSec` backstop) | circus FDR-0039's first-bullet figure; matches `juggler prompt`'s budget | agents time out while a tool is legitimately slow |
| spend control | one dedicated OpenRouter key per spawner with a hard monthly credit limit (circus: ~$10 to start) | smallest thing that bounds blast radius | per-child budgets are wanted: the spawner mints a child key carrying a subset of its own budget (OpenRouter key minting), which is FDR 0032 D19's spend quota — an ambient, monotone, drop-only right inherited at spawn as a subset of the parent's, with the provider as the enforcement point — made concrete. Not D13's `cap`, which is the right to shorten a child's lifetime (D5). The key goes in the brief as a credential reference, not a secret |
| early stop on evaluator pass | off by default (`evaluator.stop_on_pass = true` opts in per brief) | an agent that keeps calling tools after the work is done spends steps and may duplicate artifacts, but stopping early is a behaviour change a brief author should choose | most briefs set it (→ make it the default) |
| evaluator kinds | `jq` only | smallest signed-contract surface | the same count-of-kind jq appears in most briefs (→ `predicate`) or a task needs judgement (→ `agent`) |
| headless permission posture | non-`always-allow` → deny | no human to ask | a moxin's tier is `ask` only because nobody set it, and agents keep failing on it |
| account teardown | on by default in `juggler resolve`; `--keep-accounts` opts out | a run's accounts have no use after its result line is posted, and each one left is a credential that can still log in | debugging a run needs its identities alive afterwards (→ `--keep-accounts`, then a later `resolve` finishes the teardown) |
| transcript layout | one MUC per run | one link for the fallback note; brief in the same transcript | runs grow long enough that per-agent rooms read better |
| daemon for remote models | optional | the webhook host's service user has no user session | a host needs local inference for these agents (→ system-service daemon) |
| agent-scope realisation | open: sub-cgroup vs sibling unit | the bullet needs neither namespaces nor the choice | signing lands, or a non-fixed-surface tool server appears (→ sibling unit + `limits.sandbox`) |
| exit-wake emitter | the unit's `ExecStopPost` hook, sole emitter | survives every ending the main process does not | a holder needs a wake on the systemd-dead path (→ holder-side timeout or a cross-host watcher) |
| router confidence threshold | 0.5 (`--min-confidence`, overridable per brief) | operator's first-bullet figure; below it the subagent is skipped and the fallback note records the choice and confidence. NOTE (two live calls, 2026-10-06): the API's `confidence` is the MARGIN over the runner-up (0.63 vs 0.37 → confidence 0.26), not the winning probability, so with two options 0.5 means roughly "0.75 or better"; semantics for 3+ options unverified. The stanza carries both `confidence` and the chosen option's probability | mirror-phase data shows good choices rejected or bad ones accepted |
| lifecycle owner | v1: `juggler spawn` | spinclass cannot represent a repo-less session today | the first handle held across the two session kinds (→ v2 spinclass unit session, §11) |

## FDR 0032 touch-points (operator-resolved 2026-10-06; FDR 0032 edited at spinclass 1647787, unmerged)

1. **Handle table ownership and exit-wake emitter: generalised.** D7 now
   reads that the handle table and exit-wake emission live with whichever
   platform owns the principal's lifecycle; for a juggler agent that is
   the juggler runtime and ringmaster, with the journal holding accepted
   grants as a rebuildable cache. D6 is now "emitted by the lifecycle
   owner". §6 here is the realisation.
2. **Exit reasons: D6 gained `failed`.** D6 now has five reasons:
   `normal`, `failed`, `shutdown`, crash, `killed`. The mapping and the
   force-reap answer are in §6. (An earlier draft of this record
   mis-stated spinclass's position as "carry the ringmaster state
   alongside the reason" and "`killed` collapses into `interrupted`";
   neither was theirs, and the operator chose the `failed` reason.)
3. **Scope binding: kept fully open.** §9 is not cited by FDR 0032 and
   does not close its open limitation; see the note at the end of §9.
4. **Fallback: a script.** The bottom-most fallback is a plain script
   writing to orgzly, not an agent. §6's "runs the fallback" means that
   script.

## More Information

- spinclass FDR 0032 — parent record; D1, D2, D6, D7, D9, D10, D12, D13,
  D16, D17.
- clown#245 — the placeholder this record fills; clown#244 — the
  agent/frontend scope companion note.
- circus#288 — the Pebble webhook-only path this is the first bullet
  for. circus FDR-0039 (`docs/features/0039-pebble-webhook-agent-graph.md`,
  commit 27c3a0f0 on `fair-linden`) is the consumer-side record,
  superseding circus FDR-0023 (the earlier ephemeral-clown sketch whose
  "launch mechanism" question is answered as "juggler agent"). Its
  first bullet has TWO agents on this substrate — an issue-filer
  (`list_repos`, `list_issues`, `create_issue`) and a minimal note-filer
  (one note-writing tool) — both carrying `cannot_complete`, with a
  script as the bottom fallback.
- Open on circus's side and possibly returning here: how the hardened
  `pebble-webhook` system user starts transient units and gets a troupe
  identity; and how the router model ("Jev", documented as a
  `POST /v1/systemone` endpoint) is called — if it is not reachable as
  an OpenAI-compatible or Anthropic endpoint, the models file needs a
  third style, which touches §8's codecs and §10's daemon-free path.
- FDR 0010 / FDR 0011 — the juggler daemon lineage; the daemon becomes
  optional here for remote models.
- RFC 0009 / RFC 0010 / RFC 0011 / RFC 0013 — job-wakeup channel,
  spool, job MCP tools, per-instance identity.
- RFC 0017 — dumbo mock model API, one of the two test doubles.
- `docs/plans/2026-07-11-juggler-subagent-tool-design.md` — the
  single-turn `juggler-prompt` tool this record supersedes as "the
  subagent path".
- Lanes: moxy (narrowing merge, principal → effective moxyfile), troupe
  (the MUC verbs juggler calls, grant grammar, signing, certificate
  records that outlive revoked accounts), circus (room retention sweep,
  webhook-host deployment, FDR 0023 amendment), clown (this record and `juggler run`/`spawn`).
