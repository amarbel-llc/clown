# Containment Primitive for Harness/Provider Tuples — Design

Date: 2026-07-28
Status: approved (brainstorm with Sasha, session ready-dogwood)

Related: #205 (reopened), #207 (closed — tent removal reversed), FDR 0006
(harness × provider matrix), FDR 0007 (tent), FDR 0016 (non-claude provider
parity)

## Problem

clown hands a provider several kinds of resource — a command, files, network
services — and when the provider runs somewhere other than "as a direct child
of clown", every one of those has to be translated from a clown-side handle
into a provider-side handle. That translation has been invented four separate
times, each solving one axis, sharing no mechanism:

| Axis | Existing mechanism |
|---|---|
| argv | `Executor.FormatArgs` — tent rewrites into `podman run … <inner> <args>` |
| network | `pluginhost.Host.URLHostRewrite` — `127.0.0.1` → `host.containers.internal` |
| `$PATH` | `tent.RewritePathToNixStore` — profile-links → canonical `/nix/store` paths |
| files | `runClownbox` redirects `TMPDIR` into the repo bind-mount — clownbox only |

Four independent inventions of the same idea is the evidence that the
abstraction is real. The gap is the fifth case that got no mechanism at all:
**per-launch artifacts under a container locus**. FDR 0016 phase 1 added a
config-file delivery path whose `bindResult.Env` is applied by `runProvider`
to the process it spawns — which under tent is the container runtime, not the
agent. That is #205.

**Correction (2026-07-28, during Task 5).** An earlier revision of this
document also claimed the generated config "sits in a host temp dir that is
never mounted". That is **false in the default configuration**:
`internal/tent/tent.go:212` sets `TmpDir: os.TempDir()` and `:347-348` binds it
into the container at the same path, so anything under `$TMPDIR` — including a
staging root left at its default — *is* visible inside the tent, by blanket
coverage rather than by design.

The env half of #205 is the real, unconditional defect: an env var set on the
container runtime does not reach the agent inside it, no matter where the file
sits. The file half is *conditional* — it bites once the staging root moves off
`$TMPDIR`, which `stagingBaseFor` already does for clownbox. That distinction
matters for a future `Placement`: blanket-mounting `$TMPDIR` is not a design,
it is an accident that happens to cover the case, and it grants the container
far more than the one directory it needs.

## Context that shapes the answer

**tent is a shelved experiment that must remain revivable.** It is not a live
feature to protect, and its functionality is explicitly not a blocker. But it
*ran*, and in running it discovered a concrete requirement set. That makes it
the opposite of speculative: the requirements are recorded fact.

So the deliverable is not "keep tent working". It is: **capture what tent
proved is necessary, so reviving it means implementing one interface rather
than re-deriving four translation axes.**

## Decisions

1. **Approach B** — structural fix now, full design recorded for later. Do
   *not* build the `Placement` interface yet. A dormant second implementation
   rots silently and gives false confidence; the interface earns its keep when
   a second *live* locus exists (remote, or tent revived).
2. **Scope of loci** (recorded, not built): inline, container, remote, and
   server mode. Server mode is an **invocation model layered on a locus**, not
   a fourth locus — starting a server is itself a spawn that needs a locus.
3. **Layered policy** — mechanical primitives (`File`/`Service`) stay separate
   from the policy deciding *which* ambient paths a locus exposes. tent's
   bind allowlist is policy (a security decision, FDR 0007's C+F reasoning),
   not mechanism.
4. **Full staging-root migration**, not a partial one. A half-migrated seam
   has the appearance of a single exposure point without the property, which
   is worse than none.

## Part 1a — argv and env become one value

```go
// Command is a fully-formed provider invocation. Args and Env travel together
// because any locus that rewrites one must rewrite the other; keeping them
// separate is precisely how #205 happened.
type Command struct {
	Args []string
	Env  []string // additional entries; empty means inherit unchanged
}
```

- `bindResult` collapses into `Command`.
- `Executor.FormatArgs(Command) (Command, error)` replaces the argv-only form.
- `runProvider` takes a `Command` rather than `args` plus `extraEnv`.

**What this does and does not buy.** Bundling alone does not force
correctness — an executor could still pass `Env` through untouched. What it
buys is that the environment is now *in front of* the one component that knows
it is crossing a boundary. `tentExecutor` therefore returns an error when
handed a non-empty `Env` it cannot translate.

That is the cheap guard from #205, finally in the right place: the executor
that knows it is containerizing, rather than a caller that does not. The guard
is honest about tent's shelved status — it fails loudly instead of pretending
to support a combination nobody has verified.

## Part 1b — one staging root per launch

Today per-launch artifacts scatter across independent temp dirs:
`BuildClaudeArgs` writes prompt-append files into `$TMPDIR`;
`runOpencode`/`runCrush` each `mkdtemp` a config dir; `synthJobMonitorPluginDir`
and `synthJugglerPluginDir` make more; `CompilePluginDir` stages another.

That scattering is *why* the clownbox `TMPDIR` hack exists and why tent has no
answer: there is no single thing to expose.

Replace with **one staging directory per launch** — created early, removed on
exit, and passed to every artifact writer instead of each calling `mkdtemp`
itself.

The point is not tidiness. It converts "make clown's artifacts visible to the
provider" from an unbounded problem into **one mount**. A revived tent adds a
single bind. The clownbox `TMPDIR` trick becomes "put the staging root inside
the repo" — configuration rather than a global `os.Setenv` with a deferred
restore.

## Part 2 — the FDR

The durable artifact, and the actual point of this work. Written as FDR 0017,
recording:

- **The five resources** a locus must translate — argv, environment (with
  `$PATH` a special case needing store-canonicalisation), files, network
  services, tty — and which of the four in-tree mechanisms solves which today.
- **Tent's discovered requirements**: the C+F bind allowlist as ambient
  policy, `RewritePathToNixStore`, `URLHostRewrite`, and the fresh-`/tmp`
  consequence that makes per-launch artifacts invisible.
- **The `Placement` sketch** — `File`/`Service`/`Exec`/`Close` over a
  `Command` — and the layered split above.
- **Loci**, with server mode recorded as an invocation model rather than a
  locus, and the reasoning for that.
- **The suspected `appendFile`-in-tent bug** (below), as a known issue for
  whoever revives tent.
- **Revival cost**: implement one interface against a recorded requirement set.

## A suspected bug — RETRACTED

An earlier revision of this document theorised that
`--append-system-prompt-file <host-tmp-path>` pointed at a path not existing
inside the container, reasoning that `binds.go` is a static allowlist of
ambient dev state with no per-launch-artifact mechanism.

**That theory was wrong, and the reasoning had a hole**: it looked only at
`binds.go` and missed `Options.TmpDir`. `internal/tent/tent.go:212` sets it to
`os.TempDir()` and `:347-348` bind-mounts it at the same path, so the prompt
file at `$TMPDIR/clown-prompt-*.txt` has been visible inside the tent all
along.

Worth recording *why* the wrong theory survived a review: it came with a
plausible explanation for its own non-observation ("masked inside a spinclass
session, because spinclass sets `$TMPDIR` inside the worktree and tent mounts
the worktree"). That story was coherent, matched the observed absence of bug
reports, and was wrong — the real reason is simpler and one grep away. A theory
that explains why nobody has noticed it is a theory that has stopped being
falsifiable by observation, and should be checked against the code rather than
against experience.

The residual true statement is narrower: tent covers per-launch artifacts by
blanket-mounting all of `$TMPDIR`, not by knowing about them. That breaks the
moment the staging root moves off `$TMPDIR` — as `stagingBaseFor` already does
for clownbox — and it grants the container far more than the one directory it
needs. Both are reasons a revived tent should bind the staging root
specifically, which is what `internal/tent/tent.go`'s `TmpDir` doc now says.

## Testing

The refactor touches the launch path for every provider, so the strategy is
characterization first:

1. **Golden launch plans for the live paths** — claude inline, opencode,
   crush. clown's launch path is nearly pure (flags + config → a command), so
   capture `{binary, argv, env, files-written}` as fixtures from the *current*
   code and require the refactor to reproduce them. This needs a seam: a
   `--print-launch-plan`-style dump that emits the plan as JSON and exits
   without spawning. That artifact outlives this refactor.
2. **The claude path is the control**, as it was for the `pluginBinding`
   extraction: its behavior must not change at all.
3. **No tent goldens.** tent is shelved; byte-for-byte fidelity is not
   required, and writing goldens before verifying the suspected bug would
   enshrine broken behavior as the contract.
4. **Unit tests for the staging root** — that every artifact writer lands
   under it, which is the property a future `File()` depends on.

## Rollback

- **Dual architecture**: `Command` is a mechanical signature change with no
  behavioral fork, so the rollback is a revert rather than a switch. The
  staging root gets an escape hatch — `CLOWN_STAGING_ROOT=tmpdir`, which forces
  the root's BASE to `$TMPDIR`, overriding whatever `stagingBaseFor` would have
  chosen — so a regression in artifact placement is one variable away from
  being ruled out.

  **Corrected during Task 7.** An earlier revision of this section, and of the
  plan, said the hatch would restore "today's scattering". It cannot, and
  should not: once every artifact writer takes a `*staging.Root`, scattering is
  not a state the code can be in, so a hatch promising it would have to be a
  second, dead code path — exactly the dormant implementation decision 1
  rejects. Placement is the one decision here that can go wrong somewhere the
  in-repo lanes cannot see, because its failure is a file the provider cannot
  reach rather than an error; that is what the lever rolls back, and nothing
  more.

  Its grammar is one value. An explicit path was considered and rejected: it
  would turn a lever into a configuration feature (relative vs absolute, who
  creates it, what mode, how it interacts with clownbox's bind-mount
  requirement) and features are much harder to withdraw than levers. An
  unrecognised value warns on stderr and leaves placement unchanged — silence
  would let a typo convince an operator they had rolled back when they had not,
  and a hard error would let one stale export in a shell profile break every
  launch.
- **Promotion criteria**: remove the escape hatch after one release with no
  reports of artifact-placement failures across claude, opencode, and crush.
- **The tent guard is not rollback-gated.** It converts a silent
  misconfiguration into a loud error; reverting it would restore a bug.

## Tuning levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| Staging root location | one dir per launch under `$TMPDIR` | matches today's default; container loci override to a mounted path | a locus needs the root somewhere specific (clownbox already wants the repo) |
| Staging root lifetime | removed on process exit | artifacts are per-launch by definition | a debugging need for post-mortem inspection → add a keep flag |
| Build `Placement` at all | deferred until a second live locus | one live consumer invites over-design | tent revived, or a remote locus becomes real |

## What is verified vs. assumed

| Claim | Basis |
|---|---|
| Four ad-hoc translation mechanisms exist in-tree | read from source |
| `binds.go` is a static ambient allowlist with no per-launch mechanism | read from source |
| `internal/tent` has no `TMPDIR` handling | searched; none found |
| `runClownbox` redirects `TMPDIR` into the repo bind-mount | read from source |
| #205's env lands on the container runtime | inferred from `runProvider` + `tentExecutor`; never executed |
| tent bind-mounts all of `$TMPDIR` into the container | read from source (`tent.go:212`, `:347-348`) |
| ~~tent's `appendFile` is broken outside spinclass~~ | **RETRACTED** — the theory was wrong; see the retraction above |

## More information

- FDR 0006 — Single-Entrypoint Matrix (harness × provider × model × API)
- FDR 0007 — tent; its C+F bind allowlist is the ambient-policy precedent
- FDR 0016 — Non-Claude Provider Parity; phase 1 introduced the config-file
  delivery path that exposed the gap, phase 3 is the server-mode work
- #205 — the type-level hazard this makes unrepresentable
- #207 — the tent-removal issue, closed when the decision reversed
