# Clown issue grooming (2026-11-06): delta on the 2026-10-25 triage

Read-only pass at master e6f652e on `firm-maple`. Nothing was built or run, and no issue was closed, edited or relabelled.

`docs/plans/2026-10-25-issue-triage.md` already triaged 153 issues with per-issue evidence. Re-reading all 146 now would repeat it, so this note covers only what changed since: the 24 issues numbered #315 and up (the older triage stops at #302), what has landed against them, and cleanup candidates. Evidence labels are as in the old note: **V** observed, **R** read in code or git log, **I** inferred.

The list is down from 153 to 146 open. I did not diff the two lists, so which issues closed is unchecked.

## Issues newer than the old triage

| # | Title (short) | State on master | Proposal | Ev |
|---|---|---|---|---|
| 345 | built-in engineer-debug profile | no code found | needs operator ruling (profile contents, a ProtocolVersion decision if the schema changes) | I |
| 344 | findable plugin and bridge logs | not checked | ready to scope; size M | I |
| 342 | relaunch into or out of dontAsk | not checked | needs operator ruling (FDR 0023 posture) | I |
| 341 | confined unit starts from a stated environment | proposal only | log-only first, as the issue says; needs operator ruling | I |
| 339 | worker-profile file creation | operator ruled option B (4fa5ecf); V3 milestones landed (01f2c35, ec252d4, dcc536a) | the in-unit narrows bats check is still a skipped placeholder (dcc536a): keep open until it runs | R |
| 338 | model credential in piggy's secure hosting | follow-up to #336 | blocked on the live round at the card and the held circus patch | R |
| 337 | profile-digest unblock | 5277fce added `clown profile digest NAME` and a pre-stop gate (#340) | the "no command prints a digest" part looks done; verify the rest, then propose closing | R |
| 336 | OAuth delivery to confined sessions | InjectProxy merged (53630d3, 31f31ef, 3f981cc, e6f652e), live-round package at e3e50dd | stays open until the operator's live round passes and the circus patch is applied | R |
| 334 | resume: user-only override | not checked | blocked on FDR 0032's card path, as the title says | I |
| 333 | per-session policy-broker socket | 08149d2, d1ea34a, handoff 4d01efb | propose closing after the handoff's lock-bump step is confirmed | R |
| 332 | troupe owner to the frontend scope | not checked | spinclass FDR 0032 D7; blocked on spinclass | I |
| 331 | Discover refused in confined sessions | 3605b87 (no Agent tool where moxy is dropped; Discover names only real tools) | propose closing after the a2024fe deploy check | R |
| 330 | lock down Monitor | f014fde, f3d7a74, e2e recipes `explore-monitors-lockdown-*` | propose closing | R |
| 328 | adopt dagnabit | 882c06c, e1bcb59; AGENTS.md documents it | propose closing | R |
| 326 | unconfined `-c` or `--resume` continues a confined conversation | not checked | check against `cmd/clown/resume_agentscope_test.go` first | I |
| 325 | forward issue-filer into confined units | not checked | not checked; needs scoping | I |
| 324, 323, 322 | unattended: absent card, permission posture, notification | FDR 0023 and the 2026-10-26 unattended notes exist | each needs an operator ruling | R |
| 320 | resource limits and timeout on the unit | `cmd/clown/agentscope_limits_test.go` exists | verify the limits are wired, then propose closing | R |
| 319 | SIGKILLed frontend may leave the unit running | `internal/alfa/agentscope/deadman.go` and test exist (reference to #319) | verify, then propose closing | R |
| 318 | resume may relaunch confined sessions unconfined | `cmd/clown/resume_agentscope_test.go` carries four references | verify, then propose closing | R |
| 317, 315 | juggler run cancel race; ResolveModel join | juggler code | belongs to the juggler repo once #258 extracts it; do not touch here | I |

"Verify" means the code and tests reference the issue number but I did not read them closely enough to claim the acceptance condition holds.

## Still open from the old triage, with a changed answer

- **#300** (egress lacks the krone cache): still true. `internal/alfa/agentscope/egress.go:30` lists `cache.nixos.org:443` and no krone host. The old recommendation (fix FDR 0020's sentence, keep it in the sweatfile) stands. **V**
- **#243** (no file-based memory): the fragment `system-prompt-append.d/04-no-file-memory.md` is in place and this session's own prompt carries the rule. The old note kept it open pending a live check that MEMORY.md is no longer loaded; I did not run that check, so the operator should decide whether this session's behaviour is enough to close. **R**
- **#78** (CI workflow): `.github` is not a directory and no `.forgejo` files exist. Still no CI workflow; the old "close as superseded by the pre-merge `just`" recommendation stands. **V**

## Cleanup candidates

1. **`cmd/clown/agentscope_model.go:107-116`** duplicates the scheme and loopback check that `loopbackModelUpstream` (line 49) already holds. Refactor `newModelInjectProxy` onto the helper; one definition of "loopback http" is easier to keep safe. Size S. (My own code; I skipped it earlier only because an edit failed on whitespace.)
2. **`internal/alfa/agentscope/tokensource.go`**: open the token file with `O_NOFOLLOW`. The path is already required to be a regular 0600 owner-checked file, so this is hardening, not a hole. Size S.
3. **Model-visible banner** for the proxy mode via `catalogTrimFragment` in `cmd/clown/catalogtrim.go`, so the agent knows it holds a placeholder credential. Optional. Size S.
4. **In-unit `.credentials.json` seed**: when the login bind is off, seed an empty JSON object (reviewer's note, no action needed unless that file is touched).
5. **No `TODO(#NNN)` exists anywhere in the tree** (rg over the non-plan files found none), and the only bare TODOs are the intentional placeholder strings in `justfile:1749,1784,1805` (the model-registry description the recipe prints). The `03-todo-issue-refs.md` convention has no users yet, which is fine, and there are no stale untracked TODOs to chase.
6. **`flake.nix:15-21` comment** (old triage, #63): the old note called it out of date. I did not re-read it today; re-check it when that bucket-(d) batch is closed.
7. **The old triage note itself** names a worktree (`pure-teak`) and HEAD sha in its header and has many rows now closed by later commits. When the operator closes the batch above, either strike those rows or mark the note superseded by this one.

## For the operator

Proposed closes (each with its commit above): #328, #330, #331, #333; #243 on the operator's call; after a quick verify, #318, #319, #320, #337. Proposed keeps: #336 and #338 until the live round, #339 until the narrows check runs. Nothing here was done by me.
