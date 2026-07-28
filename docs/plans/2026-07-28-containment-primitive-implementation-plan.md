# Containment Primitive (Structural Fix) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Make #205's bug class unrepresentable by bundling argv+env into one `Command` value, and collapse seven scattered per-launch temp dirs into one staging root — so a future container locus needs one mount rather than an unbounded search.

**Architecture:** Two structural changes plus a design record. No `Placement` interface is built (see the design doc for why: one live locus, and a dormant second implementation rots). `Command` puts the environment in front of the executor that knows it is crossing a boundary; the staging root gives that executor a single thing to expose.

**Tech Stack:** Go, Nix flakes. `just build` is the authoritative check.

**Rollback:** `Command` is a mechanical signature change with no behavioral fork — rollback is `git revert`. The staging root gets `CLOWN_STAGING_ROOT=tmpdir`, which forces the root's base back to `$TMPDIR`, so an artifact-placement regression is one env var from being ruled out. (This wording originally said "restoring today's scattering"; see the design doc's corrected Rollback section for why that is not what the hatch does or should do.) The tent guard is deliberately NOT rollback-gated: it converts a silent misconfiguration into a loud error, and reverting it would restore a bug.

**Design record:** `docs/plans/2026-07-28-containment-primitive-design.md`. Read it first — particularly the "verified vs assumed" table, and the retraction section: the tent `appendFile` theory that table originally carried turned out to be false, and was withdrawn during Task 5.

---

## Before you start

1. **`just build` is the only fully-trustworthy check** (clown#174). `go test ./cmd/clown/` can report "inconsistent vendoring" for reasons unrelated to your change; use `just test-go`, or `just zz-explore/run-go-test ./internal/foo/` for one package. `internal/pluginhost` and `internal/provider` don't import ringmaster and are safe to test directly; `cmd/clown` is not.
2. **`nix build` reads the git-tracked snapshot.** `git add` new files before building or you get a misleading "undefined: X".
3. **`just test-go` picks up uncommitted tracked edits** — verified this session by deliberately breaking an assertion and watching the lane fail.
4. Format with `nix fmt`; `just lint` checks it.

---

## Task 1: launch-plan seam (characterization before refactoring)

**Promotion criteria:** N/A — additive, and outlives this refactor.

This must land FIRST. It captures current behavior so the refactor has something to be measured against.

**Files:**
- Create: `cmd/clown/launchplan.go`, `cmd/clown/launchplan_test.go`
- Modify: `cmd/clown/main.go` (flag parsing; `runProvider`)

**Step 1: Write the failing test**

```go
func TestLaunchPlanJSON_IsDeterministic(t *testing.T) {
	p := launchPlan{
		Binary: "/nix/store/xxx/bin/claude",
		Args:   []string{"--plugin-dir", "/a", "--resume"},
		Env:    []string{"B=2", "A=1"},
		Files:  []string{"/stage/opencode.json", "/stage/prompt.txt"},
	}
	got, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	// Env and Files MUST be sorted: map/dir iteration order would otherwise
	// make the golden fixtures flap.
	want := `{"binary":"/nix/store/xxx/bin/claude","args":["--plugin-dir","/a","--resume"],"env":["A=1","B=2"],"files":["/stage/opencode.json","/stage/prompt.txt"]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
```

**Step 2:** `just zz-explore/run-go-test ./cmd/clown/` → FAIL, `undefined: launchPlan`.

**Step 3: Implement**

```go
// launchPlan is the fully-resolved provider invocation, dumped by
// --print-launch-plan instead of being executed. It exists so the launch path
// — which is nearly pure (flags + config -> a command) — can be
// characterization-tested without spawning anything.
//
// Args order is significant and preserved. Env and Files are SORTED: both
// derive from map or directory iteration upstream, so unsorted output would
// make golden fixtures flap for reasons unrelated to the code under test.
type launchPlan struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
	Env    []string `json:"env"`
	Files  []string `json:"files"`
}

func (p launchPlan) JSON() ([]byte, error) {
	sort.Strings(p.Env)
	sort.Strings(p.Files)
	return json.Marshal(p)
}
```

**Step 4:** Test passes.

**Step 5: Wire the flag**

Add `--print-launch-plan` to `parseFlags` (mirror `--plugin-dir`'s parsing). In `runProvider`, when set: build the plan from `binary`, `argv`, `extraEnv`, and the staging root's contents; print `p.JSON()` to stdout; return 0 **without spawning**.

Redact secrets: `Env` entries whose key matches `(?i)(TOKEN|KEY|SECRET|PASSWORD)` must have their value replaced with `<redacted>`. The plan is written to golden files in git — a real API key must never land there. Add a test for this specifically.

**Step 6: Capture goldens**

```bash
just build
# For each of: claude (inline), opencode, crush
./result/bin/clown --provider opencode --print-launch-plan -- --version \
  > zz-tests_bats/golden/launch-plan-opencode.json
```

Stage them under `zz-tests_bats/golden/`, and add a bats test per provider asserting the current binary reproduces them. **These goldens encode PRE-refactor behavior — that is the point.**

**Step 7: Commit**

```bash
git add cmd/clown/launchplan.go cmd/clown/launchplan_test.go cmd/clown/main.go zz-tests_bats/golden/
git commit -m "clown: add --print-launch-plan seam and capture golden launch plans (#205)"
```

---

## Task 2: `internal/staging` — one root per launch

**Promotion criteria:** Remove `CLOWN_STAGING_ROOT=tmpdir` after one release with no artifact-placement reports across claude, opencode, crush.

Must live in `internal/` because `internal/provider` and `internal/pluginhost` both need it — not in `package main`.

**Files:**
- Create: `internal/staging/staging.go`, `internal/staging/staging_test.go`

**Step 1: Write the failing tests**

```go
func TestRoot_DirAndFileLandUnderRoot(t *testing.T) {
	r, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	d, err := r.Dir("plugin-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d, r.Path()) {
		t.Errorf("Dir() = %q, want under %q", d, r.Path())
	}

	f, err := r.File("prompt-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !strings.HasPrefix(f.Name(), r.Path()) {
		t.Errorf("File() = %q, want under %q", f.Name(), r.Path())
	}
}

// The property a future Placement.File() depends on: ONE directory to expose.
func TestRoot_CloseRemovesEverything(t *testing.T) {
	r, _ := staging.New(t.TempDir())
	d, _ := r.Dir("plugin-")
	root := r.Path()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d, root} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%q survived Close()", p)
		}
	}
}

func TestRoot_CloseIsIdempotent(t *testing.T) {
	r, _ := staging.New(t.TempDir())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}
```

**Step 2:** `just zz-explore/run-go-test ./internal/staging/` → FAIL (package missing).

**Step 3: Implement**

```go
// Package staging owns the single per-launch directory that every artifact
// clown generates for a provider lives under.
//
// The point is not tidiness. Before this package, seven call sites each made
// their own os.MkdirTemp(""), which meant "make clown's artifacts visible to
// the provider" was an unbounded problem — and it is exactly why the clownbox
// path resorts to overwriting $TMPDIR globally and why a container locus has
// no answer at all (clown#205). With one root, exposure is ONE mount.
package staging

type Root struct {
	dir    string
	closed bool
}

// New creates a launch staging root under base. An empty base uses $TMPDIR
// (os.MkdirTemp's default), preserving today's placement.
func New(base string) (*Root, error) { /* os.MkdirTemp(base, "clown-launch-*") */ }

func (r *Root) Path() string { return r.dir }

// Dir creates a subdirectory for one artifact group.
func (r *Root) Dir(prefix string) (string, error) { /* os.MkdirTemp(r.dir, prefix) */ }

// File creates a file under the root. pattern follows os.CreateTemp.
func (r *Root) File(pattern string) (*os.File, error) { /* os.CreateTemp(r.dir, pattern) */ }

// Close removes the root and everything beneath it. Idempotent, so callers can
// both `defer Close()` and close early on an error path.
func (r *Root) Close() error
```

**Step 4:** Tests pass. **Step 5:** Commit.

---

## Task 3: migrate the seven artifact writers

**Promotion criteria:** N/A.

Do these **one call site per commit** — a mis-migrated writer produces a path that exists but is in the wrong place, which is quiet, and batching hides which one did it.

**Files (one per commit, in this order — least to most entangled):**

1. `internal/pluginhost/compile.go:83` — `CompilePluginDir` gains a `*staging.Root` param.
2. `cmd/clown/jobmonitor.go:138` — `synthJobMonitorPluginDir`.
3. `cmd/clown/jugglermonitor.go:38` — `synthJugglerPluginDir`.
4. `cmd/clown/opencode.go:411` — replaces its own `mkdtemp` + `defer os.RemoveAll`.
5. `cmd/clown/crush.go:426` — same. Note: `crushDataDir` is **not** staging — it is deliberately stable and persistent (sessions live there). Leave it alone.
6. `internal/provider/claude.go:64` — the prompt-append file.
7. `internal/provider/codex.go:27` — same for codex.

For each: add the `*staging.Root` parameter, replace the `os.MkdirTemp("")`/`os.CreateTemp("")` call, delete the now-redundant `defer os.RemoveAll` (the root owns cleanup), update callers and tests.

**After each commit:** `just test-go`, and re-run the Task 1 goldens. The goldens' `files` array will legitimately change (paths move under the root) — update the fixture in the SAME commit and say so in the message, so a reviewer can see the move was intended rather than incidental.

---

## Task 4: `Command` — argv and env travel together

**Promotion criteria:** N/A.

**Acceptance criterion added during Task 1 review:** collapse the
`runWithPluginHost` → `runManaged`/`runUnbound` → `runProvider` positional tail.
`runWithPluginHost` already receives `flags parsedFlags` and then unpacks five
of its fields into separate positionals to hand down the same chain
(`ptyOpts`, the cheap-context trio, and now `printLaunchPlan`), leaving
`runManaged` at 13 parameters. Task 1's reviewer flagged that deferring this is
correct *only if* this task actually absorbs it — otherwise the next diagnostic
flag makes it 14 and the case for deferring weakens each time. Thread `flags`
itself, or fold the tail into the new `Command`/options struct.

**Files:** `cmd/clown/pluginbinding.go`, `cmd/clown/main.go`, `cmd/clown/pluginbinding_test.go`

**Step 1: Write the failing test**

```go
// The #205 regression test. An executor that rewrites argv MUST be handed the
// env too — that is the whole point of the type.
func TestExecutor_FormatArgsReceivesEnv(t *testing.T) {
	e := &recordingExecutor{}
	_, err := e.FormatArgs(Command{
		Args: []string{"--version"},
		Env:  []string{"OPENCODE_CONFIG=/stage/opencode.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.gotEnv) != 1 || e.gotEnv[0] != "OPENCODE_CONFIG=/stage/opencode.json" {
		t.Errorf("executor did not receive env: %v", e.gotEnv)
	}
}
```

**Step 3: Implement**

```go
// Command is a fully-formed provider invocation. Args and Env travel together
// because any locus that rewrites one must rewrite the other; keeping them in
// separate parameters is precisely how clown#205 happened — argv went through
// Executor.FormatArgs (so tent rewrote it) while env went straight to
// runProvider (so tent did not).
type Command struct {
	Args []string
	Env  []string // additional entries; empty means inherit unchanged
}
```

- `bindResult` collapses into `Command` (keep the name `bindResult` only if it reads better at the `Bind` call site; otherwise return `Command` directly).
- `Executor.FormatArgs(Command) (Command, error)`.
- `runProvider(executor Executor, cmd Command, logger *slog.Logger, ptyOpts ptysuspend.Options) int`.
- `directExecutor`/`passthroughExecutor` pass `Env` through unchanged — correct, since neither crosses a namespace.

**Step 4:** `just test-go`; goldens must be **byte-identical** here. This task changes plumbing only. If a golden moves, stop — you changed behavior.

**Step 5:** Commit.

---

## Task 5: the tent guard

**Promotion criteria:** N/A — not rollback-gated (see header).

**Files:** `cmd/clown/main.go` (`tentExecutor.FormatArgs`), `cmd/clown/main_test.go`

**Step 1: Write the failing test**

```go
// tent cannot currently deliver clown-generated artifacts into the container:
// bindResult.Env would land on the podman process rather than the agent, and
// the staging root is not mounted. Fail loudly rather than launching a
// provider that silently sees no config (clown#205).
func TestTentExecutor_RejectsUntranslatableEnv(t *testing.T) {
	e := &tentExecutor{ /* minimal opts */ }
	_, err := e.FormatArgs(Command{
		Args: []string{"--version"},
		Env:  []string{"OPENCODE_CONFIG=/stage/opencode.json"},
	})
	if err == nil {
		t.Fatal("expected an error for env tent cannot translate")
	}
	if !strings.Contains(err.Error(), "OPENCODE_CONFIG") {
		t.Errorf("error should name the offending variable: %v", err)
	}
}

func TestTentExecutor_EmptyEnvIsFine(t *testing.T) {
	// The claude path passes no extra env; tent must keep working for it.
}
```

**Step 3: Implement** — return an error naming the variables and pointing at the design doc, e.g.:

> `tent cannot deliver clown-generated config to the container (env OPENCODE_CONFIG would be set on the container runtime, not the agent, and the staging root is not mounted); see docs/plans/2026-07-28-containment-primitive-design.md`

**Step 5:** Commit.

---

## Task 6: drop the clownbox `TMPDIR` hack

**Promotion criteria:** N/A.

`runClownbox` (`cmd/clown/main.go`) currently `os.Setenv("TMPDIR", <repoRoot>/.tmp)` with a deferred restore, purely so `BuildClaudeArgs`' temp files land inside the repo bind-mount.

With Task 3 done, that becomes `staging.New(filepath.Join(repoRoot, ".tmp"))` — configuration instead of a process-global mutation with a restore hazard. Delete the `os.Setenv`/`defer` block.

Add a test asserting the clownbox staging root is under the repo root.

---

## Task 7: rollback escape hatch

**Promotion criteria:** remove after one release with no artifact-placement reports across claude, opencode and crush.

**Files:** `cmd/clown/main.go`, `cmd/clown/staging_test.go`, `man/man1/clown.1`

**The original wording of this task was stale and was NOT implemented literally.** It said `CLOWN_STAGING_ROOT=tmpdir` should leave artifacts "scattered as before". After Task 3 that is not a state the code can be in — every writer takes a `*staging.Root` — and a hatch promising it would need a dead second code path.

What was implemented instead: the variable **forces the staging root's base to the default (`$TMPDIR`), overriding whatever `stagingBaseFor` would have chosen.** That is a genuine rollback of the one decision that can go wrong in the field, since a misplaced artifact fails silently rather than erroring.

- The override lives **inside `stagingBaseFor`**, not at the `staging.New` call site: it overrides that function's decision, so keeping it there keeps the knob next to the policy and keeps the unit tests pinning what actually happens. A call-site check would be silently escaped by a second locus-specific arm added later.
- **`tmpdir` is the only accepted value.** An explicit path was rejected: it would make a lever into a configuration feature (relative vs absolute, creation, mode, clownbox's bind-mount requirement) and features are much harder to withdraw on the promotion criterion above. Empty is treated as unset.
- **An unrecognised value warns on stderr and leaves placement unchanged.** Silence would let a typo convince an operator they had rolled back when they had not; a hard error would let one stale export in a shell profile break every launch.

Documented in `clown(1)`'s ENVIRONMENT section — `clownfile(5)` has no such section — including the temporary status and the promotion criterion.

---

## Task 8: FDR 0017

**Files:** Create `docs/features/0017-containment-primitive.md` (0016 is the highest FDR today).

Use the `eng:fdr` skill. Content is specified in part 2 of the design doc — the five resources, tent's discovered requirements, the `Placement` sketch, the layered policy split, loci with server-mode-as-invocation-model, the exec-replacing-locus constraint, and the revival cost.

**CORRECTED during Task 5 — do not follow the instruction this replaces.** This step used to say: *"Mark the suspected bug as an unverified theory, with why it is masked inside a spinclass session."* That theory was **false**. `internal/tent/tent.go:212` sets `TmpDir: os.TempDir()` and `:347-348` bind-mounts it at the same path, so the prompt file was visible inside the container all along. Record it as a **methodological note** — a theory that shipped with a plausible explanation for its own non-observation, and thereby stopped being falsifiable by observation — not as a known issue. See the design doc's retraction section.

Left in place rather than deleted because an executed plan is a record of what was believed at the time, and this particular belief being wrong is the useful part.

Also: comment on #205 that it is now structurally addressed, and link the FDR.

---

## Out of scope

- **Building `Placement`.** One live locus; deferred until a second exists.
- **Reviving or fixing tent.** Shelved. The guard makes its limitation loud; the FDR makes revival cheap.
- **Verifying the `appendFile` theory.** Needs rootless podman and a loaded tent image, which the nix lane cannot provide.
- **`crushDataDir`.** Persistent per-project state, deliberately not staging.

## Final verification

```bash
just build      # authoritative; git add new files first
just test-go
just lint
```

Then the pre-merge attestation and `merge-this-session-async`.
