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

> **Erratum (2026-10-20, clown#283).** §5's *Headless posture* paragraph
> and the *headless permission posture* row of the Tuning Levers table
> stated that any moxy permission tier other than `always-allow` resolves
> to deny for a juggler agent (both now read "intended; not enforced
> today"). That is an intent, not moxy's behaviour:
> moxy's `tools/call` funnel never consults its permission resolver on a
> plain call (moxy#485; moxy's skipped test from bb446c3), so an
> `each-use`, `delegate-to-client` or `dynamic` tool a juggler agent can
> reach runs ungated, bounded only by the brief's `tools` list, the
> derived `allow-tools`, and the unit the agent runs in. The missing
> server-side enforcement is moxy#486 (an `enforce` mode; closed by
> 2026-10-24, its outcome not yet recorded here, see
> docs/plans/2026-10-24-fdr-rfc-audit.md); FDR 0021 §3
> (`docs/features/0021-headless-agent-loop-for-confined-workers.md`)
> records what moxy enforces today and where enforcement is to live.

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
   recording each call's outcome in the **ledger** and its full
   arguments in **`traffic.jsonl`** (§4, clown#255);
4. appends every turn to the run's MUC as a stanza;
5. at the end, runs the brief's **evaluator** (§4) over the ledger and
   writes the ringmaster job's terminal record (§6) with the verdict.

It is a ringmaster **producer**: `job_start` at boot, `job_progress` per
turn, `job_done` at the end. It is NOT a ringmaster-spawned process.

**`juggler spawn`** is the launcher-side glue a spawner calls instead of
assembling the pieces by hand. Before anything else, it checks systemd's
own readiness to start units (`systemctl is-system-running`, or `--user`
under `--user`); see Limitations for the accepted states and the refusal
shape. Given a brief, it then:

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
  "ledger":"<path>","traffic":"<path>","artifacts":[{"tool","kind","uris"}],
  "cannot_complete":null|{"reason"}}` where `"traffic"` is the child's
  `traffic.jsonl` (clown#255; §4), derived from the child's own
  principal AND job (`ChildRecord.Principal`, `ChildRecord.Job`) and the
  launcher's store — independently of `"ledger"`, since a hook-written
  terminal carries no `result_ref` even when the agent wrote plenty of
  traffic before it died — and reported only when the file actually
  exists (`"traffic"` is `""` otherwise, e.g. the run failed before
  anything was opened);
  `state` is the ringmaster
  state and `reason` the D6 reason (§6). Exit code mirrors the state:
  0 `succeeded`, 2 `failed`, 3 `aborted`, 4 `interrupted`, 1 usage. At
  `--timeout` the child job is asked to cancel (`ringmaster cancel`) and
  given `--stop-grace` (default 30s) to terminalize: exit 3 if it
  aborted in time, else 5 ("cancel requested, still running"), so a
  caller that falls back never races a late agent. Without `--wait`,
  the launch JSON `{"jid","job","room","moxin_path",
  "parent_moxyfile_digest"}` is printed immediately (`moxin_path`: the
  `MOXIN_PATH` the unit got; `parent_moxyfile_digest`: the sha256 of the
  parent reference its moxy enforces, §5). A brief whose moxyfile widens
  the spawner's is refused before anything is minted (exit 1, §5).
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
- **`juggler clean [--older-than <dur>] [--resolved [--resolved-older-than
  <dur>]] [--act]`** (clown#250): the retention sweep for what
  `spawn --new-run` and `resolve` deliberately leave behind. Two
  independent categories, each with its own age guard:
  1. a **pending run** (`spawn --new-run` failed after the root's mint;
     §1, §2) past `--older-than` (default 1h, measured from
     `RunRecord.pending_since` — stamped fresh on every failed attempt,
     not `created_at`, which a resume carries forward unchanged, so a
     retry policy whose total span exceeds the guard is never reaped
     between its own attempts; a pre-clown#250 record with no
     `pending_since` at all falls back to `created_at` rather than
     reading its zero value as enormously overdue): revokes the root the
     same way `resolve`'s
     own teardown does (`teardownRoot`: room affiliation dropped only
     when the room has an operator owner, account and password file
     revoked/deleted) and, once that succeeds, **tombstones** the record
     first — `pending: false, reaped: true, reaped_at` — then appends the
     `teardown` ledger entry, in that order (not the reverse): the
     tombstone is the record `spawn --new-run`'s `CheckOpen` actually
     consults, so a crash between the two leaves the run key correctly
     refused either way, never a window where the ledger already claims
     the root gone while the record itself could still resume and
     re-mint. A tombstone, not a delete: the root principal is
     deterministic (`NewRootPrincipal(run_key)`), so deleting the record
     would let a later `spawn --new-run` with the same key re-mint a
     brand-new account under that same principal — one the ledger's
     already-ok `teardown` entry marks gone, so `resolve`'s teardown
     would skip revoking the NEW account and it would leak forever.
     `spawn --new-run` refuses a reaped key outright, naming the
     tombstone.
  2. with `--resolved`, a **resolved run** past `--resolved-older-than`
     (default 30d — a retention policy on audit material, not a retry
     guard, so off unless asked for and measured from
     `Resolution.at`): sweeps each TERMINAL child's staged
     `<digest>.brief.toml`/`.moxyfile.toml`/`.parent-moxyfile.toml` and
     `traffic.jsonl` (clown#255) — never an account (`resolve`'s job) or
     the run/child record itself (the audit trail). A child still
     running, OR one whose journal could not even be read, is skipped
     entirely (the identical `Ringmaster.Records`/terminal check
     `resolve`'s own teardown uses, including its keep-on-error
     handling: a Records error means "don't know" and is never read as
     "gone" — a `clean --act --resolved` running while ringmaster is
     down must never unlink a live agent's `traffic.jsonl` out from
     under it), so its files are never swept and `reaped` reports
     `false` for the run, not a bare success with files quietly left
     behind.

  A run that is neither pending nor resolved — still open — is never
  touched, however old either guard. Defaults to a dry-run listing;
  `--act` performs the reap and exits non-zero if any listed run could
  not be fully reaped. Needs the minter credential exactly when
  `resolve` does, whenever `--act` is given (`troupe mint-revoke`'s
  self-removal path for the pending-run category; the resolved-run sweep
  touches no account but the check does not distinguish). A run with
  nothing left to report (every file already gone or never existed, and
  no child was skipped as still-running or unreadable) is omitted from
  the output, so a periodic sweep's listing does not grow forever.
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
| `env` | string→string table passed into the agent's unit environment; how per-agent tool configuration (e.g. a target mode) reaches moxy and its moxins, which moxyfile(5) cannot carry. `CLOWN_SESSION_ID`, `TROUPE_XMPP_*`, `TROUPE_MINT_*` (the spawner's minter credential), `MOXIN_PATH` and `MOXY_*` (moxin identity, pinned by the launcher, §5), `JUGGLER_*` and every launcher-owned key (table below) are reserved and rejected at parse, naming the key and why. `juggler spawn` also refuses, before minting anything, a key the launcher already sets in the unit (any passthrough variable — `PATH`, `HOME`, `XDG_STATE_HOME`, `XDG_RUNTIME_DIR`, `JUGGLER_MODELS_PATH`, the `JUGGLER_*_BIN` overrides, `TROUPE_TRANSPORT`, `TROUPE_XMPP_HOST`/`PORT`/`INSECURE` — whether or not this spawner has it set): a brief only ever **adds** new variables, never overrides the launcher's |
| `tools` | REQUIRED allowlist of exact tool names as moxy advertises them (`<server>_<tool>`); `juggler run` offers the model only these. A listed name moxy does not advertise is a startup error; the list may not be empty |
| `evaluator` | `{kind, program, stop_on_pass}`; first kind is `jq` (§4). `stop_on_pass` (default false) ends the run with `evaluator_pass` as soon as the program first passes after a successful tool result (§4) |
| `limits` | `{steps, wall_clock, sandbox}`; `sandbox` is RESERVED and unused in this slice (§9) |

**Launcher-owned environment** (`jugglerbrief.LauncherOwnedEnv`; a
trailing `*` is a prefix). These decide which binary a bare-name, `~/`-
or `$VAR`-anchored moxy server command resolves to, or inject code or
credentials into it, so the launcher's value is the only one moxy and its
tool servers may see:

| Key | Why the launcher owns it |
|---|---|
| `PATH` | resolves bare-name server commands |
| `HOME` | anchors ~/ paths and moxy's moxyfile hierarchy |
| `PWD` | anchors relative paths |
| `OLDPWD` | anchors relative paths |
| `SHELL` | selects the interpreter tools shell out to |
| `USER` | names the account tools act as |
| `LOGNAME` | names the account tools act as |
| `TMPDIR` | places temporary files tools create |
| `TMP` | places temporary files tools create |
| `TEMP` | places temporary files tools create |
| `XDG_*` | anchors config, data, state and runtime paths |
| `NIX_*` | reconfigures nix (substituters, paths, profiles) |
| `LD_*` | injects code into every dynamically linked process |
| `DYLD_*` | injects code into every dynamically linked process |
| `SSH_*` | carries SSH credentials (SSH_AUTH_SOCK) and options |
| `GIT_*` | redirects git's repository, config and commands |

The unit's `PATH` and `HOME` are the spawner's, passed explicitly
(`--setenv`): `systemd-run` only adds to the service manager's own
environment, whose `PATH` is the manager's (a compile-time default, or
whatever `environment.d`/`import-environment` put there), not the
spawner's, and whose `HOME` is set only for user-manager units. `juggler
run` additionally drops from moxy's environment any reserved key the
brief names (fail closed), so even a relaxed parse check could not reach
moxy's `~`/`$VAR` expansion or `PATH` lookup.

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
top-level-field rule (`uri`/`url`, `uris`/`urls`), each URI once. One
rule keeps lookups out of that list (clown#253): a call whose declared
kind is read-only — `list`, `search`, `read`, `get` or `query` — creates
nothing, so its URIs are not extracted at all (every other kind
extracts, as does a tool that declares none). Read-only tools MUST
therefore declare a read-only kind; an undeclared lookup's URIs count as
artifacts. URIs are not filtered by scheme: moxy blobs any result over
its inline threshold, so a writer's `madder://` link may hold, or be, the
call's product (a large create result, a capture receipt) and is kept —
it at least marks the call as having produced something and points at
where the product is.

The ledger does not carry a call's **arguments** — circus's next agent
graphs (circus FDR-0042) need them after the run (attestation agents
whose findings exist only as a tool call's arguments; a coverage audit
checking an agent actually opened the files its brief named), but the
evaluator's input and the future signed contract must not grow with
file bodies (clown#255). Instead, `juggler run` writes a per-run
**`traffic.jsonl`** keyed by BOTH the agent's principal and its
ringmaster job (`<store>/traffic/<principal>/<job>.jsonl`).
Keying by job, not principal alone, matters because a bare `run
--brief` with no `--job` starts a **fresh job per invocation** under
the **same** principal: keying by principal alone would let a second
run of the same brief silently overwrite the first run's file
underneath an already-printed path (delta review finding 1). The path
also deliberately sits outside `agents/<principal>/`, which is moxy's
own CWD: folio-style tools auto-allow writes inside CWD, so a traffic
file living there would be reachable — readable, deletable, replaceable
— by the very model turn whose calls it exists to audit (delta review
finding 2). Moving it outside that one CWD is not an absolute
guarantee on its own: a moxyfile granting a tool writes beyond its CWD,
or a shell-capable tool, can still reach anywhere under
`$XDG_STATE_HOME` (which is in moxy's own environment) — the real
protection is the moxyfile's own policy, this placement only removes
the CWD-scoped default that would otherwise expose it regardless of
that policy. (An earlier revision instead put the file beside the
ringmaster spool, derived from the spool path; a real ringmaster
discovers journals by `*.jsonl` suffix and read a sibling
`<job>.traffic.jsonl` as a phantom second job — scanned by its monitor,
listed by `ringmaster ls`, and deleted by its age GC independently of
the real job. First-round review finding 1.) One JSON line per tool
call, opened (truncating any stale content from an earlier run of the
**same job** — a retried adopted `--job`, never a fresh one, which
always gets its own file) right after the job's lock is acquired, and
written — each line flushed as its own `Write`, durable immediately —
at the same point its ledger entry is appended in memory: including a
read-only kind, a call whose arguments failed validation, and one
whose executor returned a transport error. Because of this, traffic
and the ledger are NOT equally durable: a `SIGKILL` mid-run leaves
every traffic line written so far on disk but no ledger at all (the
ledger only reaches disk once, at `finish`, via the result spool) —
traffic can therefore show calls a ledger reader never learns
happened. Each line carries the call's step index, the job id (so a
line is self-identifying even copied out of its directory — the loop
itself has no notion of a job and leaves this field empty; the writer
that owns the path stamps it), tool name, its **full** arguments as
sent, `args_digest` (see below), the result's size in bytes and ok
flag (never the result body itself — moxy already blobs an oversized
result, and the ledger carries the URI), and a timestamp. It is
append-only within one run and never read by the evaluator; `juggler
clean --resolved` (clown#250) reaps a resolved run's traffic files once
it is past the sweep's age guard (opt-in: §1's `juggler clean` entry) —
`agents/<principal>/` itself still has no sweep. Each ledger
call entry in turn gains **`args_digest`**: a sha256, hex-encoded, over
the arguments' canonical JSON encoding (object keys sorted, no
insignificant whitespace, numbers kept as their original text, HTML
escaping disabled — `jugglerloop.CanonicalJSON`/`ArgsDigest`; the
canonical form is precisely Go's `encoding/json` encoder output under
that configuration, not a cross-language guarantee), so a ledger entry
and its traffic.jsonl line are joinable, and a traffic file can be
checked against its ledger, without the ledger itself carrying the
arguments. A failure to open the file, or a write failure mid-run, is
recorded as a ledger note (not only logged), so an auditor can tell a
run's traffic is incomplete from the ledger alone.

Redaction and sensitivity: a tool's arguments are whatever the model
sent and the executor received — a model API token or key is never
among them, since those live in the *model* request (the HTTP call to
the provider), a wire `traffic.jsonl` does not touch at all; model
request and response bodies are not captured anywhere by this feature.
That said, `traffic.jsonl` is exactly as sensitive as the arguments
themselves: whatever a tool's args legitimately carry — including a
secret the model echoes into them (a credential it was told to use, a
pasted token) — lands in the file verbatim, with no filtering. A tool
result is recorded only as the loop saw it (size and ok, a `madder://`
URI if moxy already spilled it), never its body.

The
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
not stop the run. When the check passes mid-reply, the reply's remaining
tool calls are not executed and get no tool_result and no ledger entry:
the transcript shows them requested and unanswered, exactly as after
`cannot_complete` or a `tool_error`. The step cap and `cannot_complete`
are unchanged. The per-call check runs under its own 2 s evaluator
budget, not the run's wall clock, so a run whose pass coincides with its
deadline can end `evaluator_pass` up to ~2 s past it; a holder's cancel
(observed via the job's cancel-requested record) is unaffected. A tool call whose arguments are not valid
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

- checked **launcher-side**, before anything is minted: `juggler spawn
  --brief` renders the spawner's effective moxyfile (`moxy
  show-effective-moxyfile --dir <spawner cwd>`, in the spawner's
  environment minus its principal (`CLOWN_SESSION_ID`, `TROUPE_XMPP_*`,
  `TROUPE_MINT_*`) and with its pinned `MOXIN_PATH`) into the child's staging
  directory as `<digest>.parent-moxyfile.toml` (0600) — the **parent
  reference** — and runs `moxy narrows --parent <it> --child <the brief's
  moxyfile>`. Exit 1 refuses the spawn with moxy's violation lines
  verbatim and a run-ledger entry `{tool:"narrows", kind:"refusal",
  ok:false, violations:[{key,subject,message}], moxy_bin,
  parent_moxyfile_digest}` — `violations_missing:true` instead of
  `violations` when moxy's `--json` form was unavailable; exit 2, a failed
  render or a missing moxy binary fail the spawn too (a broken check
  never passes). The child record carries the parent reference's path,
  `parent_moxyfile_digest` (sha256, also in the launch output) and the
  `moxy_bin` used;
- enforced **moxy-side**, authoritatively: the unit gets
  `MOXY_PARENT_MOXYFILE=<the parent reference>`, so the agent's `moxy
  serve-http` refuses to start — and re-checks on reload — when its
  effective configuration (the whole moxyfile hierarchy, not just the
  brief's file) widens the reference.

Caveats. Parent and child must be served by the **same moxy build**: the
parent reference names the system moxin directory by store path. A
**hand-written** parent reference has no `[resolved-moxins]`, and moxins
are then not compared. By moxy's **shadowing rule**, a brief that omits a
`[[servers]]` entry the spawner defines must list that name in
`disable-moxins`, or the check reports it. moxy's narrowing allows bare
PATH names and `~/` or `$VAR`-anchored commands because the launcher owns
the environment; juggler guarantees that by reserving these keys (§3's
launcher-owned table) and refusing brief overrides of launcher-set
variables.

**`tools` and `allow-tools`.** The brief's `tools` allowlist stays: it is
what `juggler run` offers the model. The moxyfile's `allow-tools`
(moxy's own allowlist: a bare name is a whole server or moxin, a dotted
`server.tool` one tool; layers intersect; a parent that sets it forces
the child to) is what the narrowing check covers. When a brief's
moxyfile sets `allow-tools`, every `tools` entry must be covered by it
(`<server>_<tool>` ↔ `server.tool` under the `{server}_{tool}` name
template), checked at brief parse; when it does not, `tools` is
unchecked against it.

**`tools` is enforced, not just advertised (clown#261).** The loop
offers the model exactly `tools`, and refuses, at call time, any name
the model emits that is not in it — a hallucinated or prompt-injected
call gets a tool_result error and the run continues, never reaching
the executor. moxy's six own builtin meta tools (bare names under any
serving template: `restart`, `batch`, `async`, `async-result`,
`async-cancel`, `status`) may never appear in `tools`, nor in the
brief's own moxyfile `allow-tools` or `pass-tools` (`Brief.Validate`
refuses all three): `restart` could reload a rewritten moxyfile,
`batch`/`async` let a nested sub-call bypass this same filter (bounded
only by moxy's own `allow-tools`), and passing any of the six to a
non-juggler grandchild would reopen the same risk one level down.

**`allow-tools` is always derived from `tools` (clown#261), not merely
recommended.** The agent's own moxyfile always carries `allow-tools`
set to the canonical `server.tool` form of `tools` (resolved via `moxy
render --resolve`, independent of the `{server}_{tool}` serving
template — writing the rendered juggler name there directly would be
read as a bare, nonexistent server, silently allowing nothing),
**always overriding** whatever the brief's own moxyfile set for
`allow-tools`: `Brief.Validate` already guarantees a brief-set
`allow-tools` covers every `tools` entry, so overriding is never wider
than what the brief itself claimed. Every other key — `pass-tools`
above all, also `disable-moxins`, `[[servers]]`, `[credentials]`, ... —
is kept as the brief wrote it; a `pass-tools` entry that would collide
with the derived `allow-tools` is refused at parse (moxy#459: a tool
is held at one level, use or pass). A `tools` entry `moxy render`
cannot resolve, or whose real resolution the brief's own `allow-tools`
(when set) does not actually cover, refuses the spawn before anything
is minted, naming the tool — the latter catches a case
`checkToolsCoveredByAllowTools`'s own textual heuristic can miss: moxy's
structural inverse always splits a rendered name on the FIRST
separator, which does not always agree with that heuristic for a
`tools` entry with more than one underscore. (The six reserved meta
tools above are refused outright by `Brief.Validate`, not left to the
accident of `moxy render` failing on a name with no separator —
`async-result` reads the clown job journal across sessions and
`status` discloses the whole moxyfile hierarchy, so both are refused
for their own sake too, not only as a side effect of the others.)

**`pass-tools` (moxy#459).** moxy holds every tool at one of three
levels, in order: `none`, `pass`, `use`. `allow-tools` names the tools
held at `use` (servable, callable); `pass-tools` names a tool held at
`pass` — conferrable on a child but never itself callable by the
holder, moxy's analogue of AWS IAM `PassRole`. The narrowing check's
union covers both: `moxy narrows` requires `child.allow-tools ⊆
parent.allow-tools ∪ parent.pass-tools` and `child.pass-tools ⊆
parent.allow-tools ∪ parent.pass-tools`, so a brief may legitimately set
its own `allow-tools` to a tool the spawner holds only at `pass` — the
one upward step (`pass` → `use`) the relation permits, pre-approved when
the spawner's own moxyfile was written. A violation names the tier:
`allow-tools: child holds <tool> at use, parent at none` / `pass-tools:
child holds <tool> at pass, parent at none`. When the parent has an
allowlist and the child sets none at all, the violation is instead
`allow-tools: parent sets an allowlist, child does not` — unchanged by
pass-tools (a parent with `pass-tools` always has an allowlist, so its
child always sets one too, if only `[]` beside the tools it passes on).

**Escalation note (moxy#461): three paths, stated precisely.** Before
moxy cc75d1d (see the end of this note), a session able to rewrite its own
moxyfile and then call moxy's own `restart` tool could widen its own tool
access, because moxy's reload check validated only against the *parent*
reference pinned at spawn. The agent's own moxyfile IS writable by
itself today — it is written to `<AgentStateDir>/moxyfile`
(`internal/jugglerrun/toolplane.go`), which is moxy's own CWD, and
`AgentStateDir`'s own doc comment (`internal/jugglerrun/store.go`)
says plainly that "folio-style tools auto-allow writes inside CWD" —
so an agent holding an ordinary file-write tool can rewrite it and
trigger a moxy reload that widens moxy's own permissions up to the
spawner's whole set. Whether that reaches `restart` depends on how the
call would actually get there:

- **Model-emitted calls** — the gap this moxy#461 escalation originally
  relied on for juggler specifically: a brief's `tools` was *not* a
  call-time gate, only advertisement (`FilterToolsToAllowlist`,
  `internal/jugglerrun/agent.go`), while the loop forwarded whatever
  name the model emitted straight to moxy
  (`internal/jugglerloop/loop.go`) with no re-check. Closed by clown#261:
  the loop refuses any call for a name outside `tools`, and
  `Brief.Validate` refuses `restart` from ever appearing in `tools` or
  the brief's own `allow-tools`/`pass-tools` at all — `restart` can
  never be a name the model is offered, so the loop refuses the call
  regardless of what moxy itself would allow after a reload.
- **Out-of-band loopback** — an agent holding any HTTP-fetch or exec
  tool can rewrite its moxyfile, trigger the reload, and then POST
  directly to its own moxy's `/mcp` on `127.0.0.1`, skipping the loop
  (and so `tools`) entirely. This is closed only by the always-derived
  `allow-tools` (clown#261 point 3): moxy's own tool-call funnel
  enforces `allow-tools` — including for a builtin, by its bare name —
  against every caller regardless of transport (moxy's
  `internal/proxy/proxy.go`, the `callToolV1` allow-tools check), so a
  moxy whose effective `allow-tools` never includes `restart` refuses
  the loopback call too. And only when juggler itself launches moxy:
  the always-derived `allow-tools` is written only in the
  `deps.Moxy.Launch` path (agent.go).
- **`--moxy-url`** — when `juggler run` is pointed at an already-running
  MCP upstream instead of launching moxy itself, no `allow-tools` is
  derived at all (`DeriveAgentMoxyfile` is never called in this path):
  the external moxy's own configuration decides, entirely outside
  clown#261's reach.

**moxy closed the pass-to-use lift on reload at cc75d1d (moxy#461,
closed).** A `restart` reload is now judged by `narrowing.CheckReload`.
It refuses to raise a tool the parent reference holds at pass into the
session's `allow-tools`, unless that tool was already in `allow-tools`
when the moxy process started; moxy pins that set on its first serving
check. Use-level widening by moxyfile edit plus `restart` is unchanged.
Two limits, read from that diff:
- The bound applies only to tools the parent holds at pass.
- It applies only when the process started with `allow-tools` set. A
  moxy started with `allow-tools` unset (unrestricted) has no baseline to
  bound against.

juggler's own moxy always starts with a derived `allow-tools` (clown#261),
so it gets the bound. The juggler-specific layers above (the loop filter,
Validate's reserved builtins, the derived `allow-tools`) still apply. A
juggler agent benefits only where the host's moxy is at or after cc75d1d;
the krone fleet ran 11521ef when clown#262 was filed, which predates both
pass-tools and this fix.

Headless posture (intended; **not enforced today**, see the erratum at
the top, moxy#485/moxy#486): any moxy permission tier other than
`always-allow` should resolve to **deny** for a juggler agent; the agent
would see the denial as a tool error it can route around or
`cannot_complete` on.

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
in the moxyfile: moxyfile(5) has no environment key and cannot configure a
moxin. `juggler spawn` passes `env` into the unit, and moxy and its moxins
inherit it. (An earlier draft said "inside the moxyfile as the servers'
arguments or environment"; that is only true for `[[servers]]` entries,
not moxins, and is withdrawn.)

**Moxin identity is pinned by the launcher: `MOXIN_PATH` is inherited
from the spawner, never from the brief.** A moxin's permission tiers are
per-tool `perms-request` keys in the moxin's own TOML, found through
`MOXIN_PATH` (plus moxy's system moxin dir), not in the moxyfile — so the
only way an agent's toolset could widen past its spawner's is its moxy
discovering different moxins. `juggler spawn` therefore sets the unit's
`MOXIN_PATH` to the spawner's own value with every entry made absolute
(against the spawner's working directory), or leaves it unset when the
spawner's is unset; no `MOXY_*` variable reaches the unit but the
`MOXY_PARENT_MOXYFILE` juggler sets itself. `MOXIN_PATH`,
`MOXY_*` (`MOXY_PARENT_MOXYFILE`, which juggler sets itself to the
parent reference, among them) are reserved in a brief's `[env]`.
The value the unit got is the child record's and the launch output's
`moxin_path`. A spawner whose tools are moxins (circus's wrapper tools)
must therefore carry their directory on its own `MOXIN_PATH`. With
`MOXIN_PATH` unset, moxy falls back to its legacy hierarchy relative to
the agent's state directory (`~/.config/moxy/moxins` of the same user plus
`.moxy/moxins` dirs on the way down, which juggler never creates) — the
same user's global moxins, not a brief's choice.

**Allowlist, not deny-list.** moxyfile narrowing is additive deny-lists
(`disable-moxins`, `disable-servers`), which fail OPEN when a tool is
added to a moxin later. The brief's REQUIRED `tools` allowlist, enforced
by `juggler run` both in what it offers the model and, at call time, in
what it will actually execute (clown#261), is what makes "anything not
explicitly allowed is denied" true for the first bullet; the moxy-side
narrowing check and the always-derived `allow-tools` (above) are the
second, authoritative layer.

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
what `signal` covers. **systemd's `ExecStopPost` hook is the backstop
for a juggler unit** — it runs whenever the unit stops, with or without
anything watching the job's channel, and writes the terminal the main
process left none. **Ringmaster's host-wide liveness reaper
(ringmaster#33, landed) is the backstop FOR the hook**: any long-running
`ringmaster monitor` or the standalone `ringmaster reap` scans every
channel on the host, not only the one it is itself a consumer on, so
`juggler spawn`/`juggler run` launch no monitor of their own. A job
whose lock is released without a terminal record (the hook also never
ran: systemd itself gone) is reaped by the host-wide reaper (any
`ringmaster monitor` or `ringmaster reap` on the host), one grace
period after the first reaper observes the released lock, including
after a reboot — no D6 wake is sent on that path. A host running
neither a `ringmaster monitor` nor a `ringmaster reap` unit still has
the gap the hook exists to cover; deploying one (e.g. a `ringmaster
reap` systemd unit the host deploys) is the host's own responsibility —
see Limitations.

### 7. Transcript — one MUC per run

A run (webhook → router → issue-filer → maybe a note-filer agent) has
**one MUC**, created by the spawner and named for the run. The spawner's
first act is dropping the brief into it. Every agent in the run joins
with its own JID and posts its turns there. Each principal joins under
its own session key as nick: juggler overrides `TROUPE_XMPP_NICK` per
principal (an inherited clown-session nick would make every principal
join under one nick from different bare JIDs, a XEP-0045 409 conflict),
and agent units never inherit it. The room's MAM is the run
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
4. `juggler clean` (§1, clown#250) does the same step-down for a
   **pending** run's root — a `spawn --new-run` that failed after the
   mint and was never retried past the age guard — via the same
   `teardownRoot` helper `resolve` uses in step 3: the room itself is
   never destroyed, only the root's owner affiliation dropped, and only
   when the room has an operator owner to begin with.

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

The ledger the evaluator sees (`args_digest` joins each call to its line
in the run's `traffic.jsonl`, below):

    {
      "schema": 1,
      "calls": [
        {"tool": "list_repos",   "kind": null,    "ok": true,  "uris": [],
         "args_digest": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
        {"tool": "create_issue", "kind": "issue", "ok": true,
         "uris": ["https://code.example/clown/issues/301"],
         "args_digest": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"}
      ],
      "end": {"reason": "end_turn"},
      "cannot_complete": null,
      "steps": 3,
      "elapsed_ms": 18340
    }

The same run's `traffic.jsonl`, keyed by principal AND job
(`traffic/<principal>/job-7a21.jsonl` — neither beside the ledger nor
inside the moxy-CWD `agents/<principal>/`; see §4), never read by the
evaluator:

    {"step":1,"job":"job-7a21","tool":"list_repos","args":{},"args_digest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","result_size":42,"ok":true,"ts":"2026-10-13T14:40:01Z"}
    {"step":3,"job":"job-7a21","tool":"create_issue","args":{"title":"restart tool should reconnect automatically"},"args_digest":"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210","result_size":71,"ok":true,"ts":"2026-10-13T14:40:04Z"}

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
    loss, not safety: a dead agent cannot act. After a reboot, the first
    reaper to come back up on the host — a long-running `ringmaster
    monitor` or `ringmaster reap` — reaps each open job `interrupted` one
    grace period later (ringmaster#33, landed: the lock lives in the
    persistent state directory, not anything reboot-cleared, and the
    reaper finds every lock file under the jobs root, not only channels a
    monitor happens to be serving); still no D6 wake on that path.
  - *systemd degraded* (refuses transient units, D-Bus/polkit down,
    delegation broken): `juggler spawn` fails synchronously before an
    agent exists and the fallback runs. A legible stop. `juggler spawn`
    checks `systemctl is-system-running` (`--user` under `--user`) as a
    precondition, before the moxyfile narrowing check and before any
    principal is minted: `running` and `degraded` (an unrelated unit
    failed; the manager still starts units) proceed, anything else —
    `starting`, `initializing`, `maintenance`, `stopping`, `offline`,
    `unknown`, or a non-zero exit with no recognized state — refuses the
    spawn (exit 1, `juggler: spawn: systemd is not ready to start units:
    is-system-running reported "<state>"`). When systemctl's exit carries
    no state at all — D-Bus or polkit down, no user bus under `--user` —
    the state is normalized to `"unknown"` but the refusal's `detail`
    carries systemctl's own exit code and stderr, so that case is never
    indistinguishable from systemd genuinely answering `unknown`
    (message: `is-system-running reported no state (systemctl exit
    <n>: <stderr>)`). A `spawn --brief` refusal is recorded in the run
    ledger the same way a widening brief is
    (`{tool:"systemd",kind:"refusal",ok:false,reason,state,detail}`,
    `detail` only when no state was read), auditable without a child
    record; `spawn --new-run` has no run yet to record one against, so
    its refusal is a plain error. Internally the check shells out through
    the same `runCommand`/`Command` seam (`internal/jugglerrun/exec.go`)
    `moxy narrows` and `systemd-run` use, behind a small `SystemdChecker`
    interface, so it is faked and tested the same way. Two caveats of
    checking systemctl at all: `spawn --new-run` previously needed no
    systemd and now fails wherever `systemctl` is unavailable (darwin:
    `SystemctlPath` is empty and the bare name is not found; any other
    host without systemd); and the check follows `--user` exactly as
    `systemd-run` does, so a glue that calls `--new-run` without `--user`
    but spawns children with `--user` checks the *system* manager for the
    run while checking the *user* manager for each child — `--user`
    otherwise means nothing to `--new-run`, which starts no unit itself.
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
- **A failed `--new-run` that is never retried leaks its root, unless
  `juggler clean` (clown#250) reaps it.** To make the retry resumable
  (§1: the root is derived from the run key and kept on failure), a
  `--new-run` that fails after the mint leaves the root's account, its
  password file, a pending run record and possibly the room (clean
  drops its owner affiliation there, never destroys it — see "Per-run
  rooms accumulate" above). Past clean's age guard (`--older-than`,
  measured from `pending_since` — stamped fresh on every failed
  attempt, including a resuming retry's own stamp taken before the
  systemd check or the mint, before either can fail and return bare, so
  a retry policy's own backoff is never mistaken for staleness; an
  in-flight retry is never reaped, since `spawn --new-run` and `clean`
  serialise on the same per-run lock AND clean re-checks the age guard
  against a freshly reloaded record once it holds that lock, not just
  the listing-time snapshot that queued it for reaping), clean revokes
  the root (`teardownRoot`, the same helper `resolve` uses) and, once
  that succeeds, **tombstones** the record — `pending: false, reaped:
  true, reaped_at` — first, THEN appends the `teardown` ledger entry
  (not the reverse): the tombstone is what `spawn --new-run`'s
  `CheckOpen` actually consults, so a crash between the two always
  leaves the run key correctly refused either way, never a window
  where the ledger claims the root gone while the record could still
  resume. (A tombstone saved right before a ledger write that then
  fails loses that one teardown's audit entry permanently — nothing
  retries it, since the tombstone already refuses any resume — but
  stays safe: the account really is revoked and the key really is
  retired, just not perfectly audited, and the one `clean --act` that
  hit it exits non-zero to surface the gap.) A tombstone, not a
  delete: because the
  root is a function of the run key, deleting the record outright would
  let a later `--new-run` with the same key re-mint a brand-new account
  under that same deterministic principal — one the ledger's existing ok
  `teardown` entry already marks gone, so a later `resolve` would skip
  revoking the NEW account and it would leak forever. `spawn --new-run`
  therefore refuses a reaped key outright, naming the tombstone: a run
  key, once reaped, is retired for good, never reused. Before clean
  existed, nothing revoked an abandoned pending run; now a host running
  it periodically (`--act`; the default is a dry-run listing) closes
  that gap. The pending record drops the failed attempt's run job
  id: if failing that job during the undo itself fails, the job stays open
  and the resumed run opens a second one.
- **Parent and child need the same moxy build.** The parent reference
  names moxy's system moxin directory by store path, so the moxy `juggler
  spawn` renders it with and the moxy the unit serves with must be the
  same build. juggler pins it: the unit runs `juggler run … --moxy
  <moxy_bin>`, the absolute path of the binary that ran the check (also in
  the child record), so the agent never resolves a different moxy of its
  own — one predating `MOXY_PARENT_MOXYFILE` would otherwise silently
  ignore it.
- **A child can rewrite its own parent reference before moxy starts.**
  The unit runs as the spawner's uid, and the reference file is that
  user's. moxy pins the reference at startup and judges every reload
  against the pinned copy, so the exposure is only a reference rewritten
  before the agent's moxy starts — negligible today (the agent process
  has run no model turn yet), to be closed by clown#252 (open; a DynamicUser or
  per-run user) or by handing moxy the recorded digest to verify.
- **Staged files persist per child until `juggler clean --resolved`
  sweeps them.** `<digest>.brief.toml`, `<digest>.moxyfile.toml` and
  `<digest>.parent-moxyfile.toml` stay in the run's `children/`
  directory after the run; clean (clown#250), given `--resolved`,
  removes a TERMINAL child's once the run is resolved and past
  `--resolved-older-than`'s guard — never a still-running child's (the
  same terminal check `resolve`'s own teardown uses), and never the
  child's own record (the audit trail). Off by default: it is a
  retention policy, not a retry guard, so a bare `clean --act` sweeps
  only pending runs.
- **`traffic.jsonl` is as sensitive as the tool arguments it records, in
  full, and is not filtered or redacted (clown#255).** Whatever secret a
  model's tool call carries — a token pasted into a search query, a
  password typed into a form-filling call — lands verbatim in the
  run's traffic file. This is a property of the feature (arguments are
  the whole point), not a bug, but it means `traffic/` needs the same
  access controls as the rest of the store. Model request/response
  bodies themselves are not captured by this feature at all — only the
  tool calls a loop makes against moxy.
- **`juggler clean --resolved` (clown#250) sweeps `traffic/`;
  `agents/<principal>/` still has no sweep.** A terminal child's traffic
  file is reaped once its run is resolved and past
  `--resolved-older-than`'s guard — the same opt-in sweep that reaps a
  resolved run's staged files above, skipping a still-running child's.
  `agents/<principal>/` itself is still never swept; that gap
  (clown#255) remains.
- **A hand-written parent reference skips the moxin comparison.** Only
  a rendered reference carries `[resolved-moxins]`; juggler always
  renders one, but a reference written by hand (or by another launcher)
  compares servers and keys only.
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
- **`juggler run` holds ringmaster's advisory lock for the run's whole
  lifetime (clown#256), backed by both the exit-wake hook and
  ringmaster's host-wide liveness reaper (ringmaster#33, landed).**
  `juggler run` takes the job's advisory lock (`jobwake.AcquireJobLock`)
  right after the job exists — self-started or adopted from `juggler
  spawn` — and releases it only after `Done` has returned, never earlier
  (RFC-0018 §2.3: releasing before the terminal is durable invites a
  reaper to write `interrupted` over an outcome about to be recorded).
  **systemd's `ExecStopPost` hook (`juggler exit-wake`) is the
  first-line backstop**, for every juggler unit, with or without
  anything watching the job's channel (§6's table). **Ringmaster's
  host-wide liveness reaper is the backstop FOR the hook**: any
  long-running `ringmaster monitor` or the standalone `ringmaster reap`
  scans every channel on the host, not only the one it is itself a
  consumer on, so `juggler spawn`/`juggler run` launch no monitor of
  their own. A job whose lock is released without a terminal record
  (the hook also never ran: systemd itself gone) is reaped
  `interrupted` (message `producer died without writing a terminal
  record`, no `killed:`/`crash:` prefix, so `ExitReasonFor` reports
  `crash`) by the host-wide reaper — one grace period after the first
  reaper observes the released lock, including after a reboot. A host
  running neither a `ringmaster monitor` nor a `ringmaster reap` unit
  still has the gap the hook exists to cover; deploying one (e.g. a
  `ringmaster reap` systemd unit the host deploys) is the host's own
  responsibility. A losing race against a reaper that IS
  running (or any other writer) — on `juggler run`'s own `Done` call in
  `finish`, or the exit-wake hook's — is ringmaster's exit 3
  (`jobwake.ErrAlreadyTerminal`): both treat it as success, relaying
  whichever terminal actually landed, never logging it as a failure.
  A lock already held by a live producer (`jobwake.ErrAlreadyLocked`)
  refuses the run outright rather than acquiring none: a duplicate
  `juggler run --job X` would otherwise do full tool-use work concurrently
  with the real producer and race its `Done` call. The refusal only
  catches a duplicate that loses the race for the lock: if an operator's
  manual `juggler run --job X` instead *wins* it before the unit's own
  process starts, the unit's process then loses, exits 1, and its own
  `ExecStopPost` exit-wake writes an `interrupted` (reason crash) terminal
  over the manual run's live, in-progress work — the exact clobber the refusal
  exists to prevent, just from the other side. Rare (it needs a human
  racing the unit's own startup), not handled in code; avoid it
  operationally. Any other acquire
  failure (e.g. the lock directory is unwritable) is best-effort, not
  fatal: the agent still does its work, but records the loss of the
  backstop in the run's ledger `notes` (not only stderr), so an auditor
  can see a run had none. On an adopted job (`--job <id>`, no `Start`
  call to run the protocol check first) the check runs explicitly before
  `AcquireJobLock`, so the lock is never taken ahead of knowing ringmaster
  is protocol-compatible. Resolve's own
  handling of a child or run job is unchanged — `stopLiveChildren` already
  treats any terminal state (including a reaper-written `interrupted`)
  identically to one the hook or the agent wrote, skipping it as
  nothing-to-stop. Only `juggler run`'s own agent job is locked this way;
  the run-level job `spawn --new-run` starts, and the `Start` calls
  `spawn --brief` makes on a child's behalf before the unit exists, hold
  no lock of their own.
- **ringmaster must speak exactly protocol 3** (not "3 or later": a
  future protocol 4 is refused too) for the liveness reaper, its exit 3
  (`jobwake.ErrAlreadyTerminal`), and `ringmaster version --protocol`;
  `juggler` exact-matches the installed `ringmaster`'s reported protocol
  integer against the `jobwake.ProtocolVersion` it was built against
  (clown#256), refusing loudly on any mismatch (both versions named)
  rather than operating against a journal layout or cancel/wait state
  machine it no longer agrees with. The check runs lazily — once per
  process, right before the first real job-platform call through
  `ExecRingmaster` (a `ProtocolCheckedRingmaster` wrapper), so a usage or
  parse-time refusal that never reaches the platform costs nothing; on an
  adopted job (`--job <id>`, which skips the `Start` call that would
  otherwise trigger it) `juggler run` runs the check explicitly first,
  before `AcquireJobLock`. The one exception is `juggler exit-wake`: as
  the terminal writer of last resort it never refuses over a mismatch —
  it runs the same check non-fatally, under its own 5-second timeout, and
  only warns on stderr if it disagrees, since a real mismatch is already
  caught loudly at spawn or run time, when the job was created. Protocol
  ≥ 2 (superseded by the exact-3 requirement above) was for
  `aborted`, `cancel-requested`, `cancel` and `wait --on-cancel`; an older
  installed ringmaster (which spelled the state `cancelled`) will not
  interoperate. **troupe ≥ 4c52b3b is required** for `mint
  --password-file`, `mint-revoke --password-file`, `muc create` and `muc
  affiliate`; an older troupe rejects the flag and every mint fails. Both
  binaries are resolved by flag, then `JUGGLER_{RINGMASTER,TROUPE}_BIN`,
  then the burned-in nix path (`cmd/juggler/buildcfg.go`, clown#246,
  closed), then the bare name on PATH; the burned-in paths must satisfy
  both minimums.
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
- **Toolset granularity** (resolved): a brief's `tools` is enforced per tool
  by the loop and by the derived `allow-tools` (clown#261, closed;
  `internal/jugglerrun/agentmoxyfile.go`).
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
| brief `allow-tools` | not required; when set, it must cover every `tools` entry (and is overridden anyway, when juggler launches moxy itself — not `--moxy-url` — by the derived `allow-tools`, clown#261; the brief's own setting also gets a second check then, that the real resolved `server.tool` is covered by it too, `ErrToolCoverageMismatch`). A brief may also set `pass-tools` (moxy#459) to confer a tool it does not call itself; both are checked against the spawner's `allow-tools ∪ pass-tools` union | `allow-tools` is moxy's own allowlist and what the narrowing check compares; juggler now derives it so every agent's moxy enforces exactly `tools`, when juggler launches moxy itself (not `--moxy-url`), with no brief-author footgun | none expected; a brief's own `allow-tools` is checked but no longer the thing moxy actually loads, beyond that parse-time (and derive-time) coverage check |
| early stop on evaluator pass | off by default (`evaluator.stop_on_pass = true` opts in per brief) | an agent that keeps calling tools after the work is done spends steps and may duplicate artifacts, but stopping early is a behaviour change a brief author should choose | most briefs set it (→ make it the default) |
| evaluator kinds | `jq` only | smallest signed-contract surface | the same count-of-kind jq appears in most briefs (→ `predicate`) or a task needs judgement (→ `agent`) |
| headless permission posture | non-`always-allow` → deny (intended; not enforced by moxy today, moxy#485, pending moxy#486 — see the erratum) | no human to ask | a moxin's tier is `ask` only because nobody set it, and agents keep failing on it |
| account teardown | on by default in `juggler resolve`; `--keep-accounts` opts out | a run's accounts have no use after its result line is posted, and each one left is a credential that can still log in | debugging a run needs its identities alive afterwards (→ `--keep-accounts`, then a later `resolve` finishes the teardown) |
| transcript layout | one MUC per run | one link for the fallback note; brief in the same transcript | runs grow long enough that per-agent rooms read better |
| daemon for remote models | optional | the webhook host's service user has no user session | a host needs local inference for these agents (→ system-service daemon) |
| agent-scope realisation | open: sub-cgroup vs sibling unit | the bullet needs neither namespaces nor the choice | signing lands, or a non-fixed-surface tool server appears (→ sibling unit + `limits.sandbox`) |
| `traffic.jsonl` retention | append-only within a run, truncated only on a retry of the SAME job (clown#255); swept with `--resolved` once resolved and past `juggler clean`'s resolved-run age guard (clown#250) | arguments are the whole point of the file; capping or rotating within a run would defeat the audits it exists for. Its own `traffic/` tree (not `agents/<principal>/`, which is moxy's CWD and so tool-reachable) is reaped by clean; `agents/<principal>/` itself still has no sweep | clean's sweep needs to run on a schedule rather than by hand (→ a `ringmaster reap`-style systemd timer invoking `juggler clean --act --resolved`) |
| `juggler clean --older-than` | 1h | an in-flight retry must never be reaped out from under itself; 1h is generous headroom over any realistic backoff while still closing the leak promptly. Measured from `pending_since`, stamped fresh on every failed attempt (not `created_at`, which a resume carries forward unchanged — though a pre-clown#250 record with no `pending_since` at all falls back to `created_at` rather than reading a zero value as overdue) | a retry policy's total span routinely exceeds 1h (→ raise it, or stamp a policy-aware deadline instead of a flat duration) |
| `juggler clean --resolved-older-than` | 30d | matches the host's current interim room-retention rule (see "Per-run rooms accumulate" above); staged/traffic files are audit material, not scratch that should vanish quickly, so this is a retention policy, not a retry guard | the host's room-retention rule changes, or staged/traffic files need to outlive 30d for a slower-moving audit need |
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
