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
- A SIGKILLed frontend takes its unit down: the shim holds the dead-man
  socket and stops the unit on EOF (#319; V: host agent_scope.bats,
  2026-10-26). Not observed: the unit outliving a SIGKILLed frontend
  before #319 (the host test was not run against the older binary).
- Does `clown resume` of a confined session stay confined? Fixed on the
  clown side (#318): a confined launch records its spec and a digest of
  the spec's boundary in the session-names sidecar (outside the unit).
  Every launch naming that session (`clown resume`, `--resume`/`-r` in any
  spelling, `--session-id`, `CLOWN_SESSION_ID`, a restart) re-applies it,
  refusing when the spec is gone, a different one is asked for, or its
  boundary changed, including between the check and the unit launch (R,
  unit tests). Once any session is confined, a `--resume` search term or
  an `-r` cluster is refused for good (the sidecar is never pruned).
  spinclass side still to do: `sc resume` sets no
  `CLOWN_AGENT_SCOPE` and splices no `dontAsk` (R); the clown-side record
  now confines it anyway, but the dontAsk splice is spinclass's.
  Outside #318: sessions started by the raw `clown agent-scope-run` (no
  check, no record), sessions confined by an older clown, an unconfined
  `-c` or bare `--resume` picker continuing a confined conversation (known
  gap, #326), and a changed `XDG_STATE_HOME` (hides every record).

## 2. Missing for a confined session to run for days unattended

1. **No unattended permission posture.** clown never sets `--permission-mode`
   (default mode: an unallowed call prompts; nothing times the prompt out).
   spinclass splices `dontAsk` into *its* confined spawns; a plain
   `clown --agent-scope` launch has none. Ruled 2026-10-26: unattended plain
   launches use `dontAsk`: implemented as `CLOWN_UNATTENDED=1` (#323, FDR
   0023; live checks pending). See the permissions page.
2. **Credentials.** Read-only OAuth refresh unproven; no expiry path; the
   frontend credential-injection proxy is a design only; push/sign needs the
   operator's card (every act prompts), so an unattended session cannot push.
3. **Hang and resource limits.** Every agent unit now carries
   `MemoryHigh=6G`, `MemoryMax=8G`, `MemorySwapMax=2G`, `TasksMax=4096`,
   `CPUQuota=400%`, `IOWeight=50`, `OOMPolicy=stop`, `Restart=no`, and an
   unattended one (`CLOWN_UNATTENDED=1`) `RuntimeMaxSec=604800` (#320, FDR
   0020; V: host agent_scope.bats read the cgroup files and `systemctl
   show`). No `WatchdogSec`; a hung claude sends no Stop hook so idle
   self-close never fires (I); nothing restarts a hung unit (ruled).
4. **Frontend death.** Handled (#319): the frontend's death, by any
   signal (V for SIGKILL), ends the unit within a 10 s grace (plus at most
   5 s for an unreapable provider) through the dead-man socket; the
   unattended wall clock is the backstop. The result file then has no
   `exit_code` (its writer, the frontend, is dead): read that, with a dead
   frontend, as a dead-man stop. An [attach] session whose outer clown or
   multiplexer client goes away keeps running (the inner frontend lives).
   spinclass should require the `frontend-deadman` capability. Residual: a hung frontend
   that stays alive is not frontend death (hung-session detection is
   separate, spinclass's design); a host reboot or logout ends the
   transient unit and nothing relaunches it; linger state on flic: not
   found.
5. **Disk and logs.** Bounds since #321 (R: unit tests; growth over days
   still unmeasured live):

   | File | Bound |
   |------|-------|
   | `close-self-run.log` and every log opened through `openLogForAppend` (restart preflight lines, the in-worktree `clown-idle-notify.log`) | 1 MiB; over it, renamed to `<name>.1` (one generation, replaced) at the next open: about 2 MiB per log |
   | `agent-relay.log` (agent home, held open for the unit's life) | same 1 MiB / one `.1` generation, rotated by the writer itself (`internal/logcap`) |
   | toolcost hook state `$XDG_STATE_HOME/clown/hook-toolcost/<sha>.json` and `.lock` | state files untouched for 30 days (`toolcost.StateMaxAge`) are removed by the next hook run, and a lock only once it has no state file and is as old (a live transcript rewrites its state every Stop; its lock is never rewritten and stays). A pruned transcript that resumes is counted again from its start |
   | hung-session status file | one ~1 KB file per principal, removed on clean exit; a killed frontend leaves it (replaced by the next frontend of that principal) |
   | `$CLOWN_HOOK_DEBUG_LOG` | opt-in, unbounded; unset in unattended runs |
   | the read-write madder store | NOT bounded by clown: it is madder's blob store (outputs of moxy tool calls), and a cap or purge policy is madder's decision. Open: ask madder's session |
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
3. Test and fix the frontend-SIGKILL case. #319: done (dead-man socket,
   wall clock as backstop).
4. Add unit resource limits and a wall clock to `properties()`. #320:
   done (values per the 2026-10-26 ruling; wall clock unattended only).
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
- Resource limit values, wall clock, and restart-on-failure policy: ruled
  2026-10-26 and implemented (#320); hung-session recovery is the
  operator's, plus spinclass's bounded `restart-hung-child` (see the restart
  page and FDR 0023). No per-session override exists.
- Linger / reboot policy on flic.
- Whether `clown resume` of a confined session must re-confine: ruled yes
  (operator go, 2026-10-26); clown side done (#318).
- Log and state retention (#321).
- Acceptance of in-unit exclusions/recipe scoping as policy only.
