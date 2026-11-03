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

Proposed for the confined-spawn default: one trusted write entry for
`spinclass:worktree`; read entries for the worktree, `/nix/store` and the
madder read-write store (moxy writes large tool outputs there); nothing
else. **Pending:** the exact stanza text as spinclass-sweatfile(5) accepts
it, and what spinclass's current default already contains, from
light-cedar.

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
rule).** A new profile field `moxy_allow` (TOML `moxy_allow = [...]`, the
dotted tool list) names the ceiling; the trusted sweatfile may only narrow
it; the effective list is the intersection. This is a `ProtocolVersion`
bump (2 to 3) in `pkg/profilelookup`, announced to spinclass first:

- canonical form: a new key `moxy_allow` after `base`, a sorted,
  deduplicated array, `null` when unset; `protocol` becomes 3;
- registry stamp: `Save` stamps 3 only when a profile uses `moxy_allow`
  (as protocol 2 does for `base`), so an untouched registry stays readable
  by protocol-2 clowns; `OldestReadableProtocol` stays 1;
- **cost:** `protocol` is part of every canonical form, so the bump changes
  every profile digest, builtin and user. Every pinned session's profile
  gate then sees a mismatch at its next resume or restart and needs the
  operator's approval (`CLOWN_PROFILE_DIGEST`), and a spinclass built
  against protocol 2 fails closed (`SkewError`) on a registry stamped 3
  until it relinks clown. That is a flag day: spinclass relinks, clown
  merges the bump, the operator re-approves the pins they keep;
- spinclass: `catalogWidenings` compares `moxy_allow` (a user profile may
  only narrow its builtin's); R3' reads the field for the in-unit moxy
  check.

**Shape S, spec field (no profile protocol change).** spinclass puts the
ceiling in the launch spec (`moxy_allow` in the FDR 0035 spec, from its
trusted sweatfile layers, the way `egress_allow` is passed); clown writes
the moxyfile from it, intersected with the compiled-in default (narrowing
only), and refuses a spec that would widen the default. This bumps the
spec version (`SpecVersion` 1 to 2; `LoadSpec` refuses unknown fields),
which is a clown-spinclass contract with no profile digest involved: no
pin re-approval, no `SkewError`. R3' needs no profile report: spinclass
knows the ceiling it passed. The profile keeps `moxy/moxy` with its
exclusions as the deny-list within the ceiling.

**Recommendation:** Shape S for v1. It puts the ceiling where the #339
ownership table already puts "what a tool may touch" (spinclass's trusted
layers), avoids invalidating every profile pin, and needs one spec field
on each side. Shape P can follow if the operator wants a profile to carry
its own ceiling; the structural note's R3' discussion then applies.

### 5. Prerequisites outside clown

| Who | What | Status |
|---|---|---|
| spinclass | the `[folio]` default stanza text (item 2); under Shape S the spec field and `SpecVersion` 2; under Shape P `catalogWidenings` and R3' on the new field; R3 relaxed so a confined spawn may keep `moxy/moxy` when the ceiling is the pinned one | pending (asked light-cedar 2026-11-03) |
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
   default ceiling"); `Validate` refuses an entry outside the default.
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
canonical key `moxy_allow` after `base` (sorted unique array or null),
registry stamp 3 only for profiles using it, `OldestReadableProtocol` 1.
Every profile digest changes with the protocol number; spinclass must
relink clown at or after the bump commit before any registry is stamped
3, and the operator re-approves kept pins. `catalogWidenings` should
compare `moxy_allow` (narrow-only against the base)."

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
