# Clown profiles: status for the unattended trip (2026-10-26)

Read-only survey, no code. Labels: **V** observed this session, **R** read in
code or docs (not run), **I** inferred. Sources: `internal/profile/builtin.toml`,
the operator's `~/.config/clown/profiles.toml` (keys read, token lines skipped),
clown-profiles(5), FDR 0022, spinclass `confineprofile.go`, FDR 0034/0035,
spinclass plans of 2026-10-26, EPIC 0001. Spinclass's side was read from its
checkout, not confirmed with its session.

## 1. What exists, and what each can do unattended

`builtin.toml` holds 12 builtins (R): six provider/backend profiles
(`claude-anthropic`, `claude-local`, `opencode-*`, `crush-*`; no server
narrowing) and six claude worker templates. All six templates set
`CLOWN_DISABLE_CLAUDEAI_CONNECTORS=1` and the same `CLOWN_DISALLOWED_TOOLS`
(Artifact*, DesignSync, Workflow, plan mode, cron, RemoteTrigger, MCP resource
tools, ...). "Confined-eligible" = passes spinclass's confined-spawn rule:
a builtin (or a user profile with a `base` it only narrows) that **drops
`moxy/moxy` whole and keeps `just-us/just-us`** (R, `checkConfinedToolSurface`).

| Profile | Keeps | Notable denials (R) | Confined-eligible | Unattended dontAsk worker? |
|---|---|---|---|---|
| `claude-anthropic` (build default) | everything (no selection) | none | no (keeps moxy) | unconfined only; widest surface incl. grit, smith writes, mail |
| `reviewer` | ringmaster, troupe, moxy (read subset: rg, jq, folio read/ls/glob/tree, grit_branch/stage, smith reads, man) | folio_write, grit_commit/merge/remote, all smith writes, chix, browser, mail | no | read-mostly researcher/reviewer, unconfined |
| `porter` | ringmaster, troupe, spinclass, moxy, just-us | spinclass grit and close/spawn/grant tools; moxy: grit_clone/merge/stash/tag/worktree, smith merge, comment and issue writes, most of chix; keeps folio_write, grit_commit/remote, smith_pr-create | no | the only template that can land work (keeps merge-this-session, nothing-but-the-truth); unconfined only |
| `canary` | ringmaster, troupe, moxy (rg, folio_read/ls only) | everything else in moxy; no spinclass, no just-us | no | probe only |
| `canary-spinclass` | ringmaster, troupe, spinclass (grit, whoami, version, restart-*, host-load, close-this-session), just-us (run_recipe, list/show recipe) | moxy dropped whole; spinclass merge-this-session(-async), nothing-but-the-truth, spawn, close-child, grant | **yes** | confined, can edit (native tools), commit (spinclass grit) and run recipes, **cannot merge or file issues** |
| `canary-grit` | as canary-spinclass plus moxy (folio read/ls, rg, jq) | as above | no (keeps moxy) | probe only |

User profiles in `profiles.toml` (R; none declares `base`, so none can pass a
confined spawn as a narrowing, and all but `fable-design` keep moxy or lack
just-us anyway):

| Profile | Keeps | Notes |
|---|---|---|
| `manager` | ringmaster, troupe, spinclass, moxy, just-us, cutting-garden | moxy minus browser, nix, env_get, folio destructive, git remote/merge, personal data, smith writes, infra and deploy triggers; attended coordinator role |
| `groomer` | ringmaster, troupe, spinclass, moxy, cutting-garden | read-only moxy (no folio_write, no git writes); no just-us |
| `fable-review` | as manager, model `claude-fable-5-1` | document/code review, edits and commits |
| `fable-design` | no selection (everything), model `claude-fable-5-1` | attended design sessions |
| `lobotomee` | ringmaster, troupe, spinclass, model haiku | no moxy and no just-us, so refused for confined (no run_recipe) |

Which profile a spawn gets (R): spinclass uses the `profile` parameter, else
`[session-entry] spawn-profile` (for a confined spawn read only from sweatfiles
above the repo), else no profile; a confined spawn with neither is refused.
With no profile, clown applies its build default `claude-anthropic`
(`flake.nix defaultDefaultProfile`; order: `--profile`, `CLOWN_PROFILE`,
clownfile pin, build default, picker). I found no `spawn-profile` in the
sweatfiles I could read, so the `--profile claude-anthropic` in solid-elm's argv
most likely comes from clown re-passing a defaulted name on its relaunch
(FDR 0022, "inner clown receives the resolved name") rather than from spinclass
(I; check solid-elm's session state to be sure).

## 2. The `base` field, the registry, what a profile can express

- **Registry (R, V for the tests).** builtins compiled in, merged with
  `profiles.toml`; a user profile with a builtin's name overrides it. Every
  save stamps `[registry]` (`clown_version`, lowest `protocol`: 2 when any
  profile uses `base`, else 1). `pkg/profilelookup` (spinclass links it) fails
  closed on an unstamped, newer, unknown-key or base-under-protocol-1 registry.
  #303 (edit dropped `base`), #304 (all-stale exclusion list), #305 and #316
  (save-selection data loss) are closed.
- **`base` (R).** `builtin` or `builtin@sha256:<hex>`: a declaration, nothing is
  inherited; clown refuses a base that names no builtin or whose pin no longer
  matches. Spinclass accepts a user profile for a confined spawn only if its
  base is a builtin and `catalogWidenings` finds nothing the child adds.
- **Can express:** provider/backend/model, URL and token, env (this is how the
  templates turn connectors off and disallow built-in tools), a keep-list of MCP
  servers (`context_servers`), per-tool deny-lists per kept server
  (`context_excluded`, verified by a readback that refuses the launch if a
  named tool is still visible or none match).
- **Cannot express:** a per-tool add-back (keep a server but only these tools:
  spinclass#383, open); permission schemes, i.e. a mode plus allow list
  composed with spinclass's sweatfile (clown#276, open; today allow rules and
  `dontAsk` come from spinclass and `CLOWN_UNATTENDED`, so a profile can deny
  built-in tools but never allow one); per-repo or per-role stacking and
  inheritance (EPIC 0001 decision 3, design only); a deny-list of servers (keep
  list only).

## 3. Profile changes needing approval (FDR 0022, status experimental)

- **Built in clown (R, unit-tested, not run live):** per-session
  `(name, digest)` pin in `session-names.jsonl` (trust on first use); `clown
  resume` re-resolves and fails closed on any change, a missing profile, a
  registry skew or an unknown base; `CLOWN_PROFILE_DIGEST` and
  `CLOWN_PROFILE_SESSION` are the launcher's approval and are scrubbed from
  children; unidentifiable resume forms (`-c`, bare `--resume`) are refused when
  a profile applies; a redacted field diff for the refusal text
  (`profilelookup.Diff`, `DiffRedacted`); the restart path dry-runs the same
  gate before it stops anything (`restart_gate.go`).
- **Spinclass side (R, read on its checkout; its master not confirmed):** grant
  store, `sc profile-grant-bless` and `resume --repin-profile` (card, CLI and TTY
  only, tested against fakes; no recorded live card run I could find),
  `[profile-rights] enforce`, digest resolver. `source=dynamic` profiles are
  refused for confined spawns.
- **Design only:** a signed card escalation for re-pin from another surface
  (spinclass#407), the narrowing exemption (decision 6), dynamic profiles
  proposed, digested, blessed inline and stored in madder (EPIC 0001 decision
  14 and later), grants binding a chain of profiles.
- **What an unattended session does on drift (R):** a relaunch whose profile
  gate would refuse is dropped before the provider is stopped ("restart
  cancelled ... the requester must ask again"); a launch or resume that refuses
  exits non-zero with the diff on stderr. Both end up only in the frontend's
  stderr and `close-self-run.log`. Nobody is told: the status file does not
  carry it (it is facts about turns), and the notification half of #322 now
  belongs to spinclass. Since 364840ab254a the cancelled restart is counted
  (`clown.restart.cancelled` in stats-me), which is the only fleet-visible
  trace (R, not seen in stats-me yet). Re-pinning needs a card tap by the
  operator, so a drifted session stays down until then.

## 4. Profiles and spinclass's rights

- A confined spawn requires a profile (R, `requireConfinedProfile`): explicit or
  `spawn-profile` from a trusted layer; builtin, or user profile with a builtin
  `base` it only narrows; moxy dropped whole; just-us kept; and workers must
  keep ringmaster and troupe or they are mute (spinclass refuses). That leaves
  exactly one builtin that qualifies today: `canary-spinclass`.
- Handle rights (FDR 0032 D13, R): eight rights recorded; only `close`,
  `grant` and `restart` are enforced; `observe`, `instruct`, `cap`, `merge`,
  `check` are recorded but checked by no tool (spinclass#429, open). Profile
  grants (FDR 0034) derive rights from profile names in the interim; the
  profile right is enforced only under `[profile-rights] enforce`.
- **EPIC 0001 (status: proposed; I for how much exists).** The five role
  profiles (`manager-coordinator`, `manager-groomer`, `engineer-builder`,
  `engineer-reviewer`, `engineer-researcher`) are not authored. Their nearest
  existing things: `manager` and `groomer` (user profiles, unblessed),
  `porter` (builder), `reviewer` (reviewer); the researcher is new. Not built:
  the hyphence profile document, `_base` stacking, signed grants over profile
  chains, bless-prompt UX, the embedded-moxy catalog layer (`permission-mode =
  enforce`), required profiles by default. Concrete leaf names are the operator's.

## 5. For the trip

| Need | Status | Blocked on |
|---|---|---|
| Confined unattended worker that edits, commits, runs recipes | possible now with `canary-spinclass` (R; live boot not verified by me) | nobody |
| Confined worker that **lands** its work (merge-this-session) | not possible: no confined-eligible profile keeps the merge tools; `porter` has them but keeps moxy | clown (new builtin, below) |
| Confined worker that files issues or uses smith, mail, browser | not possible: moxy is dropped whole, connectors off; use pickup notes | spinclass#383 (add back tools) or a design call on the krone connector |
| Unconfined unattended worker with moxy tools | possible: `porter`/`reviewer` plus `CLOWN_UNATTENDED=1`; moxy's own permission layer denies calls that need a human (V: reading outside the worktree was denied in this dontAsk session) | operator accepting unconfined |
| Native Write/Edit allowed under dontAsk | depends on spinclass's allow list from the sweatfile, not the profile (V: native Edit worked in this session; native Write not exercised) | clown#276 to carry it in the profile |
| Profile edited or clown upgraded mid-trip | pinned sessions refuse to resume until a card tap; restart cancelled and counted | operator at the card, #407 |
| Rights `merge`/`check`/`observe` limiting a worker | recorded, not enforced | spinclass#429 |
| Five role profiles, stacking, dynamic profiles | design only | EPIC 0001 milestones, operator names |

Three smallest profile changes that would most reduce the gaps (all I):

1. **A builtin `worker` for confined unattended work**: canary-spinclass with the
   landing tools kept (`merge-this-session`, `merge-this-session-async`,
   `nothing-but-the-truth`, `update-this-session-description`), moxy still
   dropped whole, just-us kept. About 20 lines in `builtin.toml` plus a test
   pinning the R3/R4 shape like the existing canary-spinclass one; no protocol
   bump (a new builtin changes no existing digest). Removes the "confined worker
   cannot land" gap.
2. **Set `[session-entry] spawn-profile = "worker"` in a sweatfile above the
   repos** (operator or spinclass, not clown): every spawn then fails closed onto
   the narrow profile instead of the build default `claude-anthropic`, which
   keeps every server and is refused for confined spawns anyway.
3. **Decide how confined workers report problems**: today they cannot file
   issues. Either keep the pickup-note convention, or add a variant of `worker`
   with `CLOWN_DISABLE_CLAUDEAI_CONNECTORS` unset so the krone `issue-filer`
   connector is reachable (needs the egress allow-list to include its origin and
   widens the surface; the FDR 0020 run listed the tool but never called it).
   A decision, then one profile.

Not small: a profile-level permission scheme (#276: canonical form changes, so a
`ProtocolVersion` bump announced to spinclass first) and per-tool add-back
(#383, spinclass-first design).
