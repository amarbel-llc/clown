# Clown issue grooming (2026-11-06): two passes on the 2026-10-25 triage

Two read-only passes on the operator's idle-time task, folded into one note
(coordinator, 2026-11-06). Nothing was built, run, closed, edited or
relabelled; every disposition is a proposal for the operator.

- **firm-maple** (clown/firm-maple, master e6f652e): a delta on
  `docs/plans/2026-10-25-issue-triage.md`, which already triaged 153 issues
  with per-issue evidence (buckets a fixable / b decision / c blocked / d
  stale). Covers the 24 issues numbered #315 and up and the cleanup it saw.
- **rare-walnut** (clown/rare-walnut, master 7e79f6c): all 146 open titles,
  the 35 issues most likely resolved by recent commits read in full with
  their comments, each "fixed" claim checked against the tree or the log;
  older issues classified from titles and the repo's state.

Evidence labels as in the old note: **V** observed, **R** read in code or
git log, **I** inferred. Where the two passes differ the row says so and
names the session; where one pass checked what the other did not, the
checked answer stands with its evidence.

## 1. Issues newer than the old triage (#315 and up)

| # | Title (short) | State on master | Proposal | Ev |
|---|---|---|---|---|
| 345 | built-in engineer-debug profile | no code | needs operator ruling (the tool list; whether it keeps `Agent`); scope: `internal/0/profile/builtin.toml` plus a `builtin_templates_test.go` row that the keep and exclude sets partition the catalog and `ReadMcpResourceTool` is not disallowed; size S | I |
| 344 | findable plugin and bridge logs | design note merged (0a5a0de, `docs/plans/2026-11-06-clown-344-disconnect-record.md`); code waits for the operator's review | ready once ruled; size L (bridge, host, a frontend resource server) | R |
| 342 | relaunch into or out of dontAsk | **clown side landed**: 70f904a (posture on the verdict, `--posture` hand-off, relaunch env and argv), 7e79f6c (`keep`); the spinclass half is light-cedar's | firm-maple: needs a ruling on the posture; rare-walnut: ruled and built on clown's side, open for spinclass's half | R |
| 341 | confined unit starts from a stated environment | proposal only | log-only first, as the issue says; needs the operator's baseline ruling; size M | I |
| 339 | worker-profile file creation | option B ruled (4fa5ecf); V3 landed (01f2c35, ec252d4, dcc536a) | keep open until the in-unit `moxy narrows` bats check (a skipped placeholder in dcc536a) runs | R |
| 338 | model credential in piggy's secure hosting | follow-up to #336 | blocked on the live round at the card and the held circus patch | R |
| 337 | profile-digest unblock | items 2 and 3 done: 7802efc (the refusal quotes the library's reason), 5277fce (`clown profile digest`); item 4 (#334) linked and built; item 1 (why the stamp did not take on that host) is an operator read | propose closing | R |
| 336 | OAuth delivery to confined sessions | InjectProxy merged (53630d3, 31f31ef, 3f981cc, e6f652e), live-round package e3e50dd | open until the operator's live round passes and the circus patch is applied | R |
| 334 | resume: user-only override | **design A landed** (6a6eda5: the terminal confirm, `profile_pin_source`, FDR 0022 decision 2 amended); C when the card path exists | keep open for the switch to C, per the ruling | R |
| 333 | per-session policy-broker socket | 08149d2, d1ea34a, handoff 4d01efb; `cmd/clown/brokersocket.go` | propose closing after the handoff's lock-bump step is confirmed | R |
| 332 | troupe owner to the frontend scope | the operator ruled the order on 2026-11-01 (frontend first, piggy key later; in the issue) | blocked on sequencing with spinclass FDR 0035; size L | R |
| 331 | Discover refused in confined sessions | 3605b87 (no `Agent` where moxy is dropped; `TestBuiltinTemplatesWithoutMoxyDisallowTheAgentTool`) | propose closing after the a2024fe deploy check; the "portable list" and "move to moxy" (#11) options are not done | R |
| 330 | lock down Monitor | f014fde, f3d7a74, the `explore-monitors-lockdown-*` recipes; refined by #335 (0e3d121) | propose closing | R |
| 328 | adopt dagnabit | 882c06c, e1bcb59; AGENTS.md documents it | propose closing | R |
| 326 | unconfined `-c` or bare `--resume` continues a confined conversation | `unseenResumeForm` refuses those forms when a profile applies (profilegate.go); the confinement check for them is not implemented (the resume help says so, citing #326) | check `cmd/clown/resume_agentscope_test.go`, then a policy ruling: refuse while any session is confined, or document as accepted; size S | R |
| 325 | forward issue-filer into confined units | **done**: `cmd/clown/frontendmoxin.go` serves `clown-builtin-moxins/issue-filer` frontend-side | propose closing (firm-maple had not checked it) | R |
| 324 | unattended with the card absent | design only | needs an operator ruling; size M | R |
| 323 | unattended permission posture | **ruled 2026-10-26 (in the issue) and built**: FDR 0023, `cmd/clown/unattended.go`; the B5 live checks are unrecorded | firm-maple: needs a ruling; rare-walnut: the ruling exists and the code is merged, propose closing (B5 noted in the FDR) | R |
| 322 | notify when blocked or down | clown's half merged (the hung-session status file, FDR 0023); the 2026-10-26 ruling (in the issue) makes the notification spinclass's | firm-maple: needs a ruling; rare-walnut: ruled, close on clown or move to spinclass | R |
| 320 | resource limits and timeout on the unit | **wired**: `internal/alfa/agentscope/unit.go:118-131` (`MemoryMax=8G`, `TasksMax=4096`, `CPUQuota=400%`, `RuntimeMaxSec=604800` unattended); `cmd/clown/agentscope_limits_test.go` | propose closing, or retitle to the per-launch knob the issue also asked for (the values are constants) | R |
| 319 | SIGKILLed frontend leaves the unit | the dead-man socket the ruling asked for: `internal/alfa/agentscope/deadman.go`, `cmd/clown/agentscope_deadman_test.go` | propose closing | R |
| 318 | resume may relaunch confined sessions unconfined | `agentScopeForLaunch` (resume.go), `sessions.AgentScopeOf`; `cmd/clown/resume_agentscope_test.go` | propose closing | R |
| 317, 315 | juggler run cancel race; ResolveModel join | juggler code, still in this tree (`cmd/juggler`, the `juggler-boundary` check) | belong to the juggler repo once #258 extracts it; group under #258 | R |

## 2. Still open from the old triage, with a changed answer

- **#300** (egress lacks the krone cache): still true, `egress.go:30` lists
  `cache.nixos.org:443` and no krone host. The old recommendation (fix FDR
  0020's sentence, keep the host in the sweatfile) stands; it is the
  operator's call since the default list is a security surface. **V**
- **#302** (live permission metrics): the spike the old note asked for is
  done: `internal/bravo/permstat/permstat.go` emits `clown.permission.*`
  and FDR 0023 names the counters. Propose closing. **R** (rare-walnut)
- **#175** (cheap-context to native httpServers): moxy ships
  `/clown/exclude-tools` (moxy#399) and clown's client pushes to both
  (`cmd/clown-stdio-bridge/http.go:51-57`). Propose closing. **R**
  (rare-walnut)
- **#163** (AskUserQuestion auto-continue): `cmd/clown/claudesettings.go:141`
  injects `CLAUDE_AFK_TIMEOUT_MS` to disable it; the "pin a version" half is
  moot. Propose closing. **R** (rare-walnut)
- **#189** (vanity routing): `code.linenisgreat.com/clown` is the push
  remote of every merge this week. Propose closing. **V** (rare-walnut)
- **#243** (no file-based memory): `system-prompt-append.d/04-no-file-memory.md`
  is in place and both sessions' prompts carry the rule. The old note kept
  it open pending a live check that MEMORY.md is no longer loaded; neither
  pass ran that check (rare-walnut's prompt did load a MEMORY.md index, so
  the loading half is not fixed by the fragment alone). Operator's call. **R**
- **#292** (Canonical carries values): the env half is fixed (protocol 2
  digests, the redacted pin form); the URL half is open, as the old note and
  FDR 0022 say. Retitle to the URL half. **R**
- **#78** (CI workflow): no `.github` or `.forgejo`; the old "superseded by
  the pre-merge `just`" recommendation stands. **V** (firm-maple)
- **#258** (extract juggler): still in-tree; the sibling `juggler` repo
  exists but clown's `cmd/juggler` and `internal/*/juggler*` are the live
  code (the `juggler-boundary` flake check pins their isolation). Open as
  sequenced. **R**

## 3. Duplicates and overlaps (rare-walnut)

- #241 (plugin host neither restarts nor surfaces a dead child) is the
  problem behind #344's record; its restart half is FDR 0002's deferred
  `restart` field. Fold the surfacing half into #344, retitle #241 to the
  restart half or close it against #344.
- #299 (one clown URI scheme) and #344: the disconnect record proposes
  `clown://session/<principal>/plugins/...`; settle the scheme once.
- #11 (move Discover to the moxy plugin) is option 4 of #331.
- #117, #134, #119 (clown as the job system, job-as-executor, cross-host
  wakes) predate the ringmaster extraction (FDR 0014); ringmaster FDR-0001
  is their home.
- #141 and #166 (troupe as hub, XMPP backend RFC) are superseded by #213's
  outcome (troupe is its own repo and backend).
- #26, #34, #38 are the issues behind RFC 0006, FDR 0004/RFC 0007 and RFC
  0008, all still draft; the records or the issues should be the tracker,
  not both.

## 4. Belongs to another repo (rare-walnut)

- troupe: #228, #213 (done: troupe exists), #172, #171, #170, #167, #166,
  #156, #141.
- ringmaster: #193, #82, #168, #214 (the monitor is ringmaster's binary
  now), #293 (`job_message` preview truncation).
- circus (the `--backend=circus` provider, FDR 0011 exploring): #86, #76,
  #58, #57, #56, #87, #79; #100 is eng/circus's.
- juggler, once #258 extracts it: #260, #263, #271, #270, #252, #251, #245,
  #317, #315.

## Close list (prepared, NOT executed; firm-maple)

Nothing below has been closed; it waits for the operator's confirmation from a Claude session. "Proven" means the commit named does what the title asks, judged from its message and the files it touches; I did not re-run live checks.

| # | Commit(s) | Close comment |
|---|---|---|
| 328 | 882c06c, e1bcb59 | Adopted: internal/ is in the dagnabit layout, the facade and layout drift gates are wired (`just lint-layout`, `just lint-facade`), and AGENTS.md documents it. |
| 330 | f014fde, f3d7a74 | Done: unattended sessions lose the Monitor tool and cannot TaskStop the wake/troupe monitors; the bats case pins the launch plan. |
| 331 | 3605b87 | Done: no Agent tool where moxy is dropped, and Discover names only tools the unit exposes. |
| 333 | 08149d2, d1ea34a, 4d01efb | Done: a per-session policy-broker socket is named, handed to the spinclass serve and exported as CLOWN_POLICY_BROKER; the handoff note lists what the next lock bump must carry. |

Held back from the list: #243 (the operator's call); #318, #319, #320, #337 (verify first); #336, #338, #339 (open work).

## Ready cleanup, status

- `O_NOFOLLOW` on the token open: done in the commit that follows this note, with a symlink case in `TestTokenSourceRefusesAFileItCannotTrust`.
- Deduping the loopback check in `newModelInjectProxy` (item 1): left as is. The helper returns a bool, while the call site needs the parsed URL and two distinct error messages, so a refactor would add a second parse or change the messages for little gain.

## 5. Ready to work, with scope (rare-walnut; not in section 1)

| Issue | Acceptance test | Files | Size | Ruling? |
|---|---|---|---|---|
| #242 jobs outlive a restart, wakes do not | **corrected**: the issue's comment hands the prompt rule to ringmaster#40 (the job-platform fragment lives there), so #242 closes after a ringmaster bump, which is the operator's lock change. clown's own piece, done in this pass: the restart notice tells a relaunched session not to wait for pre-restart wakes (`restartedJobsNotice`, `restart_relaunch.go`) | `cmd/clown/restart_relaunch.go` | XS | the bump |
| #150 flip pty-suspend default | clownfile(5) states the default; a test of the resolved default (today: "Unset is off") | `internal/alfa/clownfile`, `man/man5/clownfile.5` | XS | yes |
| #165 `--print-session-id` on spawn | spawn mode prints the minted id once; a test | `cmd/clown/attach.go` | S | **corrected**: spinclass consumes the spawn-entry's stdout and uses the hook round-trip today ("direction B" in the issue); a new stdout line is a contract change for spinclass's lane, so not a solo change here |
| #199 `ENABLE_TOOL_SEARCH` by model | no code mentions it; a settings-env test per model | `cmd/clown/claudesettings.go` | S | once the rule is known |
| #116 re-enable WebSearch | find where it is denied; a test | the global denylist | XS | yes (egress) |
| #77 Go 1.26.3 | `go.nix` says `go = "1.26.0"` for the module; the toolchain is igloo's: check the built binary after the next input bump | flake inputs | XS | the bump is the operator's |

Features that read as ready but need a product decision first: #212, #239,
#265, #266, #176/#177, #277, #276, #280, #274, #240, #218/#220, #201, #203,
#205, #206, #211 (the old triage's bucket b holds their options).

## 6. Stale or exploratory (rare-walnut, titles only unless noted)

- Tent (FDR 0007 testing, FDR 0012 exploring): #101, #102, #103, #104, #91,
  #90, #89, #83, #75, #88, #67, #65, #73, #97, #79; `--tent-pass-devshell`
  is still in `clown --help`. No tent commit in the recent log: own the
  group or park it explicitly.
- Explorations: #4, #7, #12, #17, #25, #44, #52, #53, #66, #107, #118,
  #137 (done in spirit by RFC 0014), #138, #139, #142, #143, #153.
- Old chores: #21, #27, #32, #37, #39, #40, #42, #84 (`just lint` runs
  conformist's justfile rules; the old note recommends closing as
  superseded by conformist-justfile(7)), #94, #121, #151, #173.
- #208 and #209 describe Claude Code behaviour; #208's fleet answer was the
  #339 ruling. Close both as upstream with that pointer.
- #81 (`*-local` profiles broken): needs one `clown --profile claude-local
  --print-launch-plan` run to close or confirm.

## 7. Cleanup candidates

From firm-maple:

1. **`cmd/clown/agentscope_model.go:107-116`** duplicates the scheme and
   loopback check `loopbackModelUpstream` (line 49) holds; refactor
   `newModelInjectProxy` onto the helper. S.
2. **`internal/alfa/agentscope/tokensource.go`**: open the token file with
   `O_NOFOLLOW` (hardening; the path is already a checked regular 0600
   file). S.
3. **Model-visible banner** for the proxy mode via `catalogTrimFragment`
   (`cmd/clown/catalogtrim.go`), so the agent knows it holds a placeholder
   credential. Optional. S.
4. **In-unit `.credentials.json` seed**: seed an empty JSON object when the
   login bind is off (reviewer's note; only if that file is touched).
5. **`flake.nix:15-21` comment** (old triage, #63): called out of date
   there; not re-read.
6. **The old triage note** names a worktree and HEAD in its header and has
   rows since closed; strike them or mark it superseded by this note when
   the batch is closed.

From rare-walnut:

7. **Help text drift from #342.** `cmd/clown/main.go:3256` lists `restart
   --session KEY [--when idle|now] [--reason TEXT]` without `--posture`
   (`restart.go:395` has it). The manpage had the same gap and
   `man/man1/clown-restart.1` is corrected in this merge; the Go string is
   left for a code commit. XS.
8. **`cmpOr` reimplements `cmp.Or`.** `cmd/clown/resume.go:284-289`; six
   call sites outside tests (profilegate.go, main.go, resume.go,
   restart_relaunch.go); the module is at Go 1.26. XS.
9. **Test-only entry points in a production file.** `cmd/clown/restart.go:93-116`:
   `restartControlHandler` and `restartControlHandlerNow` are only called
   from tests (main.go builds the triggers form). Move to a test helper. XS.
10. **Duplicated test fixture.** The skewed-registry fixture appears in
    `resume_profile_test.go:39-49`, `resume_override_test.go` and
    `TestLaunchProfilePin`; one helper. XS.
11. **A dead plugin child is logged only at shutdown.**
    `internal/alfa/pluginhost/server.go:216-236`: `reap` closes `done` but
    nothing watches it during the session. #344's host-side item.
12. **AGENTS.md line 16** says "no standalone test suite (pre-merge
    validation via `just build`...)"; the gate runs `just` and
    `just test-go-godyn` is the documented unit lane. Reword. XS.
13. **Two records, one title**: "FDR 0013 — Job-Wakeup Channel (`clown job`)"
    (testing) and "RFC 0009 — Job-Wakeup Channel (`clown job`)"
    (experimental); mark one superseded (by the other, or by RFC 0015 now
    that the channel is ringmaster's).
14. **FDR 0023 status** is still "proposed" though the marker, carrier,
    status file and posture change are merged and spinclass sets the marker;
    its criteria put it at "experimental" once B5 is recorded.
15. **Plan notes without a status line**: most of `docs/plans/` has none; a
    "Status:" convention would make the next pass cheaper.

Both passes agree: no `TODO(#NNN)` exists in the tree, and the only bare
`TODO`s are the intentional placeholder strings in `justfile:1749,1784,1805`
(the model-registry description a recipe prints). Nothing stale to chase.

## 8. For the operator

Proposed closes, each with its evidence above: #328, #330, #333, #325,
#319, #318, #320, #337; #302, #175, #163, #189 (changed since the old
triage); #331 after the a2024fe deploy check; #323 and #322 (ruled and
built on clown's side; firm-maple listed them as needing a ruling, the
rulings are in the issues' comments of 2026-10-26); #243 on the operator's
call. Proposed keeps: #336 and #338 until the live round, #339 until the
narrows check runs, #334 for the switch to C, #342 for spinclass's half.
Proposed retitle: #292 to the URL half, #320 to the per-launch knob if
wanted. Then: the transfer policy for troupe, ringmaster and circus issues
(section 4); the small rulings that unblock XS items (#300, #150, #116,
#345, #326); the tent group (section 6); one code commit for cleanup items
1, 2, 7 to 10 and 12. Neither pass filed a new issue; nothing found is a
defect beyond the help-text drift this merge fixes in the manpage.

## 9. Close list (prepared, not run)

Per the coordinator (2026-11-06 12:11Z): nothing is closed by hand until
the operator confirms from a Claude session. Each row is an issue proven
fixed, the exact commit, and the one-line close comment to post. Where a
file moved in the dagnabit relayout (882c06c) the introducing commit
predates it and is named by the file instead.

| Issue | Commit(s) | Close comment |
|---|---|---|
| #328 | 882c06c, e1bcb59 | Landed: `internal/` is in the dagnabit NATO-level layout (882c06c) with the `lint-layout` and `lint-facade` gates (e1bcb59); AGENTS.md documents it. |
| #330 | f014fde, f3d7a74; refined by 0e3d121 | Landed: unattended sessions lose the Monitor tool and cannot TaskStop the wake or troupe monitors (f014fde; bats f3d7a74); #335 (0e3d121) lets them stop their own backgrounded MCP calls. |
| #333 | 08149d2, d1ea34a (handoff 4d01efb) | Landed: a per-session broker socket path, `CLOWN_POLICY_BROKER` exported to the agent and relayed into confined units (08149d2, d1ea34a). The handoff's lock-bump step is the operator's. |
| #325 | 0718edf, then 7504d6e, 6f5a8e0, ee0c381 | Landed: the issue-filer moxin is served frontend-side as `clown-builtin-moxins/issue-filer` and forwarded into the unit (0718edf and follow-ups); the token file is only stat'ed, never read, by the frontend. |
| #323 | 8a7bc4d, 3984da5 | Landed per the 2026-10-26 ruling: `CLOWN_UNATTENDED=1` puts claude in dontAsk through `--settings` (8a7bc4d), carried across [attach] and refused from config (3984da5); FDR 0023. The B5 live checks remain unrecorded, as the FDR says. |
| #322 | acc1a8301a93 (clown's half) | Landed on clown's side: the hung-session status file (FDR 0023 "Hung-session status file"). Per the 2026-10-26 ruling the notification is spinclass's; closing here, tracked there. |
| #318 | 449c680, df1b923, 947679a, 51b672d | Landed: a resumed session relaunches under its recorded agent-scope spec, bound to the spec's bytes, and is refused rather than run unconfined when the spec is gone or changed (the confinement gate series). |
| #319 | `internal/alfa/agentscope/deadman.go` (predates 882c06c) | Landed per the 2026-10-26 ruling: the dead-man socket; the in-unit shim treats EOF as frontend death and stops the unit; RuntimeMaxSec (#320) is the backstop. |
| #320 | 31a87cb | Landed: `MemoryMax=8G`, `TasksMax=4096`, `CPUQuota=400%` on every unit and `RuntimeMaxSec=604800` for unattended launches (31a87cb). The values are constants; a per-launch knob is a new issue if wanted. |
| #302 | `internal/bravo/permstat/permstat.go` (predates 882c06c) | Landed: `clown.permission.<key>.{prompted,approved,declined}` counters from the PermissionRequest hook (permstat); FDR 0023 names them and notes that dontAsk sessions count nothing. |
| #331 | 3605b87 | Landed: templates that drop moxy disallow the `Agent` tool, so no subagent is spawned with zero tools; Discover's list names only real tools (3605b87). The portable-list and move-to-moxy options stay with #11. |
| #337 | 7802efc, 5277fce | Landed: the refusals quote pkg/profilelookup's own reason or the digest the registry now vouches for (7802efc, item 2); `clown profile digest NAME` prints a profile's digest (5277fce, item 3); #334 is linked and built (6a6eda5, item 4). Item 1 was an operator read of that host. |
| #175 | `cmd/clown-stdio-bridge/http.go:51-57` | Landed: moxy ships `/clown/exclude-tools` (moxy#399) with the same contract as the bridge, and clown's cheap-context client pushes the exclusion set to native httpServers plugins too. |
| #243 | bd338b9, 61b7af2 | Landed: `system-prompt-append.d/04-no-file-memory.md` forbids the harness's file-based memory unless the user asks in the conversation. Not verified live that MEMORY.md is no longer loaded; close only if that is acceptable. |
| #163 | `cmd/clown/claudesettings.go:141` | Landed: clown injects `CLAUDE_AFK_TIMEOUT_MS` (max int32) into claude's settings env, disabling AskUserQuestion's auto-continue; no version pin needed. |
| #189 | no commit | Done: `code.linenisgreat.com/clown` is the push remote and every merge lands through it. |

Not in the list on purpose: #292 (retitle to the URL half), #336 and #338
(the live round), #339 (the narrows check), #334 (switch to C), #342
(spinclass's half), #242 (ringmaster#40's bump; clown's restart notice now
carries the rule).
