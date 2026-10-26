# Unattended clown: confinement (`--agent-scope`)

Status page, 2026-10-26, clown master da76a79 (+ this page). Goal: a confined
clown session that can run for days with nobody at the keyboard. Docs only; no
code is proposed or changed here.

Labels: **V** verified live (a recorded real run, or a host bats test that
drives real systemd; nothing here was re-run for this page, so V means "the
repo records it"), **R** read in code or tested only against fakes/unit tests,
**I** inferred, **not found** = searched, absent. Source: FDR 0020
(`docs/features/0020-agent-scope-confinement.md`), `internal/agentscope`,
`cmd/clown/agentscope.go`, `zz-tests_bats/agent_scope.bats`.

## 1. What is proven, faked, unverified

Proven live (V, FDR table "Status on flic" + `agent_scope.bats`, host-only
lane `just verify-agent-scope`, recorded 11/11 then 18/18 on 2026-10-24):

- Transient user unit launch, `PrivateUsers=self`, `PrivatePIDs`,
  `ProtectHome=tmpfs`, `ProtectSystem=strict`, `PrivateTmp`, `NoNewPrivileges`,
  the unit does not outlive the launcher on the normal exit path.
- `PrivateNetwork=yes`: host loopback/abstract sockets unreachable, forwarded
  listeners reachable; egress only through the allowlisting proxy; loopback
  refused.
- Worktree bind writable, real `$HOME` hidden; the operator OAuth file is
  bound read-only (the read-only property is V).
- One manual real-claude run on 2026-10-19 (FDR): login through the bound
  credentials, transcript landing in the operator's projects dir, Stop hook
  ran in the unit and wrote state through the bind, MCP via forwards ("MCP-OK
  7"), moxy starting in the unit with `allow-tools` active (folio write in the
  worktree ok, to `$HOME` refused).

Only tested against fakes / Go unit tests (R):

- Frontend-serve socket (F12), statsd relay, frontend monitor sockets, idle
  socket: bound by code; no confined session has exercised them live.
- Exclusion readback refusal under confinement (`cheapcontext_inunit_test.go`).
- The MCP startup hold (#296) inside the unit (env passes; the hold itself is
  unit-tested).
- Restart kill of the unit (`killUnit`, fake `systemctl`).
- Permission counters inside the unit (state now under the bound
  `hook-toolcost` dir; the permstat tests run outside a unit).

Unverified (I / not found):

- OAuth token refresh with the credentials file bound read-only: the live run
  that worked was read-write; the FDR calls the refresh-rotation risk "a
  theory". Expiry handling: not found.
- Linger / host reboot behaviour, journald capture of unit stderr (`--pipe`).
- Whether a SIGKILLed frontend leaves the unit running (the stop is a
  `defer`; no `BindsTo`/`PartOf`/`RuntimeMaxSec` in `unit.go`) (I, untested).
- Does `clown resume` of a confined session stay confined? Fixed on the
  clown side (#318): a confined launch records its spec in the
  session-names sidecar (outside the unit), and `clown resume` re-applies
  it, refusing when the spec is gone or a different one is asked for (R,
  unit tests). spinclass side still to do: `sc resume` sets no
  `CLOWN_AGENT_SCOPE` and splices no `dontAsk` (R); the clown-side record
  now confines it anyway, but the dontAsk splice is spinclass's.
  Outside #318: sessions started by the raw `clown agent-scope-run` (no
  check, no record), sessions confined by an older clown, an unconfined
  `-c` or bare `--resume` picker continuing a confined conversation (known
  gap), and a changed `XDG_STATE_HOME` (hides every record).

## 2. Missing for a confined session to run for days unattended

1. **No unattended permission posture.** clown never sets `--permission-mode`
   (default mode: an unallowed call prompts; nothing times the prompt out).
   spinclass splices `dontAsk` into *its* confined spawns; a plain
   `clown --agent-scope` launch has none. Ruled 2026-10-26: unattended plain
   launches use `dontAsk` (#323, not implemented). See the permissions page.
2. **Credentials.** Read-only OAuth refresh unproven; no expiry path; the
   frontend credential-injection proxy is a design only; push/sign needs the
   operator's card (every act prompts), so an unattended session cannot push.
3. **Hang and resource limits.** No `WatchdogSec`, `RuntimeMaxSec`,
   `MemoryMax`, `TasksMax`, `CPUQuota` in `unit.go`; a hung claude sends no
   Stop hook so idle self-close never fires (I); nothing restarts a hung unit.
4. **Frontend death.** A SIGKILLed frontend likely leaves the unit running
   (I). A host reboot or logout ends the transient unit and nothing relaunches
   it; linger state on flic: not found.
5. **Disk and logs.** No rotation/pruning found for `close-self-run.log`, the
   toolcost hook state, `agent-relay.log` (in the agent home under the
   worktree), the read-write madder store.
6. **Resume keeps confinement?** Fixed on the clown side (#318); the
   spinclass side (dontAsk on resume) still to do. See section 1.
7. **Exclusions are policy, not a boundary** in-unit (FDR 705-714); the
   in-unit stdio bridge trusts loopback (#288).
8. **Egress default** lacks the krone nix cache host (#300).

## 3. Smallest ordered work list (issue numbers only where they exist)

1. Run `just verify-agent-scope` at HEAD, plus one real `claude -p` round
   across an OAuth access-token expiry with the read-only bind. No issue.
2. Establish whether `clown resume` of a confined session re-applies
   `--agent-scope` (read the spinclass launch path and `resumeLaunchFlags`;
   test); fix if not. #318: clown side done; spinclass side open.
3. Test and fix the frontend-SIGKILL case (`BindsTo`/`RuntimeMaxSec` or a
   reaper). #319.
4. Add unit resource limits and a wall clock to `properties()`. #320
   (juggler has a `RuntimeMaxSec` precedent).
5. Log/state retention caps (listed in section 2.5). #321.
6. Live check, in one confined session, of idle self-close, monitors, serve
   socket, statsd relay and the permission counters (#287, #269 cited as built
   not verified; #302).
7. #300 (krone cache vs sweatfile), #288 (peer check), #301 (serve route
   refusal), #296 optional live probe.
8. Hang recovery / restart of a confined session: see the restart page.

## 4. Decisions only the operator can make

- Credential model for unattended runs: keep the OAuth file read-only-bound
  (every unit can read it), an API key/setup-token, or the not-yet-built
  frontend injection proxy.
- Egress defaults: add the krone cache, and which login hosts stay default.
- Whether unattended sessions may push/sign at all (piggy prompts every act).
  Ruled 2026-10-26, in part: with the card absent there is no fallback to the
  inherited ssh-agent (#324).
- Resource limit values, wall clock, and restart-on-failure policy (#320;
  restart-on-failure: hung-session recovery stays operator-gated, ruled
  2026-10-26, see the restart page).
- Linger / reboot policy on flic.
- Whether `clown resume` of a confined session must re-confine: ruled yes
  (operator go, 2026-10-26); clown side done (#318).
- Log and state retention (#321).
- Acceptance of in-unit exclusions/recipe scoping as policy only.
