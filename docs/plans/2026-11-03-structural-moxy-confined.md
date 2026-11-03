# Structural moxy for confined workers: the clown side (design, 2026-11-03)

Design note, no code. For the operator to approve before anything merges. The
spinclass side (FDR 0035 R3, the sweatfile resource rules) is spinclass/light-cedar's
note; this one covers what clown must build and change. Labels: **R** read in
code or docs this session, **I** inferred, **U** unverified or undecided.
Nothing here was built or run.

## The directive

Operator, via circus/eager-holly (2026-11-03): confined workers need to reach
moxy, and **only** the mcps defined by the profile's moxyfile and the resource
rules defined by the sweatfile. It must be **structural**: reaching more than
that should not be possible from inside the confined unit. Moxy plus toolbox is
the default for all profiles. (`57ce758` already enables toolbox in the
non-worker templates and in the in-unit moxyfile; this note is about `worker`
and `canary-spinclass`, which drop moxy whole.)

## Where we are (R)

- A confined spawn today must drop `moxy/moxy` whole and keep `just-us`
  (spinclass `checkConfinedToolSurface`, FDR 0035 R3). Reason on record: the
  rule wants no moxy `grit_*` reachable, and in-unit exclusions are "policy, not
  a boundary" (clown FDR 0020, the exclusion paragraphs): an in-unit moxy and its
  bridge run in the worker's unit, so the worker can reach its own
  `/clown/exclude-tools` on loopback. clown#301 closed the same hole only for
  the spinclass-serve bridge (its control routes answer 403 to the unit).
- A confined session that keeps moxy gets it **inside** the unit
  (`startInUnitServers`, `agent-relay`), under `agentscope.InUnitMoxyfile`
  (an `allow-tools` ceiling, read-only bind). FDR 0020 says why not the
  frontend: "A frontend moxy would be a ProtectHome bypass through
  `folio_write`."
- Frontend-class servers (ringmaster, troupe, spinclass serve, the
  `clown-builtin-moxins` issue-filer) run outside the unit with the operator's
  authority and reach it over a forwarded socket. A moxin served that way "must
  have no filesystem or exec effect for its caller" (`frontendmoxin.go` header).
- The unit's view (R, FDR 0020 unit properties, `agentscope/spec.go`):
  `ProtectHome=tmpfs`, `PrivatePIDs=yes`, `PrivateTmp`, the worktree and
  `binds_rw`/`binds_ro` from the spinclass spec, `HOME` = an agent home under
  the worktree (default `<worktree>/.tmp/agent-home`), loopback unreachable,
  sockets bound in as single inodes.
- The sweatfile rules reach moxy as the policy broker: `spinclass serve` listens
  on a per-session socket, clown names it in `CLOWN_POLICY_BROKER`
  (`brokersocket.go`), confined it is relayed to `127.0.0.1:19001` in the unit.
  moxy#490/#500: folio, rg, jq and toolbox ask it per file operand.

## Why the obvious design fails the directive

"Run one full moxy in the frontend, reachable only over the unit's socket" is
what the directive's wording suggests. As a plain frontend process it does not
make "more than defined" impossible:

1. **Wrong file view (R).** A frontend moxy sees the operator's filesystem, not
   the unit's. `~` is the operator's home there, not the agent home (moxy FDR 0016 says
   that in a confined unit `~` means the unit's agent home; a frontend moxy
   has no way to know that on its own, I). The only thing then keeping it off
   the operator's files is the broker's verdict on a **path name**.
2. **Symlinks (R for the premise, I for the exploit).** The worktree is writable
   by the unit. A worker plants `link -> ~/.ssh/id_x` in it; the path the broker
   judges is inside the worktree, the file the frontend opens is not. moxy#490's
   own text says `folio.read` follows a symlink leaf today; toolbox opens with
   `openat2(RESOLVE_NO_SYMLINKS)`, folio, rg and jq do not. Fixing that in every
   tool is moxy's lane and would make safety depend on every path-taking tool
   in the catalog being right.
3. **Unreviewed tools (R, moxy FDR 0015).** Under `permission-mode = "enforce"`
   a tool with no declared tier is refused, which is the safe default, but the
   guarantee is exactly "as good as every tool's resource declarations". That is
   a policy argument, not a structural one.

So a plain frontend moxy trades the unit's mount namespace, which is a boundary,
for path-name policy, which is not. I recommend against it.

## Recommendation: moxy in a sibling unit, same view, outside the worker's reach (I)

Serve moxy from a **second transient service** that clown starts next to the
agent unit, instead of inside it or in the frontend:

| Property | Agent unit (today) | Moxy unit (proposed) |
|---|---|---|
| Spec | spinclass spec | the **same** spec (same binds, `ProtectHome=tmpfs`, agent home, deny paths) |
| Filesystem view | the spec | the spec, by construction (so `~`, symlinks and the worktree resolve exactly as for the worker's own tools) |
| pid ns | `PrivatePIDs=yes` | its own `PrivatePIDs=yes`: the worker cannot see or ptrace it |
| Config the worker could touch | InUnitMoxyfile read-only bind, but exclusions pushed to a route the worker can reach | moxyfile and exclusions written by the frontend into clown-owned state that is **not** a bind of the agent unit; control routes answer the unit 403 (the #301 peer-class mechanism) |
| Reach into the worker's unit | n/a | one unix socket bound **into** the agent unit as a single inode (the existing Sockets mechanism) |
| Permission mode | delegate (hook in front) | `enforce`, plus the broker (the sweatfile rules) |

What this buys, against the directive:

- **"Only what is defined":** the effective tool set is the moxyfile
  `allow-tools` clown writes (profile ceiling intersected with the in-unit
  ceiling), enforced by the proxy, not by a list the worker can reach. The
  frontend reads the effective list back through the control route (the
  readback FDR 0035 R3 calls "form (ii), not built, waits on the frontend
  proxy").
- **"Structural":** the moxy process, its config, its exclusions and its
  control routes are outside the worker's pid and mount namespaces. Its file
  authority is the unit's, not the operator's, so even a bug in a tool's path
  handling cannot reach what the unit cannot reach (the symlink case resolves
  into the hidden home and fails).
- **Same uid is not a hole here (R, FDR 0020):** the agent unit's `PrivatePIDs`
  already hides every other process of that uid; the moxy unit is one more
  process it cannot see.

What it costs:

- A second unit per session: lifecycle (start before the agent, stop with the
  session, die when the frontend dies, the same deadman as the agent unit),
  startup latency, a second cgroup under `clown-agent.slice`, and spinclass's
  confinement check, which currently expects one unit per session (**U**: see
  question (a) to spinclass).
- Not everything moxy does is reachable with the unit's authority. That is the
  point, but it means: git remote operations still go through spinclass serve's
  frontend grit; nix builds still go through the daemon socket the spec binds;
  the broker socket must be bound into the moxy unit too (today it is relayed to
  the agent unit on `127.0.0.1:19001` because its network is private; a unix
  socket bind is simpler for the moxy unit).
- The in-unit moxy's hook logger, `CLOWN_SESSION_ID` gap and async-job
  limitation (FDR 0020, "What a confined worker loses") carry over or need a
  line; a moxy unit that is clown-started could be given the session id.

## The profile's moxyfile as the ceiling (U, needs a decision)

A profile today is `context_servers` (keep servers) plus `context_excluded`
(a **deny**-list per server). That is not a ceiling: a tool nobody listed
(`toolbox_run`, or a tool a later moxy adds) is on. For the directive we need an
allow-list the moxy unit enforces. Three shapes:

1. **A profile field** (e.g. `moxy_allow`), written into the moxyfile as
   `allow-tools`. Honest and explicit; adds a field to `profilelookup`'s
   canonical form, so a `ProtocolVersion` bump announced to spinclass first
   (AGENTS.md: that package is spinclass's), and a registry-stamp consideration
   for older clowns reading `profiles.toml`.
2. **Derive it** from the keep-set against moxy's catalog at launch
   (`catalog minus context_excluded`). No schema change, but it makes a moxy
   upgrade silently widen the ceiling (new tools appear), which is the opposite
   of structural. I do not recommend it for the confined path.
3. **The sweatfile owns it** (spinclass's note): the profile only names
   `moxy/moxy`, the sweatfile names the tools. Then clown's profile need not
   change; it moves the ceiling to spinclass's trusted layers (like
   `spawn-profile`, read only above the repo).

My preference is (1) for clown plus (3)'s trust rule: the profile proposes, the
trusted sweatfile may only narrow, and the moxy unit's effective list is the
intersection. This is the same shape as the user-profile "narrows a builtin"
rule spinclass already enforces (Tier 1), so a new `moxy_allow` is checked by the
existing `catalogWidenings`. **U:** whether that check can read a new field
without a spinclass change.

## What clown has to change (R for locations, I for the changes)

1. **A second-unit launcher** next to `runInAgentScope`
   (`cmd/clown/agentscope.go`): same spec, different command (`moxy serve-mcp`
   behind the stdio bridge), no pty, a unix socket output. Reuses
   `prepareAgentScope`; the unit properties are the agent unit's.
2. **The socket into the agent unit** (`Spec.Sockets` / the serve-socket
   pattern, `ServeConn.Frontend`): the bridge tags peer class; control routes
   refuse the unit; `/mcp` stays open to it.
3. **`InUnitMoxyfile` stops being an in-unit file.** The ceiling moves to
   clown-owned state the frontend writes before the moxy unit starts. The
   constant remains the default ceiling; `57ce758`'s `toolbox` entry carries
   over.
4. **The exclusion push** (`applyInUnitExclusions`, readback file) becomes a
   frontend push over the control route plus a frontend readback, not an
   in-unit record.
5. **Profile templates:** `worker` and `canary-spinclass` keep `moxy/moxy` with
   a real `moxy_allow` (no `grit_*`, `toolbox_run` in); the partition test and
   the "worker must drop moxy whole" test change **only after** spinclass's R3
   changes. I have not touched either.
6. **Docs:** FDR 0020: the "Consumer" table row for moxy (it is no longer "inside
   the unit as claude's stdio child"), the sentence "A frontend moxy would be a
   ProtectHome bypass" (replaced by why a sibling unit is not), the
   exclusions-are-policy paragraphs (true only for non-sibling in-unit servers),
   the "What a confined worker loses" moxy rows (most become available through
   the ceiling), and F12 (the control routes rule now covers the moxy unit).
   `docs/plans/2026-10-26-confined-moxy-sequencing.md` line 41 still says
   clown#301 is open; it is closed for the serve bridge only.

## Alternatives considered

- **Plain frontend moxy** (the directive's literal wording): rejected above.
  It would be acceptable only if every path-taking tool in the profile's
  ceiling were proven no-symlink and resource-declared; that is a per-tool audit
  in moxy's lane and not structural.
- **Keep moxy in the agent unit, make exclusions a boundary**
  (spinclass#383 per-tool add-back plus a read-only exclusion route): needs the
  bridge in the worker's unit to be untamperable, which it is not (same pid ns,
  same uid inside the unit). A boundary needs a different process owner.
- **A toolbox-only in-unit server** (my earlier option (a)): still the cheapest
  way to get `toolbox_run` into `worker` alone, but it does not answer "moxy for
  confined workers". It stays a fallback if the sibling unit is not built.

## Spinclass's answers (light-cedar, 2026-11-03; their note: spinclass
`docs/plans/2026-11-03-structural-confined-moxy.md`, commit 808155c, branch only)

They call this design V2 and recommend it as the boundary; they add V1 (broker
floor with a resolved-realpath check, plus `openat2` in moxy) as an interim and
defense in depth. Their answers, as reported, not re-verified by me:

- **(a) Units:** the spec is one JSON with one unit name hint and one result
  record per worktree. They did not read every confinement-check row and infer
  the checks assume one unit per session. A twin needs a second result path, or
  clown launching it from the same spec without a second spinclass record (my
  call). The protected-bind and probe checks run on the spec, so one spec covers
  both units.
- **(b) Sweatfile `[folio]`:** anchors, not only host paths: `pwd`, `home`,
  `env:`, `nix-store`, `spinclass:{worktree,repo,worktrees,children,siblings}`,
  `madder:`. Evaluated only by `spinclass serve`'s broker. In a confined session
  `home:` means the unit's agent home (the broker already maps it), so the twin
  must set the same `HOME`.
- **(c) Ceiling ownership:** split. The tool ceiling (moxyfile `allow-tools`) is
  the profile's, clown-owned; the resource rules are the sweatfile's, spinclass-
  owned. Neither may come from inside the unit. This settles shape 1 above for
  the tool ceiling; shape 3 is not wanted for tools.
- **(d) R3:** becomes R3': keep the "no in-unit `moxy/moxy`" test and `just-us`;
  if the frontend moxy server is kept, profile resolution must **report** its
  effective tool set, which must contain no `grit_*`; no report means refuse. That
  needs the new profile-protocol field, announced to spinclass first. Spinclass
  changes nothing in R3 until clown reports it.
- Unverified on their side: moxy's `serve-mcp` config discovery (the cwd/moxyfile
  experiment covered `serve-moxin` only), that the twin can reuse the agent-unit
  property builder, and the context cost.

Consequence for the ceiling: the profile gets an explicit tool list field
(shape 1) and `profilelookup` reports the effective set; the sweatfile does not
supply tools. The "hybrid with intersection" preference above is dropped.

## Open questions

- (clown) Second result path vs launching the twin from the same spec with no
  second spinclass record: the second keeps spinclass's checks honest, the first
  is less spinclass work. Undecided.
- (operator) Approve shape 1 (profile field) and V2 as the boundary with V1 as
  the interim? A `profilelookup` protocol bump needs announcing to spinclass
  before it lands.
- (moxy) Does `moxy serve-mcp` take everything needed from a clown-written
  moxyfile under `permission-mode = "enforce"` with the broker (moxy FDR 0015)?
  `enforce` refuses `restart` and any unannotated tool; the in-unit ceiling lists
  `restart`, `batch` and `async`.
- (clown, later) Whether the moxy unit should be shared by a session's spawned
  children or one per unit; the directive says one per unit.

## Not claimed

Nothing here was run. Whether a second unit with the same spec starts reliably,
whether the socket bind into the agent unit behaves as the serve socket does,
and the startup cost are all unmeasured. `just verify-agent-scope` has no case
for any of it.
