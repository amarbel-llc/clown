---
status: proposed
date: 2026-10-06
promotion-criteria: >
  experimental once a real Pebble recording on krone launches an issue-filer
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
spinclass worktree session. The Pebble webhook on krone (circus#288) needs
short, headless, tool-using agents with no worktree, no repo and no
terminal, launched by a system service, each under its own principal so
FDR 0032's handles, exit wakes and provenance apply to it. juggler already
owns model resolution and a single-turn prompt path but has no agent loop,
no tool execution, and no lifecycle or identity integration. This FDR
gives juggler that loop and makes it the substrate every future agent runs
on, with the frontend (Claude Code via clown, trapeze, or XMPP itself)
decoupled from it.

## Interface

### 1. Two verbs

**`juggler run`** is the agent. It is a headless process that:

1. reads its **brief** (§3) as a stanza from the run's MUC (§7);
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
| `moxyfile` | the agent's **inline moxyfile** (§5) |
| `evaluator` | `{kind, program}`; first kind is `jq` (§4) |
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
ledger carries a `schema` version.

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

The **router's typed choice is itself a tool call** (`route(choice)`,
kind `route`), so the one rule has no exception and the router is a live
unit test of the whole path.

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
capability token. At slice-0 strength (one host, no crypto) the grant
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
MUC provisioning and teardown are troupe's and circus's lanes.

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

## Examples

An issue-filer brief (unsigned, slice-0 shape):

    schema = 1
    principal = "3f1c…"           # minted by the spawner
    parent    = "pebble-webhook@krone"
    room      = "pebble-9f3a@rooms.xmpp.example"
    model     = "openrouter/anthropic/claude-sonnet"
    system    = "You file one issue per actionable item in the transcription…"
    task      = "<transcription text>"
    [limits]
    steps = 8
    wall_clock = "120s"
    [evaluator]
    kind = "jq"
    program = '''
      .cannot_complete == null
      and ([.calls[] | select(.kind == "issue" and .ok)] | length) >= 1
    '''
    [moxyfile]
    # narrows the spawner's moxyfile to smith's three verbs, always-allow
    …

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

The router, same mechanism, one tool:

    .cannot_complete == null
    and ([.calls[] | select(.kind == "route" and .ok)] | length) == 1

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
  `Delegate=yes` on its own unit. Which one krone uses is circus's lane.
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
| default step cap | 8 | the bullet's agents make 2–4 tool calls | real briefs routinely hit it |
| default wall clock | 120s | matches `juggler prompt`'s budget | agents time out while a tool is legitimately slow |
| evaluator kinds | `jq` only | smallest signed-contract surface | the same count-of-kind jq appears in most briefs (→ `predicate`) or a task needs judgement (→ `agent`) |
| headless permission posture | non-`always-allow` → deny | no human to ask | a moxin's tier is `ask` only because nobody set it, and agents keep failing on it |
| transcript layout | one MUC per run | one link for the fallback note; brief in the same transcript | runs grow long enough that per-agent rooms read better |
| daemon for remote models | optional | krone's webhook user has no user session | a host needs local inference for these agents (→ system-service daemon) |
| agent-scope realisation | open: sub-cgroup vs sibling unit | the bullet needs neither namespaces nor the choice | signing lands, or a non-fixed-surface tool server appears (→ sibling unit + `limits.sandbox`) |
| exit-wake emitter | the unit's `ExecStopPost` hook, sole emitter | survives every ending the main process does not | a holder needs a wake on the systemd-dead path (→ holder-side timeout or a cross-host watcher) |

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
  for; circus FDR 0023 — the earlier ephemeral-clown sketch whose
  "launch mechanism" question this answers as "juggler agent".
- FDR 0010 / FDR 0011 — the juggler daemon lineage; the daemon becomes
  optional here for remote models.
- RFC 0009 / RFC 0010 / RFC 0011 / RFC 0013 — job-wakeup channel,
  spool, job MCP tools, per-instance identity.
- RFC 0017 — dumbo mock model API, one of the two test doubles.
- `docs/plans/2026-07-11-juggler-subagent-tool-design.md` — the
  single-turn `juggler-prompt` tool this record supersedes as "the
  subagent path".
- Lanes: moxy (narrowing merge, principal → effective moxyfile), troupe
  (MUC provisioning, grant grammar, signing), circus (krone deployment,
  FDR 0023 amendment), clown (this record and `juggler run`/`spawn`).
