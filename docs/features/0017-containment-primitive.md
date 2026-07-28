---
status: testing
date: 2026-07-28
promotion-criteria: the `CLOWN_STAGING_ROOT=tmpdir` placement lever is removed after one release with no artifact-placement reports across claude, opencode and crush; and the `Placement` interface stays unbuilt until a second *live* locus exists (tent revived, or a remote locus becomes real)
---

# Containment Primitive — the resources a launch locus must translate

## Problem Statement

clown hands a provider several kinds of resource — a command, an
environment, files, network services, a terminal — and every one of them is
a *launcher-side handle*. When the provider runs anywhere other than as a
direct child of clown, each handle has to be translated into a handle that
means the same thing on the far side. That translation had been invented
four separate times in-tree, each solving one axis, sharing no mechanism,
and a fifth axis — per-launch artifacts under a container locus — had no
mechanism at all, which is clown#205.

This record exists because **tent is a shelved experiment that must remain
revivable**. tent is not a live feature and its functionality is not a
blocker. But it *ran*, and in running it discovered a concrete requirement
set, which makes those requirements recorded fact rather than speculation.
The deliverable is therefore not "keep tent working" — it is: capture what
tent proved is necessary, so that reviving it, or adding a remote or server
locus, means implementing one interface against a written requirement set
rather than re-deriving four translation axes from scratch.

## The five resources a locus must translate

A **locus** is where a launched process runs. The inline locus (a direct
child of clown) is the degenerate case: every handle already means the same
thing on both sides, so nothing needs translating. Every other locus has to
translate all five of these.

| Resource | Inline locus | Container locus (tent), today |
|---|---|---|
| argv | passed through unchanged | `Executor.FormatArgs` rewrites it into `podman run … <image> <inner-binary> <args>` (`tentExecutor.FormatArgs` → `tent.BuildArgs`) |
| environment | `Command.Env` appended to `os.Environ()` in `runProvider` | **no mechanism.** Ambient vars cross via tent's `DefaultEnvPassthrough`; clown-generated ones do not, and `tentExecutor.FormatArgs` now refuses them |
| `$PATH` (special case) | inherited | `tent.RewritePathToNixStore` — resolve each entry through symlinks, keep it only if it canonicalises under `/nix/store/`, drop the rest |
| files | `internal/staging.Root` — one directory per launch, placed by `stagingBaseFor` | `binds.go`'s static allowlist (ambient state), plus a blanket `$TMPDIR` bind that covers per-launch artifacts **by accident** |
| network services | plugin host binds `127.0.0.1:<ephemeral>`; that URL goes straight into the compiled manifest | `pluginhost.Host.URLHostRewrite`, set by `pluginURLHostFor`: `host.containers.internal` on darwin, empty on linux (`--network=host` shares the namespace) |
| tty | `ptysuspend` pty proxy, gated by `supportsPtySuspend` | podman `-i` always, `-t` only when `opts.Tty`, itself gated on `stdioIsTerminal()`; tent is *excluded* from the pty proxy because podman manages its own pty |

**Four independent inventions of the same idea is the evidence that the
abstraction is real.** Before this work those four were `FormatArgs`,
`URLHostRewrite`, `RewritePathToNixStore`, and `runClownbox`'s
`os.Setenv("TMPDIR", <repo>/.tmp)` redirect. They share no type, no call
site, and no test. The fourth has since been replaced by `stagingBaseFor`;
the recurrence is what belongs in the record, not the specific survivors.

`$PATH` is listed separately from the rest of the environment because it is
the one variable whose *value* is a list of launcher-side paths. Passing it
through verbatim is not a translation — the entries have to resolve into a
filesystem the locus actually shares, which is why tent canonicalises them
into `/nix/store` and drops everything that does not land there.

## Interface

What landed is a structural fix, not the interface. Three things are
observable.

**`internal/staging.Root` — one staging directory per launch.** Every
artifact clown generates for a provider (compiled plugin dirs, synthesised
job-monitor and juggler plugin dirs, the opencode and crush config dirs, the
claude prompt-append file, the codex instructions file) is created under a
single `clown-launch-*` directory, created early and removed on exit.
Previously seven call sites each made their own `os.MkdirTemp("")`. `Close`
is terminal — `Dir`/`File` return `ErrClosed` afterwards and `Path` goes
empty — because an artifact created under a closed root would sit outside
both the root's cleanup and any mount a locus derived from `Path`.

Where the root goes is decided by `stagingBaseFor`: `$TMPDIR` for every
provider except clownbox, whose sandbox gives the container a fresh `/tmp`
and mounts only the repo, so its root goes in `<repo>/.tmp`.

**`Command` — argv and env travel as one value.**

```go
type Command struct {
	Args []string
	Env  []string // additional entries; empty means inherit unchanged
}
```

`pluginBinding.Bind` returns one; `Executor.FormatArgs(Command) (Command,
error)` takes and returns one; `runProvider` consumes one.

Be precise about what this buys. **Bundling does not force correctness** —
an executor can still pass `Env` straight through, and `directExecutor` and
`passthroughExecutor` do exactly that, correctly, since neither crosses a
namespace. What it buys is that the environment is now *in front of* the one
component that knows it is crossing a boundary. That is the difference
between a component that can be wrong and a component that never sees the
data at all: under the old shape argv went through `FormatArgs` (so tent
rewrote it) while env went straight to `runProvider` (so tent never saw it),
which is precisely how #205 happened.

**`tentExecutor` refuses env it cannot translate.** Given a non-empty `Env`
it returns an error naming the offending variables and pointing at the
design doc. The reasoning is split deliberately: the env half is
unconditional (a var set on the container runtime never reaches the agent
inside it), while the file half is conditional, so the error says the
staging root is *not guaranteed* to be mounted rather than *not mounted* —
the stronger claim would be false in the default configuration.

**`CLOWN_STAGING_ROOT=tmpdir`** pins the staging root's base to `$TMPDIR`,
overriding `stagingBaseFor`'s choice. It is a rollback lever, not a
configuration feature; see Tuning Levers.

**`--print-launch-plan`** dumps `{binary, args, env}` as JSON and exits
without spawning, with values of keys matching
`(?i)(TOKEN|KEY|SECRET|PASSWORD)` redacted. It exists so the launch path —
which is nearly pure — can be characterisation-tested, and it backs golden
fixtures for claude, opencode and crush.

## Examples

Inspect a launch without running it:

    $ clown --provider opencode --print-launch-plan -- --version
    {"binary":"/nix/store/…/bin/opencode","args":["--version"],
     "env":["OPENCODE_CONFIG=/tmp/clown-launch-1a2b/opencode.json"],"files":[]}

Rule out artifact placement as the cause of a regression:

    $ CLOWN_STAGING_ROOT=tmpdir clown --provider crush

    # a typo is neither silent nor fatal:
    $ CLOWN_STAGING_ROOT=tmp clown
    clown: ignoring CLOWN_STAGING_ROOT="tmp"; the only accepted value is
    "tmpdir", which pins the launch staging root to $TMPDIR

Wire tent to a config-file provider and the tripwire fires:

    $ clown --tent --provider opencode
    clown: tent cannot deliver clown-generated config to the container:
    env OPENCODE_CONFIG would be set on the container runtime, not on the
    agent inside it, and nothing guarantees the staging root it names is
    mounted there; see docs/plans/2026-07-28-containment-primitive-design.md

## Tent's discovered requirements

These are what a revived tent — or any container locus — has to provide.
They are not a design; they are what running tent turned up.

**1. The bind allowlist is *ambient policy*, and that is a security
decision.** `internal/tent/binds.go` exposes a fixed read-only set:
`/nix/var` and `/etc/nix` (daemon socket, profile-link targets, daemon
config), `~/.nix-profile` and `~/.local/state/nix/profiles/profile` (so the
in-tent `$PATH` resolves), `~/.gitconfig` and `~/.config/git`,
`~/.config/nix`, and `~/.config/ssh` — deliberately *not* `~/.ssh`, so
private key material stays outside. There is **no general `$HOME` bind**;
the allowlist is the whole story. FDR 0007's C+F reasoning is why: each
entry was argued for individually, and the read-only-ness and the
`~/.config/ssh` vs `~/.ssh` split are the argument's output.

The load-bearing distinction for a future locus interface is that all of
this is **ambient state** — things that exist regardless of this launch,
whose exposure is a judgement call about what the sandboxed agent should be
able to see. **Per-launch artifacts are the opposite**: clown created them
seconds ago and knows their exact paths, and exposing them is not a
judgement call at all. Conflating the two categories is exactly why the
second one ended up with no mechanism. A locus interface must keep them
apart: mechanical primitives on one side, the policy of which ambient paths
to expose on the other.

**2. `RewritePathToNixStore`** (`internal/tent/path.go`) — the `$PATH`
translation above. FDR 0007's strategy B: realpath-rewrite, no dedup,
drop-on-failure. Its point is hermeticity — every surviving entry reaches a
real binary through the existing read-only `/nix/store` bind, with no
dependency on `$HOME` or any other host mount.

**3. `URLHostRewrite`** (`internal/pluginhost/host.go`) — rewrites only the
host portion of the MCP server URLs written into the compiled manifest,
preserving port and path. The host's own dial path (healthchecks, shutdown)
keeps using `127.0.0.1`. That asymmetry is the general shape of service
translation: the launcher and the launched process need *different* handles
for the same listener, and only the latter gets rewritten.

**4. The blanket `$TMPDIR` bind.** `internal/tent/tent.go` sets
`Options.TmpDir` to `os.TempDir()` and binds it into the container at the
same path. **This covers per-launch artifacts by accident, not by design**,
and it is worth being exact about both failure modes:

- It **stops covering them the moment the staging root moves off
  `$TMPDIR`** — which `stagingBaseFor` already does for clownbox, and which
  any future locus-specific arm is free to do again.
- It **grants the container far more than the one directory it needs**:
  everything any process on the host has left in `/tmp`, writable.

Both are arguments for a revived tent binding **the staging root
specifically** — one mount covering every artifact, which is the entire
point of the root existing. `internal/tent/tent.go`'s `TmpDir` doc comment
says this; keep the two consistent if either changes.

## The `Placement` sketch — designed, deliberately not built

```go
// NOT BUILT. Recorded so a revival implements an interface rather than
// re-deriving the axes above.
type Placement interface {
	// File makes a launcher-side path reachable at this locus and returns
	// the path the launched process must use to reach it.
	File(hostPath string) (innerPath string, err error)

	// Service makes a launcher-side listener reachable and returns the URL
	// the launched process must dial. Cf. URLHostRewrite: the launcher
	// keeps its own handle unchanged.
	Service(hostURL string) (innerURL string, err error)

	// Exec runs the Command at this locus, translating argv and env
	// together — or refusing, as tentExecutor does today.
	Exec(cmd Command) (exitCode int, err error)

	// Close releases what the locus allocated. See "exec-replacing loci"
	// for when no Close is correct.
	Close() error
}
```

`Command` already exists and is the input to `Exec`; `staging.Root.Path()`
is already the single argument a container locus would hand to `File`. The
`$PATH` and tty axes are not separate methods — `$PATH` is env, handled
inside `Exec`, and tty is a property of how `Exec` wires stdio.

**The layered split.** `Placement` is *mechanism* and nothing else. The
policy deciding which ambient paths a locus exposes — tent's `binds.go` — is
a separate layer that a locus implementation consults. Mechanism is generic
and testable; policy is a security decision that has to be reviewed per
locus. `File`/`Service` are the same code for every container regardless of
what the allowlist says; the allowlist is the same argument regardless of
what the mechanism is.

### Why it was not built

**One live locus.** Only the inline locus is exercised. A `Placement`
implementation for a shelved container would be a **dormant second
implementation**, and a dormant implementation rots silently: nothing
compiles against its behaviour, no lane runs it, and its continued existence
gives false confidence that the abstraction has been validated when in fact
it has only been typed. The abstraction earns its keep when a second *live*
locus exists.

What was built instead is the two things that make building it later cheap:
`Command` (so argv and env are already one value) and `staging.Root` (so
"expose clown's artifacts" is already one path rather than an unbounded
search).

## Loci

| Locus | Status | Notes |
|---|---|---|
| inline | live | direct child of clown. Every handle already means the same thing on both sides |
| container | shelved (tent, clownbox) | the requirement set above. clownbox is a partial case: it needs `File` (hence the `<repo>/.tmp` root) but shares the network namespace |
| remote | not built | the axis that would make `Placement` earn its keep. Adds a genuinely new problem: artifacts have to be *transferred*, not just *renamed*, so `File` returning a path implies a copy |
| server mode | **not a locus** | see below |

**Server mode is an invocation model layered on a locus, not a fourth
locus.** The reasoning is short and worth keeping: starting a server is
itself a spawn, and that spawn needs a locus. A server can be started
inline, in a container, or remotely, and once it is running the question of
where its process lives is answered by whichever locus started it. Treating
server mode as a locus would mean cross-multiplying it with the other three
and reimplementing the same five translations per cell. It belongs above the
locus layer, not beside it. (FDR 0016 phase 3 is the server-mode work.)

## Exec-replacing loci cannot have their staging reclaimed by the launcher

`runProvider`'s callers `defer root.Close()`, which is correct for every
locus that keeps clown in the process tree. It is **not** correct — and
never runs — where clown replaces itself: codex is launched via
`syscall.Exec`, so clown is gone before any deferred function fires, and the
exec'd process reads its instructions file afterwards.

This is inherent, not an oversight. There is no point at which clown *could*
correctly remove the file: any moment early enough for clown to still exist
is a moment the file is still needed. The artifacts must outlive the
launcher, and **no `Close` is correct**.

Generalise it: **any locus that replaces or outlives the launcher's process
inherits this question of who reaps.** A remote locus faces it in a sharper
form (the artifacts are on another machine, and the launcher may have
exited). A server-mode invocation faces it too — the server outlives the
command that started it. A `Placement` implementation for such a locus must
either hand ownership to the launched process, or arrange a reaper that is
not the launcher; a `Close` that quietly does nothing is the wrong answer,
because it makes the leak invisible rather than absent.
`internal/provider/codex.go` records this at the one site where it is live
today.

## Revival cost

The point of this record. Reviving tent, or adding a remote locus, is:

1. Implement `Placement` — four methods, sketched above.
2. Against the recorded requirement set in "Tent's discovered requirements":
   argv rewriting (`BuildArgs` exists), `$PATH` canonicalisation
   (`RewritePathToNixStore` exists), service rewriting (`URLHostRewrite`
   exists), and one bind of `staging.Root.Path()` in place of the blanket
   `$TMPDIR` bind.
3. Decide the ambient-exposure policy separately, using FDR 0007's C+F
   allowlist as the precedent for how that argument is made.
4. Replace `tentExecutor.FormatArgs`' refusal with a translation, and delete
   the tripwire.

What is explicitly *not* part of that cost: rediscovering that there are
five axes, rediscovering that ambient policy and per-launch artifacts are
different problems, and rediscovering that the `$TMPDIR` bind was covering
the second one by accident.

## A methodological note: a theory that explained its own non-observation

An earlier revision of the design doc claimed that tent handed claude an
`--append-system-prompt-file` path that did not exist inside the container,
reasoning that `binds.go` is a static allowlist of ambient dev state with no
per-launch-artifact mechanism.

**That claim was false.** `internal/tent/tent.go` sets
`Options.TmpDir: os.TempDir()` and bind-mounts it into the container at the
same path, so a prompt file at `$TMPDIR/clown-prompt-*.txt` has always been
visible inside the tent. The reasoning had a hole: it looked only at
`binds.go` and never at `Options`.

The reason this is recorded as *methodology* rather than as a known issue is
**why the wrong theory survived a review**. It shipped with a plausible
explanation for its own non-observation: "the bug is masked inside a
spinclass session, because spinclass puts `$TMPDIR` inside the worktree and
tent mounts the worktree." That story was coherent, it matched the observed
absence of any bug report, and it was wrong — the real explanation was
simpler and one grep away.

The lesson generalises past this bug: **a theory that comes with an
explanation for why nobody has noticed it has stopped being falsifiable by
observation.** Absence of reports can no longer count as evidence against
it, because the theory has already accounted for that absence. At that
point the only remaining check is the code, and it should be run
immediately rather than treated as confirmation. Anyone reading this record
while reviving tent should apply the same test to whatever they believe is
broken before they start fixing it.

The residual true statement is the narrow one, kept above: tent covers
per-launch artifacts by blanket-mounting all of `$TMPDIR`, not by knowing
about them.

## Limitations

- **`Placement` does not exist.** This record is a specification for
  something unbuilt. Treat the sketch as a starting point that has never
  been compiled, let alone run.
- **The container locus is unexercised.** There are no tent goldens and no
  lane runs tent; verifying the container path needs rootless podman and a
  loaded tent image, which the nix lane cannot provide. Everything said
  about tent here is read from source, not observed.
- **The tent env guard cannot fire today.** tent is only ever paired with
  `claudeBinding`, which contributes no env. It is a tripwire on the seam,
  written now precisely because it will spring the moment someone wires tent
  to a config-file provider — the exact combination #205 describes, and
  exactly when nobody would be looking for it.
- **`--print-launch-plan`'s `files` array is always empty.** Populating it
  is now a walk of `root.Path()` rather than a guess across seven temp
  dirs; it is deferred, not blocked.
- **`crushDataDir` is not staging.** It is deliberately stable, persistent
  per-project state (crush sessions live there) and is excluded from the
  root on purpose.
- **`Root.Close` is best-effort and terminal.** It marks the root closed
  even when removal fails, and it cannot protect writes through a file
  handle that outlives it: on Unix such a write lands in an unlinked inode
  and is silently lost. Callers close their handles first.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| `CLOWN_STAGING_ROOT` | present; `tmpdir` is the only accepted value | Placement is the one decision here that can fail where clown's own lanes cannot see it, because the failure is a file the provider cannot reach rather than an error. A single accepted value keeps it a lever rather than a configuration feature (relative vs absolute, who creates it, what mode, clownbox's bind requirement) — and features are far harder to withdraw than levers. Unrecognised values warn and change nothing: silence lets a typo convince an operator they rolled back when they did not, and a hard error lets one stale shell export break every launch | **Remove** after one release with no artifact-placement reports across claude, opencode and crush |
| Staging root location | one dir per launch under `$TMPDIR`; `<repo>/.tmp` for clownbox | matches the pre-migration default; container loci override to a mounted path | a locus needs the root somewhere specific — clownbox already does, and a revived tent binding the root instead of all of `$TMPDIR` would be the second |
| Staging root lifetime | removed on clown's exit; never reclaimed on exec-replacing paths | artifacts are per-launch by definition; the exec case is inherent, not a bug | a debugging need for post-mortem inspection → add a keep flag |
| Build `Placement` at all | deferred | one live locus invites over-design, and a dormant implementation rots silently | **a second *live* locus**: tent revived, or a remote locus becomes real |

## More Information

- `docs/plans/2026-07-28-containment-primitive-design.md` — the approved
  design this record discharges, including the full retraction and the
  verified-vs-assumed table
- `docs/plans/2026-07-28-containment-primitive-implementation-plan.md` — the
  eight tasks as executed
- FDR 0006 — Single-Entrypoint Matrix (harness × provider × model × API)
- FDR 0007 — tent; its C+F bind allowlist is the ambient-policy precedent,
  and its 2026-05-19 PATH-construction matrix is where strategy B was chosen
- FDR 0016 — Non-Claude Provider Parity; phase 1 introduced the config-file
  delivery path that exposed the gap, and phase 3 is the server-mode work
- clown#205 — the bug class this makes unrepresentable
- clown#207 — the tent-removal issue, closed when the decision reversed
