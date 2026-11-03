# V3 structural moxy: build plan and ratification package

Status: plan, 2026-11-03, not code. Follows the operator's rulings: V3
approved (`2026-11-03-structural-moxy-confined.md`, "Operator ruling") and
option B for clown#339 (`2026-11-03-worker-file-creation.md`: workers get
create, list and search through the in-unit moxy's `folio` and `rg` tools,
not the builtin `Write`/`Glob`/`Grep`). Everything in "Ratification
package" is one operator decision; nothing is built before it. spinclass's
answers are folded in where marked "spinclass:" (light-cedar); items still
awaited are marked "pending".

## What V3 is, in one paragraph

Moxy keeps running inside the agent unit as claude's stdio child, as today.
What changes: its global moxyfile, bound read-only over
`$HOME/.config/moxy/moxyfile` in the unit, becomes a real **ceiling**
(`allow-tools` as an allow-list of whole moxins and dotted tools,
`permission-mode = "enforce"`), and clown arms moxy's own narrowing check so
the ceiling cannot be widened from inside: `MOXY_PARENT_MOXYFILE` names a
parent moxyfile clown wrote with `moxy show-effective-moxyfile` (so it
carries `resolved-moxins`), `MOXIN_PATH` is the same pinned absolute list
on both sides, and the in-unit moxy enforces "child narrows parent" at
startup and on every reload (moxyfile(5) NARROWING; `moxy narrows` is the
same check as a command). Path policy for `folio`/`rg` is the policy
broker's (`spinclass serve`, sweatfile `[folio]`), relayed into the unit at
`BrokerPort` as today. The ceiling is policy, not a boundary
(spinclass#400, "Still open" in the structural note): the unit's binds are
the boundary.

## Ratification package (one operator decision)

### 1. The default confined ceiling

Today (`internal/*/agentscope/inunit.go`, `InUnitMoxyfile`): `allow-tools`
= folio, grit, rg, jq, man, chix, env, toolbox, batch, async, async-result,
async-cancel, status, restart; `disable-moxins` = smith, chrest, calendar,
car, gmail, gws, piers, prison, freud, stats-me. No `permission-mode`.

Proposed V3 default (whole moxins first, then dotted tools):

```toml
# Written by clown for an agent unit (clown#244, FDR 0020; V3 ceiling,
# clown#339). Read-only inside the unit; narrowing enforced by moxy against
# the parent clown wrote (MOXY_PARENT_MOXYFILE), so nothing below widens it.
permission-mode = "enforce"
allow-tools = [
  "toolbox", "env", "man",
  "rg.search",
  "folio.read", "folio.ls", "folio.glob", "folio.tree", "folio.file-type", "folio.mktemp",
  "folio.write", "folio.mkdir", "folio.mv", "folio.rm",
]
disable-moxins = ["smith", "chrest", "calendar", "car", "gmail", "gws", "piers", "prison", "freud", "stats-me", "grit", "chix", "jq"]
```

Dropped from today's list, with the reason:

| Entry | Why it leaves |
|---|---|
| `restart` | refused under `enforce` (moxy/sunny-sumac, from source) |
| `batch`, `async`, `async-result`, `async-cancel`, `status` | meta tools a worker does not need for files; `enforce` refuses unannotated tools, and whether these are annotated is unverified; add back on request |
| `grit` | spinclass's grit tools are the confined worker's git (FDR 0036; R3's reason); moxy's grit would bypass its ownership model |
| `chix`, `jq` | not needed for the clown#339 case; `chix` builds through the host daemon and is a separate decision |
| `folio.cp`, `folio.link` | `link` is the symlink-planting case the structural note flags (folio, rg and jq do not open with `RESOLVE_NO_SYMLINKS`, moxy#490); `cp` reads any path the broker allows, so it may come back once the read rules are settled |
| `folio.chmod`, `folio.du`, `folio.df`, `folio.tar` | not needed |

Kept whole: `toolbox` (pure applets; `toolbox.run` is dynamic, floor plus
broker), `env` (with the moxy#497 guards once that moxy is deployed), `man`.

Tool names here are moxy's dotted `allow-tools` form (`folio.write`); the
agent sees them as `folio_write`. Whether `allow-tools` accepts the dotted
form in a moxyfile (not only on `serve-mcp`'s flag) is verified by the
first build step; the structural note records it for `serve-mcp` (V) and
`show-effective-moxyfile` writes "per tool".

### 2. The `[folio]` rules that bound the write tools to the worktree

Semantics (spinclass, 2026-11-03): the stanza judges a resource by path
prefix under an anchor; a path that does not exist yet is judged by its
nearest existing ancestor, so create and edit are the same question; a
write is allowed iff a trusted write entry covers the path (`pwd:` or
`spinclass:worktree`, which covers `.tmp` and the agent home per
spinclass#443); anything else is unmatched, which under `dontAsk` is a deny
naming "unmatched". When no layer above the repo sets `[folio]`, the
broker does not listen and moxy's compiled-in rule applies (folio write
auto-allows inside the CWD and the Claude dirs).

spinclass (light-cedar, 2026-11-03): there is **no** default stanza a
confined spawn gets. The stanza is operator config read only from the
global and parent-directory layers (a repo- or worktree-level value is
ignored with a warning), not merged, one policy for every session in that
hierarchy, confined or not (per-profile patterns are the later half of
spinclass#438). Per-caller anchors make one stanza serve both:
`spinclass:worktree` is the caller's own worktree; for a confined session
the agent home at `<worktree>/.tmp/agent-home` is under it (#443). The
text spinclass-sweatfile(5) accepts today, for the B workers:

```toml
[folio]
read  = ["spinclass:worktree", "nix-store:", "madder:"]
write = ["spinclass:worktree"]
deny  = ["spinclass:worktrees/*/.spinclass", "spinclass:worktree/.claude", "spinclass:worktree/.mcp.json"]
unmatched = "ask"
```

Reading it: a path under the caller's worktree is allowed for read and
write; `/nix/store` is readable (derivation outputs a worker inspects);
`madder:` lets a tool read back the blobs moxy streams its outputs to
(add it to `write` only if the in-unit moxy's streaming needs the broker's
say; today the broker never sees the stream itself). The denies: the
`.spinclass` mirror of session state and the spawn log (a deny applies to
every operation and its specificity beats the allow), `.claude`
(`settings.local.json` is read-only in the unit since #400 anyway) and
`.mcp.json` (the #400 planting vector). `unmatched = "ask"` is a deny under
`dontAsk` and the refusal names "unmatched"; writing `"deny"` is equivalent
for workers but turns every operator session's out-of-worktree read into a
deny instead of a prompt, which is why the manpage's example uses `ask`. An
exec check has no allow list, so only deny or unmatched answers it: with
this stanza an exec-class moxy tool is asked, i.e. denied for a worker. The
worktree's gate files (`justfile`, `flake.nix`, `flake.lock`) are the
guarded gate's control (#449), not the stanza's. `sc validate` checks the
stanza.

This stanza is the operator's to add to the global or parent layer; it is
not written by clown or spinclass.

### 3. What the broker denies, and how it reads

- `folio.write`/`mkdir`/`mv`/`rm` on a path outside the worktree entry:
  denied, the tool error names the entry or "unmatched" and, for a
  `dontAsk` session, that nobody can answer an ask (spinclass fact 5).
- `rg.search`/`folio.read` on a path outside the read entries: the same.
- A path reached through a symlink leaf: judged by its name, not its
  target (moxy#490); with `folio.link` out of the ceiling, a worker cannot
  plant one through moxy (it still can through `run_recipe`, which is the
  accepted exec surface).
- Everything in the unit is still bounded by the binds: a write the broker
  allowed but the kernel refuses fails with `EROFS`.

### 4. Where the ceiling comes from: two shapes, pick one

**Shape P, profile field (the structural note's preference 1 + 3's trust
rule; the shape spinclass asked for).** A new optional `moxy` table on the
profile (light-cedar, 2026-11-03):

```toml
[profile.moxy]
placement   = "in-unit"          # or "frontend" | "twin"
allow_tools = ["mcp__plugin_moxy_moxy__folio_read", ...]   # the ceiling, as the agent sees the names
tools       = ["mcp__plugin_moxy_moxy__folio_read", ...]   # the effective set after the ceiling and the exclusions
```

`allow_tools` is the read-only global moxyfile's ceiling; `tools` is what
spinclass judges; both ride so spinclass can check `tools ⊆ allow_tools`
(proof the set is no wider than the ceiling). clown computes both from the
profile alone (the ceiling list minus `context_excluded`; no live catalog,
so the canonical form stays a function of the definition). A profile
without the table keeps today's meaning. The trusted sweatfile may only
narrow the ceiling; the effective moxyfile is the intersection.
`checkConfinedToolSurface` then accepts `moxy/moxy` in the keep set iff
`placement == "in-unit"`, `tools` is non-empty and `⊆ allow_tools`, and
contains no `grit_*`, no `smith_*`, no `restart`/`batch`/`async`, and no
tool outside the toolbox+env+man default plus the B subsets (the spawn
grant's profile digest pins the whole table anyway); anything else refuses
naming the tool; `frontend` with moxy kept stays refused; `twin` refused
until specified.

This is a `ProtocolVersion` bump (2 to 3) in `pkg/profilelookup`,
announced to spinclass first:

- canonical form: a new key `moxy` after `base`: an object
  `{placement, allow_tools, tools}` with sorted, deduplicated arrays, or
  `null` when the table is absent; `protocol` becomes 3;
- registry stamp: `Save` stamps 3 only when a profile uses `moxy` (as
  protocol 2 does for `base`), so an untouched registry stays readable by
  protocol-2 clowns; `OldestReadableProtocol` stays 1;
- `profile.Validate`: `placement` in the three values, `tools ⊆
  allow_tools`, names in the `mcp__<plugin>__<server>__<tool>` shape;
- **cost, clown side:** `protocol` is part of every canonical form, so the
  bump changes every profile digest, builtin and user. Every pinned
  session's profile gate sees a mismatch at its next resume or restart and
  needs the operator's approval (`CLOWN_PROFILE_DIGEST`); the operator
  re-approves the pins they keep. (The protocol-2 bump had the same
  effect.)
- **spinclass side, no flag day** (light-cedar): spinclass resolves
  profiles through the clown library it links (`LinkedClownRev`, from its
  locked clown input), so the field reaches it only after a spinclass
  clown-input bump, rebuild and deploy; until then it sees no `moxy` table
  and keeps refusing moxy whole (R3 as today). Lead time: one spinclass
  release cycle after clown's merge. `catalogWidenings` compares the table
  (narrow-only against the base).

**Shape S, spec field (no profile protocol change).** spinclass puts the
ceiling in the launch spec (`moxy_allow` in the FDR 0035 spec, from its
trusted sweatfile layers, the way `egress_allow` is passed); clown writes
the moxyfile from it, intersected with the compiled-in default (narrowing
only), and refuses a spec that would widen the default. This bumps the
spec version (`SpecVersion` 1 to 2; `LoadSpec` refuses unknown fields),
which is a clown-spinclass contract with no profile digest involved: no
pin re-approval, no `SkewError`. The profile keeps `moxy/moxy` with its
exclusions as the deny-list within the ceiling. spinclass's condition
(light-cedar, 2026-11-03): under S an R3' check against the spec list is
spinclass checking its own intent, not what the unit serves. That is
acceptable as policy only if clown's unit record (the frontend-owned
result file spinclass reads back after launch) carries the **digest of
the read-only moxyfile it mounted** and the **effective tool list it
derived**, so spinclass can assert "armed == asked" and close the session
on a mismatch, as the record already carries the unit name and cgroup.
Without that record-back S "gives workers the tools but gives R3' nothing
to stand on" and spinclass would document it as unchecked. P remains the
shape for the real check, because the digest pins the ceiling before
launch.

**Recommendation:** spinclass designed and prefers Shape P (its R3' check
reads the profile's own report, and the spawn grant's digest pins the
table), and with the linked-library fact there is no spinclass flag day;
the remaining cost is clown's pin re-approval, a one-time operator act the
protocol-2 bump already exercised. So: **Shape P**, unless the operator
wants to avoid the pin re-approval now, in which case Shape S is the
cheaper interim and P follows. Either way the ceiling text (item 1) and the
stanza (item 2) are the same.

### 5. Prerequisites outside clown

| Who | What | Status |
|---|---|---|
| spinclass | under Shape P: `catalogWidenings` and `checkConfinedToolSurface` on the `moxy` table, after relinking clown at the bump (light-cedar files the spinclass side once the bump and field name are announced); under Shape S: the spec field and `SpecVersion` 2 | answered 2026-11-03; spinclass work waits on the shape decision |
| operator (sweatfile) | the `[folio]` stanza of item 2 in the global or parent layer, checked with `sc validate` | pending |
| moxy | a deployed moxy whose `serve-mcp` honours `MOXY_PARENT_MOXYFILE` and `permission-mode = "enforce"` (the build here, 0.6.46+7edf873, ships `moxy narrows` and the NARROWING contract), plus the moxy#497 env guards; the lock bump is the operator's, not this session's | to confirm with moxy/sunny-sumac |
| operator | this package; the `worker` profile regaining `moxy/moxy` (a pinned-profile change) | pending |

## Build steps (clown), in order

1. **Ceiling writer** (`internal/*/agentscope/ceiling.go`): a pure function
   `CeilingMoxyfile(allow []string) []byte` rendering the moxyfile above
   from a list, plus `IntersectAllow(def, requested)` (narrowing only,
   refuses a widening with the offending entries named). Unit tests pin
   the bytes and the refusal.
2. **Parent file** (`cmd/clown/agentscope.go`, `startFrontendEndpoints`):
   write the seed moxyfile under the unit's socket dir, render the parent
   with `moxy show-effective-moxyfile` run with `XDG_CONFIG_HOME` pointed
   at a scratch dir holding only the seed and `MOXIN_PATH` set to the
   pinned list (`trustedMoxinPath`, `cmd/clown/frontendmoxin.go`), write
   the output beside it; bind both read-only into the unit (the child over
   `$HOME/.config/moxy/moxyfile` as today, the parent at a fixed path). The
   in-unit moxy server's env gets `MOXY_PARENT_MOXYFILE=<parent>` and
   `MOXIN_PATH=<pinned>` (`reserveInUnitServers`, `cmd/clown/main.go`).
3. **Self-check at launch:** `moxy narrows --parent <parent> --child
   <child>` must exit 0, else the launch is refused with moxy's lines; a
   moxy on PATH without `narrows` refuses too (an older moxy cannot
   enforce). Tests with a fake `moxy` on PATH (exit 0 / exit 1 with lines /
   missing subcommand).
4. **Default ceiling:** `InUnitMoxyfile` becomes the rendered default
   (item 1's list, `enforce`, no `restart`). The existing pin test on its
   content changes with it.
5. **Shape S plumbing** (if chosen): `Spec.MoxyAllow []string`,
   `SpecVersion` 2 accepted alongside 1 (a version-1 spec means "the
   default ceiling"); `Validate` refuses an entry outside the default; and
   the record-back spinclass requires: `Result.MoxyfileDigest` (sha256 of
   the mounted read-only moxyfile bytes) and `Result.MoxyTools` (the
   effective tool list clown derived), written with the "provider
   started" result, so spinclass can assert armed == asked. Under Shape P
   the same two result fields are still worth writing: they let spinclass
   compare the launch against the profile's `moxy.tools` it already pinned.
6. **Exclusion push** stays as today (`applyInUnitExclusions`): the
   profile's `context_excluded` is a deny-list within the ceiling; the
   readback refusal is unchanged.
7. **Profiles:** `worker` and `canary-spinclass` regain `moxy/moxy` in
   `context_servers` with the saved exclusions, only after spinclass's R3
   change lands (the "worker must drop moxy whole" test flips with it).
   This is the pinned-profile change the operator ratifies.
8. **Bats (`verify-agent-scope`):** one confined launch with the real moxy
   in the unit: `moxy narrows` reported 0; `folio_write` under the worktree
   and `.tmp` succeeds; a write outside is denied with a tool error naming
   "unmatched"; `rg_search` and `folio_ls` work; no `grit_*` tool is
   listed; `restart` is absent; a child moxyfile planted in the worktree
   (`.moxy/moxyfile` adding a tool) does not widen the list (moxy refuses
   at reload, logged).
9. **Docs:** FDR 0020 (the moxy consumer row: in-unit under a parent-
   enforced ceiling; the "exclusions are policy" paragraphs stay true; the
   "What a confined worker loses" rows for folio/rg flip), the structural
   note's status, `2026-10-26-confined-moxy-sequencing.md` line 41.

Not in V3: a frontend or twin moxy (withdrawn), a per-session moxy shared
by children, per-tool add-back in a profile (spinclass#383).

## Announcement text for spinclass (sent when the shape is chosen)

Shape S: "clown will accept FDR 0035 spec version 2 with one new optional
field, `moxy_allow` (array of moxy allow-tools entries); a version-1 spec
keeps meaning the default ceiling; clown refuses an entry the default
does not hold. No profile protocol change."

Shape P: "clown will bump `pkg/profilelookup.ProtocolVersion` to 3: new
canonical key `moxy` after `base`, an object `{placement, allow_tools,
tools}` (sorted unique arrays) or null when the profile has no `[profile.moxy]`
table; registry stamp 3 only for profiles using it; `OldestReadableProtocol`
1. Every profile digest changes with the protocol number (the operator
re-approves kept pins). spinclass relinks clown at or after the bump
commit on its own release cycle; until then it sees no table and R3 holds.
`catalogWidenings` should compare the table (narrow-only against the
base)."

Tool subsets (item 1): no objection from the broker side (light-cedar):
every path argument of the kept tools is judged per resource against the
stanza; `mktemp` creates under `.tmp`, inside the worktree; the excluded
tools are exactly those whose second argument or effect is not a plain
path check.

## Order and gates

1. Operator ratifies items 1 to 4 (one decision) and names the shape.
2. spinclass lands the stanza default, R3', and (Shape S) the spec field.
3. moxy on the hosts is at a rev that enforces the parent (operator's lock
   bump); until then step 3's self-check refuses the launch, by design.
4. clown builds steps 1 to 6 and 8 to 9, merges (targets=[]), then step 7
   with the operator's profile change.
5. One live confined spawn on `worker` (the bats case run by the operator
   or a session with a user manager), then the clown#339 symptom is
   re-checked with posh/prime-olive's task.
