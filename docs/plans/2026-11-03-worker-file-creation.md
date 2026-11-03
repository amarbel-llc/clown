# Worker-profile file creation (clown#339): a joint design with spinclass

Status: design, 2026-11-03, written by clown/rare-walnut with the broker-side
facts from spinclass/light-cedar (marked "spinclass:" below; R = read in
code or a manpage, I = inferred). Nothing here widens a profile; the
recommendation is a permission-profile decision for the operator.

## What was observed

A confined, unattended session on the `worker` profile (posh/prime-olive,
2026-11-03) reported that it can edit existing files, run `just` recipes
and commit, but cannot create files, search or use a shell. Asked for the
exact refusal, it answered that there was none: `Write`, `Grep`, `Glob`,
`Bash` and `rg_search` were never in its tool list, `Edit` and
`run_recipe` worked with no prompt, and `Read` on a directory returns
`EISDIR`, so it cannot list a directory either. Its tool list was claude's
`AskUserQuestion`, `Edit`, `ListAgents`, `Read`, `Skill`, `ToolSearch`,
plus ringmaster, troupe, the issue-filer, just-us and spinclass's tools.

## Root cause: two layers, each assuming the other covers it

1. **clown's build-time denylist** (`flake.nix`, `disallowed-tools.txt`,
   passed as `--disallowed-tools` on every non-tent launch) denies
   `Bash(*)`, `WebFetch`, `WebSearch`, `Write`, `EnterWorktree`,
   `NotebookEdit`, `PowerShell`, `LSP`, `Glob` and `Grep` to **every**
   claude session. The 2026-04-22 design behind it
   (`docs/plans/2026-04-22-managed-disallowed-tools-design.md`) routes file
   and search operations through moxy's `folio_*`, `rg_search` and `jq`
   tools, which moxy's permission floor and, in a spinclass session, the
   policy broker gate per path. `Edit` is not on the list.
2. **The worker profile drops moxy whole** (`internal/*/profile/builtin.toml`,
   the `worker` entry) so that it passes spinclass's confined-spawn rule
   (FDR 0035 R3: no in-unit `moxy/moxy`). A profile's
   `CLOWN_DISALLOWED_TOOLS` can only add to the global denylist
   (`cmd/clown/claudesettings.go`, `profileDisallowedTools`), never remove
   from it.

So the worker keeps `Edit` (not denied) but loses `Write`, `Glob` and
`Grep` (denied globally) and gets no moxy replacement. Neither layer owns
"may this profile create files": clown's denylist assumes moxy, spinclass's
rule removes moxy. spinclass: the broker is involved in no file operation
of the worker profile as spawned today; it answers only moxy's per-resource
checks, and claude's native tools never reach it (R).

The same sessions can already create files by another route: `run_recipe`
runs the repo's justfile, and the justfile is an existing file the worker
may `Edit`. Denying `Write` is therefore friction, not a boundary. The
boundary for a confined session is the unit: `ProtectHome=tmpfs`, the
worktree bound read-write, the rest read-only or absent (FDR 0020).

## Facts the design rests on

**clown**

- The denylist is global and build-time; the profile knob is additive only.
- The catalog-trim banner tells a session which servers its profile
  stopped and how many tools it hid, not which native tools the build
  denylist removed; posh/prime-olive discovered the gap through
  `ToolSearch`.
- Under `dontAsk` (FDR 0023) a native call that has no allow rule is
  denied with claude's own text, no `PermissionRequest` fires, and clown
  notifies nobody (clown#322).
- The `reviewer`, `porter`, `canary*` and `worker` profiles all carry the
  same `CLOWN_DISALLOWED_TOOLS` core (UI and scheduling tools; `worker`
  and `canary-spinclass` add `Agent`).

**spinclass** (light-cedar, 2026-11-03)

1. `[folio]` judges a resource by path prefix under an anchor; a path that
   does not exist yet is resolved to its nearest existing ancestor and
   judged by the same entries (R, `folioeval/roots.go`). "Create inside the
   worktree" and "edit an existing file" are the same question: allowed
   iff a trusted write entry covers the path (`pwd:` or
   `spinclass:worktree`), else unmatched (ask or deny, as the trusted layer
   says). `.tmp` and the agent home are under `pwd:` (spinclass#443). With
   no `[folio]` above the repo, the broker does not listen and moxy's
   compiled-in rule applies (folio write auto-allows inside the CWD and the
   Claude dirs). There is no create-vs-edit distinction in the stanza.
2. The broker answers only moxy's per-resource checks (`CLOWN_POLICY_BROKER`,
   moxy RFC 0002). Inside a unit the PreToolUse hook writes no decision;
   the harness's own permission rules decide native tools
   (spinclass-sweatfile(5), `[permissions]`).
3. Native-tool permissions come from the generated worktree settings:
   `apply.go` (`ApplyClaudeSettings`) writes the trusted sweatfile's
   `[claude]` allow list plus `Read(<worktree>/*)` and `Edit(<worktree>/*)`
   into `<worktree>/.claude/settings.local.json` at creation, the same for
   every worktree session, confined or not; read-only in the unit since
   spinclass#400. R: the code comment says an `Edit(path)` rule covers every
   file-editing tool (Read, Edit, Write, MultiEdit, NotebookEdit) in the
   harness's rule semantics, so no `Write` rule is written and a test
   asserts that. So, from spinclass's side, `Write` inside the worktree is
   already allowed; the worker lacks it only because clown's
   `--disallowed-tools` removes the tool itself. `Glob` and `Grep` need no
   rule (read-only, never prompt).
4. spinclass#455 (related in the issue): a `dontAsk` session could start a
   rebase of its own branch but not continue or abort it, because the
   ownership check read the checked-out branch, which is none mid-rebase;
   fixed on light-cedar's branch (9db04e7, unmerged, under review: the fix
   will keep ask for a confined caller, since `rebase-merge/head-name`
   lives in the common gitdir's `worktrees/<name>/` directory, writable by
   the unit, so the continue/abort allow applies to unconfined `dontAsk`
   callers only, or the head-name is recorded in frontend state when grit
   starts the rebase). Adjacent, not a cause.
5. A broker deny reaches the session as a moxy tool error carrying the
   verdict and the reason, naming the winning entry or "unmatched" (and,
   for a `dontAsk` session, that nobody can answer an ask). It can say
   which entry refused, not "creating files is forbidden", since the
   stanza has no notion of creation. A native-tool denial under `dontAsk`
   carries only the harness's text.

## Ownership (agreed between the two sessions)

| Question | Owner | Mechanism |
|---|---|---|
| Which tools a profile exposes (native and MCP) | clown, the profile | build denylist, `CLOWN_DISALLOWED_TOOLS`, the profile's keep set and exclusions; a profile is pinned by digest, so a change needs the operator |
| What a moxy tool may touch | spinclass, the broker | sweatfile `[folio]`, evaluated per resource |
| What a native tool may touch | spinclass, the generated worktree settings | `[claude]` allow list written at spawn; structurally bounded by the unit's binds |
| The structural bound for everything in the unit | clown's launcher from spinclass's spec | FDR 0020 / FDR 0035: worktree read-write, the rest hidden or read-only |

Consistency rule (spinclass's formulation, adopted): a tool the profile
exposes but the policy denies surfaces as a refusal (fail closed,
acceptable); a tool the policy would allow but the profile does not
expose is unreachable (also fine). The failure mode to avoid is a native
tool denied under `dontAsk` with no text saying why, and a tool absent
with no text saying why, which is today's case.

## Per-profile intent

| Profile | Role | Create files | Edit | Search and list | Shell |
|---|---|---|---|---|---|
| `worker`, `porter` | implementer | yes, inside the worktree (incl. `.tmp`) | yes | yes | no (recipes only) |
| `reviewer` | read-mostly reviewer | no | today yes; a read-only reviewer variant would drop `Edit` too | yes | no |
| a research profile (none built in today; `canary` is the minimal shape) | researcher | no (a scratch dir is a reasonable exception, under `.tmp`) | no | yes | no |
| `canary*` | smoke | no | yes | no | no |

"Inside the worktree" is the unit's bind, so for a confined session it is
structural; the allow rule or `[folio]` entry only decides whether the
tool refuses before the kernel would.

## Options

**A. Native allow-back for profiles without moxy (recommended first change).**

- clown: a per-profile subtractive knob beside `CLOWN_DISALLOWED_TOOLS`
  (name provisional: `CLOWN_ALLOWED_NATIVE_TOOLS`, a comma list) that
  removes the named entries from the build denylist for that launch.
  Eligible entries are `Write`, `Glob` and `Grep` only; `Bash(*)` and the
  web tools stay denied whatever a profile says. The builtin `worker` and
  `porter` profiles set `Write,Glob,Grep`; `reviewer` sets `Glob,Grep`.
  The knob rides the profile's env table, so it is part of the pinned
  digest: changing it is the operator's act, and a resume shows the diff.
- spinclass: nothing. The generated `Edit(<worktree>/*)` rule already
  covers `Write` in the harness's rule semantics (fact 3), `Glob` and
  `Grep` never prompt, and the broker never sees native tools. light-cedar
  accepts the unit's worktree bind as the bound and the `Edit` rule's path
  scope as the harness-side bound.
- Denial surfacing: a `Write` outside the worktree is denied by the allow
  list under `dontAsk` with claude's text, and the unit would refuse the
  write anyway (`EROFS`, or the path is absent). See "Surfacing" for the
  banner line that names the denied tools up front.
- Size: a few lines in clown (knob, three profile values, a test on the
  eligible set, the banner line) and a bats case; a live confined spawn to
  confirm the harness's `Edit`-covers-`Write` semantics (R from a code
  comment, not yet observed with the tool present).
- Why this is safe enough: the kernel-level bound does not move; the
  worker could already write any file through a recipe it edits into the
  justfile; `Glob`/`Grep` read only what the unit sees.

**B. In-unit moxy for the worker (the destination).** The structural-moxy
note's V3 (`2026-11-03-structural-moxy-confined.md`, approved as the
direction) runs moxy inside the unit with a protected `allow-tools`
ceiling; the default confined moxyfile is `toolbox + env + man`. Adding
`folio` and `rg` to it gives the worker `folio_write`, `folio_ls` and
`rg_search` under the broker's `[folio]` rules, so path policy becomes
per rule, not only per bind, and the broker's deny text names the rule.
This needs the V3 build, R3 relaxed to R3', and an operator decision on
`folio`/`rg` in the default moxyfile (spinclass: "a separate operator
decision"). It does not remove the need for A: a profile that drops moxy
still has only native tools.

**C. Status quo plus surfacing.** Keep workers unable to create files; add
the banner line (below) so a worker says "this profile cannot create files"
at once and the coordinator dispatches a differently-tooled session. Cheap,
but it leaves the implementer profiles unable to add a test or a doc file,
which is the complaint.

**D. Creation through a recipe.** Rejected as a design: it is the escape
route that already exists, and making it official would route file
creation through an unreviewed shell step rather than a permissioned tool.

## Surfacing (every option)

- clown's catalog-trim banner gains one line naming the native tools the
  build denylist and the profile removed and what replaces them, from the
  launch plan: for the worker today "native Write, Glob, Grep, Bash are
  denied; no file-creation or search tool: ask your coordinator"; under A
  "file creation: Write inside the worktree". The same text goes into the
  system-prompt fragment clown already injects, so the model knows it
  before its first `ToolSearch`.
- A native denial under `dontAsk` keeps claude's text; clown#322 (notify
  someone of a denial) stays open and is not needed for A, since inside
  the worktree nothing is denied.
- A broker denial already names the entry (spinclass fact 5). A
  creation-specific message would need a new op kind in `[folio]`, which
  spinclass declines: the prefix rule covers creation.

## Smallest first change

Option A's two halves plus the banner line, landed in this order:

1. clown: the subtractive knob with the eligible set `{Write, Glob,
   Grep}`, the banner line, tests; builtin profile values for `worker`,
   `porter` (`Write,Glob,Grep`) and `reviewer` (`Glob,Grep`). The profile
   value change is the operator-gated part: it widens a pinned profile.
2. spinclass: nothing for A (light-cedar, 2026-11-03).
3. One confined spawn on `worker` that creates a file under `.tmp` and one
   under the worktree root, lists a directory with `Glob`, searches with
   `Grep`, and is denied on a path outside the worktree. This is also the
   check that the `Edit` rule covers `Write` under `dontAsk`; if it does
   not, spinclass adds `Write(<worktree>/**)` (light-cedar's test today
   asserts the rule is absent, so that test would flip).

Operator decisions: whether the implementer profiles may create files
inside the worktree (A); whether `reviewer` gets `Glob`/`Grep` (read-only;
recommended); whether `folio`/`rg` join the V3 default moxyfile later (B).

## Read-only profiles (question 3 of the issue)

Today R3 checks moxy server presence only (`checkConfinedToolSurface`);
the native tool ceiling is clown's denylist, which spinclass cannot see. A
spinclass check that a read-only profile exposes no `Write` would need the
profile protocol to report the native tool set, the same new field R3'
waits on (structural-moxy note). Until then it is clown's job alone.

The one lever spinclass holds (light-cedar): for a confined spawn whose
profile is declared read-only, `apply.go` could omit the
`Edit(<worktree>/*)` rule and the sweatfile's `[claude]` allow for `Edit`,
so under `dontAsk` a native `Edit` or `Write` is denied even if the
profile left the tool in; settings-side, and since spinclass#400 not
widenable from inside the unit. They would key it on a profile flag clown
reports, not on the profile name. That flag is the same protocol addition
as the tool-set field: a `ProtocolVersion` bump announced to spinclass
first (AGENTS.md). Not needed for clown#339; recorded as the shape of the
reviewer/researcher follow-up.

## Open

- Whether `reviewer` should become read-only (drop `Edit`) or a separate
  read-only profile should exist; not needed for clown#339.
- The harness's `Edit`-rule-covers-`Write` semantics, observed live with
  the tool present (step 3 above).
- `Read` on a directory (`EISDIR`): `Glob` covers listing once allowed.
