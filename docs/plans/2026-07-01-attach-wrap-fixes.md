# clownfile [attach] wrap fixes: spawn TTY gate + double selection dialog

Date: 2026-07-01
Issues: #161 (spawn TTY gate), #160 (double selection dialog)

Two independent defects in `maybeReexecMultiplexer`'s placement and gating
(`cmd/clown/attach.go`, called from `runWithFlags` in `cmd/clown/main.go`).
Both were found while investigating "the resume TUI dialog appears twice when
clown has a multiplexer set." They are conceptually distinct and land as two
commits; the spawn fix is a clean, isolated MUST-compliance fix and goes first.

## Background

`[attach]` (RFC-0013 §1.3, RFC-0014 §5) self-wraps clown in a multiplexer on
boot. The burned-in default sets `multiplexer = "posh"`, so every interactive
clown self-wraps unless a higher-precedence clownfile opts out. The wrap is a
single call to `maybeReexecMultiplexer(cf, flags, attachMode)` at one
mid-pipeline point in `runWithFlags`. That single point is simultaneously:

- **too late** for interactive selection UI — clown's own resume picker/confirm
  and profile picker run *before* it, in the outer process, then get replayed in
  the re-exec'd inner process (→ #160, double dialog); and
- **wrongly gated** for spawn — the interactive-TTY gate rejects the
  non-interactive detached context the spawn template exists for (→ #161).

## Commit 1 — spawn mode resolves its template regardless of TTY (#161)

### Problem

`maybeReexecMultiplexer` gates all three modes uniformly:

```go
if !isInteractiveTerminal() && os.Getenv("CLOWN_ATTACH_FORCE") != "1" {
    return nil // run inline
}
```

Spawn (`--clown-attach=spawn`, `ModeSpawn`) is definitionally non-interactive:
spinclass's `sc spawn` (`internal/spawn/spawn.go` `launchRendered`) runs the
worker via `exec.Command(...).Run()` with nil stdio (→ `/dev/null`, not a TTY)
and does not set `CLOWN_ATTACH_FORCE`. So the gate skips the wrap, the
`[attach].spawn` `--detach` template never resolves, the worker never detaches,
and spinclass's `cmd.Run()` blocks for the whole provider lifetime — burning the
hello-handshake deadline. Violates RFC-0014 §5.1 ("clown MUST resolve the
`[attach].spawn` template").

Currently masked: the spawn bats test forces past the gate with
`CLOWN_ATTACH_FORCE=1`, and spinclass FDR-0006 spawn legs are untested in
production.

### Change

In `maybeReexecMultiplexer`, exempt `ModeSpawn` from the interactive-TTY gate.
Start/resume keep the gate (an interactive attach genuinely needs a TTY;
`CLOWN_ATTACH_FORCE` stays their escape hatch). Spawn is detached — a TTY is
neither present nor required.

```go
// Spawn is a detached-worker launch (RFC-0014 §5): non-interactive by
// definition, so the interactive-TTY gate does not apply. Start/resume are
// interactive attaches and keep the gate (CLOWN_ATTACH_FORCE overrides).
if mode != clownfile.ModeSpawn &&
    !isInteractiveTerminal() && os.Getenv("CLOWN_ATTACH_FORCE") != "1" {
    return nil
}
```

The loop guard (`attachedID != ""`) and the disabled-mux skip stay ahead of the
gate, unchanged — a spawned inner worker that has already been wrapped must still
not re-wrap.

### Tests

- `zz-tests_bats/clownfile_attach.bats`: drop `CLOWN_ATTACH_FORCE=1` from the
  "spawn mode resolves the --detach spawn template" test so it exercises the real
  non-TTY path. (bats has no PTY, so this is the authentic spawn context.)
- `cmd/clown/attach_test.go`: add a Go unit test asserting `ModeSpawn` resolves
  (does not return nil-inline) with no TTY and no force env, while `ModeStart`
  under the same conditions returns nil-inline. Use the existing
  mux-absent-degrades seam (a nonexistent mux binary) so the exec is not actually
  performed — we assert the gate decision, not the exec.

### Verification

`go build ./cmd/clown/...`; the merge hook (`just`) runs the bats lane.

## Commit 2 — selection UI renders once under a multiplexer (#160)

### Problem

Clown's own interactive selection TUIs run in the outer process, *before* the
wrap, then the re-exec replays the **pre-selection** argv (`os.Args[1:]`) into
the mux, so the inner process re-runs them:

- `clown resume` picker/confirm — in `run()` → `runResume`, before
  `runWithFlags`. The inner replay re-runs `clown resume`, re-rendering the
  picker AND re-selecting independently of the outer's pinned id.
- profile picker — in `runWithFlags` before the wrap. The inner replay is a bare
  `clown` (no `--provider`), so the picker fires again.

The `attachedID != ""` loop guard prevents a third dialog but not the second: it
gates only `maybeReexecMultiplexer`, not the pickers upstream of it.

### Design principle (the fix frame)

The multiplexer wraps the **resolved provider session**, not the pre-launch
selection UI. Selection determines the identity that names the mux session — for
a resume, `decideClaudeSession` sets `identity.Key = <chosen session id>`, so the
mux session name is not even knowable until *after* the picker. Selection
therefore belongs *outside* the mux; only the resolved invocation belongs
*inside* it.

Concretely: replay a **resolved** argv into the mux rather than the raw outer
argv. A resolved argv carries an explicit `--provider` and, for a resume, an
explicit `--resume <id>` / `--session-id <id>`. Those are exactly the two
inner-picker suppression conditions (`providerExplicit`, and the
`--resume`/`--session-id` presence check), so a resolved-argv replay renders each
TUI exactly once — in the inner (in-mux) process — with no separate guard needed.

### Change

`maybeReexecMultiplexer` currently builds `entry` from `os.Args[1:]`. Rebuild it
from the *resolved* `flags` instead, so the spliced `{entry}` is the
post-selection command. The pieces already exist on `parsedFlags` at the call
site (main.go, after profile pick + `decideClaudeSession`):

- `flags.provider` + `flags.providerExplicit` → emit `--provider <p>`
- `flags.forwarded` → already carries the injected `--session-id`/`--resume`
  (from `decideClaudeSession`) for the claude path, plus any `--model` etc.
- other resolved top-level flags that must survive the re-exec (`--naked`,
  `--tent`, `--profile`, `--backend`, `--tent-pass-devshell`, …)

Add a `parsedFlags`-to-argv reconstruction (`func (p parsedFlags) reexecArgv()
[]string`) that emits a canonical, resolved clown argv. `maybeReexecMultiplexer`
prepends `bin, --clown-attach-id, id` and splices that as `{entry}`.

This makes the re-exec independent of `os.Args` shape: whether the user typed
`clown resume`, `clown --profile foo`, or `clown -- --resume x`, the inner
process receives one canonical resolved argv and neither picker re-fires.

**Implementation choice — settled on (a), full argv reconstruction:**

- (a) *Full argv reconstruction* — `reexecArgv()` serializes every resolved
  top-level flag. Most robust (fully decoupled from `os.Args`); enumerates the
  flag set, pinned by a unit test. **Chosen.**
- (b) *Minimal splice* — keep `os.Args[1:]` and patch it. Rejected: leaks
  `os.Args` structure, easy to get subtly wrong (equals-forms, interleaving).

(a) directly encodes "the mux wraps the resolved session" and kills a class of
argv-shape bugs.

**Regression fixed while implementing (a):** the profile picker (the bare-`clown`
path in `runWithFlags`) set `flags.provider` + `selectedProfile` but NOT
`flags.profile`. Emitting only `--provider` into the inner argv would drop the
picked profile's non-provider settings (backend/model/env/URL), which
opencode/crush consume from `selectedProfile` in-process. Fix: set
`flags.profile = selectedProfile.Name` at the picker site so `reexecArgv()`
carries `--profile <name>` and the inner re-resolves the identical profile.

### Tests

- `zz-tests_bats/clownfile_attach.bats`: new test — `clown resume` under
  `multiplexer="zmx"` (stub mux) records a mux argv whose spliced `{entry}` is the
  **resolved** `clown --provider claude ... --resume <id>` (no bare `resume`
  subcommand token), proving the inner will not re-run the picker. (Selection
  itself needs a session fixture; if a picker fixture is impractical in bats,
  cover the single-match auto-path and rely on the Go test for argv shape.)
- `cmd/clown/*_test.go`: unit-test `reexecArgv()` for the resume, profile-pick,
  and `-- --resume` cases — asserting explicit `--provider` and, for resume, the
  injected `--resume`/`--session-id` are present so the inner pickers are
  suppressed.

### Verification

`go build ./cmd/clown/...`; merge hook runs the bats lane. Manual: with the
burned-in `posh` default, `clown resume` shows the picker once.

## Non-issues confirmed during investigation (no change)

- bare `clown` / `clown -- --resume <id>`: clown renders no TUI itself; single
  wrap, single claude-native TUI.
- pty-suspend proxy: runs at the innermost `runProvider`, downstream of the wrap
  and all selection — no interaction.

## Sequencing

1. Commit 1 (#161) — spawn TTY gate. Isolated, low-risk, MUST-compliance.
2. Commit 2 (#160) — resolved-argv replay. Larger; depends on the argv-shape
   choice above.
