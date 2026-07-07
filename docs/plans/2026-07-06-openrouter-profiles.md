# OpenRouter Profiles + Profile TUI Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Make `clown --profile claude-openrouter` (and picker selection) launch claude-code against openrouter.ai's Anthropic-compatible endpoint, with a `clown profile` huh TUI to manage user profile entries, a clownfile pin key, and the `~/.config/juggler` → `~/.config/clown` migration.

**Architecture:** Extend `internal/profile` with a claude `gateway` backend and an `Env` map; a new `applyNamedProfile` step in `cmd/clown/main.go` authoritatively exports `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_API_KEY=""` for claude+gateway profiles. A `clown profile` subcommand (huh forms, atomic TOML writer) manages the user `profiles.toml`; the startup picker gains a `+ add profile…` item. Design doc: `docs/plans/2026-07-06-openrouter-profiles-design.md`.

**Tech Stack:** Go, BurntSushi/toml, charmbracelet huh + bubbletea (all already vendored), bats (zz-tests_bats), just.

**Rollback:** Purely additive — don't select the profile / delete the clownfile pin. Legacy config paths keep working via read-fallback.

**Design deviation (correct the doc in Task 2):** the design says the profile's `backend` feeds `flags.backend` / `newBackend()`. That is wrong: `parsedFlags.backend` is the **tent container backend** (podman|lima), a different axis from the profile registry's API backend (anthropic|gateway|local). `applyNamedProfile` must NOT touch `flags.backend`; the profile `Backend` only selects gateway-env behavior (claude) or is consumed inside `runOpencode`/`runCrush` (as today).

**Verification commands:** iterate with `go test ./internal/profile/... ./internal/clownfile/... ./cmd/clown/...` (vendored deps; works in the devshell). Cheap compile checks: `go build ./cmd/clown/...`. Do NOT run full `just` before merging — the spinclass pre-merge hook runs it.

---

### Task 1: `internal/profile` — Env field, claude+gateway validation, backend helpers

**Files:**
- Modify: `internal/profile/profile.go`
- Test: `internal/profile/profile_test.go`

**Step 1: Write failing tests** (append to `profile_test.go`; match its existing table style):

```go
func TestValidateClaudeGateway(t *testing.T) {
	ok := Profile{Name: "or", Provider: "claude", Backend: "gateway",
		URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}"}
	if err := Validate(ok); err != nil {
		t.Fatalf("claude+gateway with url+token should validate: %v", err)
	}
	for _, p := range []Profile{
		{Name: "no-url", Provider: "claude", Backend: "gateway", Token: "x"},
		{Name: "no-token", Provider: "claude", Backend: "gateway", URL: "x"},
	} {
		if err := Validate(p); err == nil {
			t.Errorf("profile %q should fail validation", p.Name)
		}
	}
}

func TestBackendsForProvider(t *testing.T) {
	got := Backends("claude")
	want := []string{"anthropic", "gateway", "local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Backends(claude) = %v, want %v", got, want)
	}
	if Backends("nope") != nil {
		t.Fatal("unknown provider should return nil")
	}
}

func TestEnvDecodes(t *testing.T) {
	var f struct {
		Profile []Profile `toml:"profile"`
	}
	src := "[[profile]]\nname = \"x\"\n[profile.env]\nANTHROPIC_DEFAULT_HAIKU_MODEL = \"m\"\n"
	if _, err := toml.Decode(src, &f); err != nil {
		t.Fatal(err)
	}
	if f.Profile[0].Env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "m" {
		t.Fatalf("env not decoded: %#v", f.Profile[0])
	}
}
```

**Step 2:** Run `go test ./internal/profile/` — expect FAIL (no Env field, no Backends, gateway invalid for claude).

**Step 3: Implement.** In `profile.go`:

- Add to `Profile`: `Env map[string]string \`toml:"env,omitempty"\`` and add `,omitempty` to the `URL` and `Token` tags (encode hygiene for Task 4's writer; decode is unaffected).
- `validCombos`: `"claude": {"anthropic": true, "gateway": true, "local": true}`.
- Change the gateway url/token requirement (currently gated on opencode/crush) to apply to **any** provider with `Backend == "gateway"`.
- Add (sorted, deterministic):

```go
// Backends returns the valid backend names for provider, sorted, or nil for
// an unknown provider. The single source of the provider/backend matrix for
// UI code (the profile TUI's select options).
func Backends(provider string) []string {
	m, ok := validCombos[provider]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(m))
	for b := range m {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Providers returns the known provider names, sorted.
func Providers() []string {
	out := make([]string, 0, len(validCombos))
	for p := range validCombos {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
```

**Step 4:** `go test ./internal/profile/` — PASS.

**Step 5:** Commit: `feat(profile): claude gateway backend, env map, matrix helpers`

---

### Task 2: `applyNamedProfile` in cmd/clown

**Files:**
- Modify: `cmd/clown/main.go` (new func near `applyClownfileProfile` ~line 465; call site after the picker block ~line 337)
- Modify: `docs/plans/2026-07-06-openrouter-profiles-design.md` (the backend deviation, header note above)
- Test: `cmd/clown/main_test.go`

**Step 1: Failing tests.** `applyNamedProfile` mutates process env; use `t.Setenv` throughout (it restores automatically, and also marks the test non-parallel):

```go
func TestApplyNamedProfileGatewayEnvAuthoritative(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "old")
	t.Setenv("ANTHROPIC_API_KEY", "real-key")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	flags := parsedFlags{provider: "claude"}
	p := profile.Profile{Name: "or", Provider: "claude", Backend: "gateway",
		URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}"}
	if err := applyNamedProfile(&flags, p); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ANTHROPIC_BASE_URL"); got != "https://openrouter.ai/api" {
		t.Errorf("base url = %q", got)
	}
	if got := os.Getenv("ANTHROPIC_AUTH_TOKEN"); got != "sk-or-test" {
		t.Errorf("auth token = %q", got)
	}
	v, set := os.LookupEnv("ANTHROPIC_API_KEY")
	if !set || v != "" {
		t.Errorf("ANTHROPIC_API_KEY must be present-but-empty, got set=%v v=%q", set, v)
	}
}

func TestApplyNamedProfileEmptyTokenRef(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	flags := parsedFlags{provider: "claude"}
	p := profile.Profile{Name: "or", Provider: "claude", Backend: "gateway",
		URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}"}
	err := applyNamedProfile(&flags, p)
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("want error naming the env var, got %v", err)
	}
}

func TestApplyNamedProfileModelInjection(t *testing.T) {
	flags := parsedFlags{provider: "claude", forwarded: []string{"-p", "hi"}}
	if err := applyNamedProfile(&flags, profile.Profile{Provider: "claude", Backend: "anthropic", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	if flags.forwarded[0] != "--model" || flags.forwarded[1] != "m1" {
		t.Fatalf("forwarded = %v", flags.forwarded)
	}
	// user-passed --model wins
	flags2 := parsedFlags{provider: "claude", forwarded: []string{"--model", "user"}}
	_ = applyNamedProfile(&flags2, profile.Profile{Provider: "claude", Backend: "anthropic", Model: "m1"})
	if len(flags2.forwarded) != 2 {
		t.Fatalf("must not double-inject: %v", flags2.forwarded)
	}
}

func TestApplyNamedProfileEnvMapOnlyIfUnset(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SUBAGENT_MODEL", "ambient")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "")
	flags := parsedFlags{provider: "claude"}
	p := profile.Profile{Provider: "claude", Backend: "anthropic",
		Env: map[string]string{"CLAUDE_CODE_SUBAGENT_MODEL": "p", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "p"}}
	if err := applyNamedProfile(&flags, p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CLAUDE_CODE_SUBAGENT_MODEL") != "ambient" {
		t.Error("ambient env must win over profile env map")
	}
	if os.Getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "p" {
		t.Error("unset env must be filled from profile env map")
	}
}
```

**Step 2:** `go test ./cmd/clown/ -run TestApplyNamedProfile` — FAIL (undefined).

**Step 3: Implement** in `main.go`:

```go
// applyNamedProfile applies a selected named profile (--profile, the clownfile
// pin, or the interactive picker) to the run. Unlike the clownfile [profile]
// defaults layer (applyClownfileProfile, only-if-unset), a named profile is an
// explicit user selection, so its gateway env is authoritative: picking
// claude-openrouter must mean OpenRouter even when the shell exports
// ANTHROPIC_* for something else. The generic Env map stays only-if-unset.
// Deliberately does not touch flags.backend — that is the tent container
// backend (podman|lima), a different axis from the profile's API backend.
func applyNamedProfile(flags *parsedFlags, p profile.Profile) error {
	if p.Model != "" && providerTakesModelFlag(flags.provider) && claudeFlagValue(flags.forwarded, "--model") == "" {
		flags.forwarded = append([]string{"--model", p.Model}, flags.forwarded...)
	}
	if p.Provider == "claude" && p.Backend == "gateway" {
		url := clownfile.ResolveEnv(p.URL)
		token := clownfile.ResolveEnv(p.Token)
		if url == "" {
			return fmt.Errorf("profile %q: url %q resolved empty", p.Name, p.URL)
		}
		if token == "" {
			return fmt.Errorf("profile %q: token %q resolved empty (is the referenced variable exported?)", p.Name, p.Token)
		}
		_ = os.Setenv("ANTHROPIC_BASE_URL", url)
		_ = os.Setenv("ANTHROPIC_AUTH_TOKEN", token)
		// Present-but-empty is the gateway contract: unset makes claude fall
		// back to Anthropic auth and conflict with the auth token.
		_ = os.Setenv("ANTHROPIC_API_KEY", "")
	}
	for k, v := range p.Env {
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, clownfile.ResolveEnv(v))
		}
	}
	return nil
}
```

Call site — immediately after the picker block (after `flags.profile = selectedProfile.Name`, before `resolveProvider`):

```go
	if selectedProfile != nil {
		if err := applyNamedProfile(&flags, *selectedProfile); err != nil {
			fmt.Fprintf(os.Stderr, "clown: %v\n", err)
			return 1
		}
	}
```

Also fix the design doc's Section-1 `backend → flags.backend` bullet per the header note.

**Step 4:** `go test ./cmd/clown/` — PASS. **Step 5:** Commit: `feat(clown): apply named profiles uniformly; claude gateway env`

---

### Task 3: builtin `claude-openrouter`? — NO (decision recorded)

No builtin profile is added (design: builtins stay secrets-free; the TUI template is the delivery vehicle). Nothing to do; this task exists so the executor doesn't "helpfully" add one.

---

### Task 4: config-dir migration (`userConfigPath`) + profiles/opencode/crush read-fallback

**Files:**
- Create: `cmd/clown/configpath.go`
- Modify: `cmd/clown/main.go` (`loadProfiles`), `cmd/clown/opencode.go` (`opencodeLocalConfigPath` users), `cmd/clown/crush.go` (same pattern)
- Test: `cmd/clown/configpath_test.go`

**Step 1: Failing tests.** Control `XDG_CONFIG_HOME` and `HOME` with `t.Setenv` pointing into `t.TempDir()`:

```go
func TestUserConfigPathCanonical(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("HOME", dir)
	canonical := filepath.Join(dir, "xdg", "clown", "profiles.toml")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, legacy, err := userConfigPath("profiles.toml")
	if err != nil || got != canonical || legacy {
		t.Fatalf("got %q legacy=%v err=%v, want canonical", got, legacy, err)
	}
}

func TestUserConfigPathLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	legacyPath := filepath.Join(dir, ".config", "juggler", "profiles.toml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, legacy, err := userConfigPath("profiles.toml")
	if err != nil || got != legacyPath || !legacy {
		t.Fatalf("got %q legacy=%v err=%v, want legacy fallback", got, legacy, err)
	}
}

func TestUserConfigPathNeitherExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	got, legacy, err := userConfigPath("profiles.toml")
	want := filepath.Join(dir, ".config", "clown", "profiles.toml")
	if err != nil || got != want || legacy {
		t.Fatalf("got %q legacy=%v err=%v, want canonical-nonexistent", got, legacy, err)
	}
}
```

**Step 2:** FAIL. **Step 3: Implement** `configpath.go`:

```go
// userConfigPath resolves the per-user config file `name` (e.g.
// "profiles.toml", "opencode.toml"). Canonical location is
// $XDG_CONFIG_HOME/clown/<name> (defaulting to ~/.config/clown/<name>); when
// the canonical file is absent but the legacy ~/.config/juggler/<name> exists,
// the legacy path is returned with legacy=true so the caller can warn once.
// Writers must always use userConfigWritePath. Legacy fallback removal
// criterion: one release with the warning, no reports (design doc, 2026-07-06).
func userConfigPath(name string) (path string, legacy bool, err error) {
	canonical, err := userConfigWritePath(name)
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(canonical); statErr == nil {
		return canonical, false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return canonical, false, nil
	}
	legacyPath := filepath.Join(home, ".config", "juggler", name)
	if _, statErr := os.Stat(legacyPath); statErr == nil {
		return legacyPath, true, nil
	}
	return canonical, false, nil
}

// userConfigWritePath is the canonical (always-write) location for name:
// $XDG_CONFIG_HOME/clown/<name>, ~/.config/clown/<name> when XDG is unset.
func userConfigWritePath(name string) (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "clown", name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "clown", name), nil
}

// warnLegacyConfig emits the one-line legacy-path warning.
func warnLegacyConfig(path string) {
	fmt.Fprintf(os.Stderr, "clown: reading legacy config %s; move it to ~/.config/clown/ (the TUI writes there)\n", path)
}
```

Rewire callers:
- `loadProfiles` (main.go:49-55): replace the hardcoded juggler path with `userConfigPath("profiles.toml")`; call `warnLegacyConfig` when `legacy` and the file loads.
- `opencodeLocalConfigPath` / crush's equivalent: read paths go through `userConfigPath`, the prompt-write paths through `userConfigWritePath`. Fix the stale comments (they already say `~/.config/clown/`).
- Note: `os.UserHomeDir` honors `$HOME` on linux, so `t.Setenv("HOME", …)` works.

**Step 4:** `go test ./cmd/clown/` — PASS (existing opencode/crush tests may reference the juggler path; update them to the canonical path). **Step 5:** Commit: `fix(clown): canonical ~/.config/clown config dir with legacy juggler fallback`

---

### Task 5: `internal/profile` writer — Save / Upsert / Remove

**Files:**
- Create: `internal/profile/store.go`
- Test: `internal/profile/store_test.go`

**Step 1: Failing tests:**

```go
func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "profiles.toml")
	in := []Profile{{Name: "or", Display: "Claude (OpenRouter)", Provider: "claude",
		Backend: "gateway", URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}"}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v != %#v", in, out)
	}
}

func TestUpsertAndRemove(t *testing.T) {
	base := []Profile{{Name: "a"}, {Name: "b"}}
	up := Upsert(base, Profile{Name: "b", Display: "B2"})
	if len(up) != 2 || up[1].Display != "B2" {
		t.Fatalf("upsert replace: %#v", up)
	}
	up = Upsert(up, Profile{Name: "c"})
	if len(up) != 3 {
		t.Fatalf("upsert append: %#v", up)
	}
	rm, found := Remove(up, "a")
	if !found || len(rm) != 2 {
		t.Fatalf("remove: %#v found=%v", rm, found)
	}
	if _, found := Remove(rm, "nope"); found {
		t.Fatal("remove of absent name must report false")
	}
}
```

**Step 2:** FAIL. **Step 3: Implement** `store.go` — encode `file{Profile: profiles}` with `toml.NewEncoder` into a buffer, `os.MkdirAll(dir, 0o700)`, write to `os.CreateTemp(dir, ".profiles-*")`, chmod 0600, `os.Rename` over path. Doc comment states the file is TUI-managed and comments are not preserved. `Upsert` replaces by Name or appends; `Remove` filters by Name returning `(result, found)`.

**Step 4:** PASS. **Step 5:** Commit: `feat(profile): atomic user-profiles writer (Save/Upsert/Remove)`

---

### Task 6: clownfile pin key + resolution order

**Files:**
- Modify: `internal/clownfile/clownfile.go` (Profile struct + `mergeInto`)
- Modify: `cmd/clown/main.go` (resolution order; delete the `buildcfg.DefaultProfile` defaulting in `parseFlags` ~line 1821)
- Test: `internal/clownfile/clownfile_test.go`, `cmd/clown/main_test.go`

**Step 1: Failing tests.** clownfile side (follow the existing test file's fixture style):

```go
// in clownfile_test.go: a deeper file's [profile].profile overrides a
// shallower one; absent leaves it "".
```

Write it with two temp-dir clownfiles (`[profile]\nprofile = "x"`) through `Discover`, asserting `cf.Profile.ProfileName`. cmd side — factor the ordering into a pure function and test it:

```go
func TestResolveProfileName(t *testing.T) {
	cases := []struct{ flag string; providerExplicit bool; pin, def, want string }{
		{"flag", false, "pin", "def", "flag"},
		{"", false, "pin", "def", "pin"},
		{"", false, "", "def", "def"},
		{"", true, "pin", "def", ""},   // explicit --provider suppresses defaulting
		{"flag", true, "pin", "def", "flag"}, // explicit --profile always wins
		{"", false, "", "", ""},
	}
	for _, c := range cases {
		got := resolveProfileName(c.flag, c.providerExplicit, c.pin, c.def)
		if got != c.want {
			t.Errorf("resolveProfileName(%q,%v,%q,%q) = %q, want %q",
				c.flag, c.providerExplicit, c.pin, c.def, got, c.want)
		}
	}
}
```

**Step 2:** FAIL. **Step 3: Implement.**

`clownfile.go` Profile struct:

```go
	// ProfileName pins a named registry profile: the run resolves it exactly
	// as if --profile <name> were passed — beneath an explicit --profile flag,
	// above buildcfg.DefaultProfile — and it suppresses the interactive picker.
	ProfileName string `toml:"profile"`
```

plus the replace-if-nonempty line in `mergeInto`. `main.go`:

```go
// resolveProfileName orders the named-profile sources: explicit --profile
// flag > clownfile [profile].profile pin > buildcfg.DefaultProfile. An
// explicit --provider suppresses profile *defaulting* (pin and build default)
// but never an explicit --profile.
func resolveProfileName(flagProfile string, providerExplicit bool, pin, buildDefault string) string {
	if flagProfile != "" {
		return flagProfile
	}
	if providerExplicit {
		return ""
	}
	if pin != "" {
		return pin
	}
	return buildDefault
}
```

In `runWithFlags`, right after `applyClownfileProfile(&flags, cf.Profile)`:

```go
	flags.profile = resolveProfileName(flags.profile, flags.providerExplicit, cf.Profile.ProfileName, buildcfg.DefaultProfile)
```

Delete the `parseFlags` block at ~1821 (`if p.profile == "" && !p.providerExplicit && buildcfg.DefaultProfile != ""`) and update any parser test that asserted it — the behavior moves to `runWithFlags` (`resume` subcommand path also gains it, which is a fix, not a regression). The pin suppresses the picker with no extra code: a resolved name makes `selectedProfile` non-nil before the picker gate. Unknown pin name hits the existing unknown-profile error listing available profiles.

**Step 4:** PASS all packages. **Step 5:** Commit: `feat(clownfile): [profile].profile pin selects a named registry profile`

---

### Task 7: `clown profile` subcommand — list/remove + plumbing

**Files:**
- Create: `cmd/clown/profilecmd.go`
- Modify: `cmd/clown/main.go` (subcommand switch ~line 199: `case "profile": return runProfileCmd(rawArgs[1:])`; `printHelp` gains the subcommand + updated `--profile` text)
- Test: `cmd/clown/profilecmd_test.go`

**Step 1: Failing tests.** Refactor target: `loadProfiles` currently returns only the merged set; `list` needs source labels. Add `loadProfileSets() (builtin, user []profile.Profile, userPath string, err error)` (extracting the existing body; `loadProfiles` becomes a thin `profile.Merge` wrapper — keep its signature so existing callers/tests stand). Test the formatter, not the I/O:

```go
func TestFormatProfileList(t *testing.T) {
	builtin := []profile.Profile{{Name: "claude-anthropic", Display: "Claude (Anthropic)", Provider: "claude", Backend: "anthropic"}}
	user := []profile.Profile{
		{Name: "claude-anthropic", Display: "Mine", Provider: "claude", Backend: "anthropic"}, // override
		{Name: "claude-openrouter", Display: "Claude (OpenRouter)", Provider: "claude", Backend: "gateway"},
	}
	out := formatProfileList(builtin, user)
	for _, want := range []string{"user override", "claude-openrouter", "user", "claude / gateway"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
```

**Step 2:** FAIL. **Step 3: Implement** `profilecmd.go`:

```go
// runProfileCmd dispatches `clown profile <list|add|edit|remove>` — the
// management surface for the named-profile registry (user profiles.toml).
func runProfileCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: clown profile <list|add|edit <name>|remove <name>>")
		return 2
	}
	switch args[0] {
	case "list":
		return runProfileList()
	case "add":
		return runProfileAdd()
	case "edit":
		if len(args) < 2 { /* usage, return 2 */ }
		return runProfileEdit(args[1])
	case "remove":
		if len(args) < 2 { /* usage, return 2 */ }
		return runProfileRemove(args[1])
	default:
		/* usage, return 2 */
	}
}
```

`formatProfileList` builds `name / display / provider-backend / source` rows (source: `builtin`, `user`, `user override` when a user name shadows a builtin) using `text/tabwriter`. `runProfileList` = `loadProfileSets` + print. `runProfileRemove`: TTY-gate (reuse the `pluginhost.IsInteractive()` precedent from opencode.go), `huh.NewConfirm` naming the file, then `profile.Remove` + `profile.Save` to `userConfigWritePath("profiles.toml")`; removing a name that only exists as builtin errors ("builtin profiles cannot be removed; overrides can"). For this task, `runProfileAdd`/`runProfileEdit` are stubs returning "not implemented" (Task 8 fills them) — keeps the commit compiling and bats-testable.

**Step 4:** PASS + `go build ./cmd/clown/`. **Step 5:** Commit: `feat(clown): profile subcommand (list/remove); loadProfileSets split`

---

### Task 8: add/edit huh form + templates

**Files:**
- Create: `cmd/clown/profileform.go`
- Modify: `cmd/clown/profilecmd.go` (fill add/edit stubs)
- Test: `cmd/clown/profileform_test.go`

**Step 1: Failing tests** — everything except the interactive `form.Run()` is pure and tested:

```go
func TestProfileTemplates(t *testing.T) {
	or := templateByName(t, "openrouter")
	if or.URL != "https://openrouter.ai/api" {
		t.Errorf("openrouter url = %q (must be /api, NOT /api/v1)", or.URL)
	}
	if or.Token != "${OPENROUTER_API_KEY}" || or.Model != "" || or.Backend != "gateway" {
		t.Errorf("openrouter template: %#v", or)
	}
}

func TestValidateProfileNameField(t *testing.T) {
	existing := map[string]bool{"taken": true}
	v := validateProfileName(existing, "")
	if v("taken") == nil {
		t.Error("duplicate must be rejected on add")
	}
	if v("") == nil || v("has space") == nil {
		t.Error("empty / spaced names must be rejected")
	}
	if v("claude-openrouter2") != nil {
		t.Error("plain kebab name must pass")
	}
	// edit mode: own original name allowed
	if validateProfileName(existing, "taken")("taken") != nil {
		t.Error("edit may keep its own name")
	}
}

func TestFormValuesToProfile(t *testing.T) {
	v := profileFormValues{Name: " or ", Display: "X", Provider: "claude",
		Backend: "gateway", URL: " https://openrouter.ai/api ", Token: "${OPENROUTER_API_KEY}"}
	p := v.toProfile()
	if p.Name != "or" || p.URL != "https://openrouter.ai/api" {
		t.Errorf("fields must be trimmed: %#v", p)
	}
	if err := profile.Validate(p); err != nil {
		t.Fatal(err)
	}
}
```

**Step 2:** FAIL. **Step 3: Implement** `profileform.go`:

- `profileFormValues` struct (Name/Display/Provider/Backend/Model/URL/Token strings, Confirm bool) with `toProfile()` (TrimSpace all fields).
- Template table:

```go
type profileTemplate struct {
	key  string // select value
	p    profile.Profile
	note string // shown post-save
}

var profileTemplates = []profileTemplate{
	{key: "openrouter", p: profile.Profile{Name: "claude-openrouter",
		Display: "Claude (OpenRouter)", Provider: "claude", Backend: "gateway",
		URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}"},
		// Model deliberately empty: claude's own defaults flow through and
		// OpenRouter's Anthropic Skin maps them (tuning lever, design doc).
		note: "If claude was previously logged in with an Anthropic account, run /logout once inside claude (cached-login conflict)."},
	{key: "gateway", p: profile.Profile{Provider: "claude", Backend: "gateway"}},
	{key: "anthropic", p: profile.Profile{Provider: "claude", Backend: "anthropic"}},
	{key: "local", p: profile.Profile{Provider: "claude", Backend: "local"}},
}
```

- `validateProfileName(existing map[string]bool, editOriginal string) func(string) error` — nonempty, `^[a-z0-9][a-z0-9._-]*$`, unique unless == editOriginal. (Duplicating a **builtin** name is allowed — that's the override mechanic — so `existing` is user names only.)
- `buildProfileForm(v *profileFormValues, existingUserNames map[string]bool, editOriginal string) *huh.Form` — one `huh.NewGroup` mirroring `promptOpencodeLocalConfig` (main.go precedent): name/display inputs, provider `huh.NewSelect` over `profile.Providers()`, backend select with `OptionsFunc` re-reading `profile.Backends(v.Provider)` (dynamic on provider change — see vendor/…/huh README "Dynamic" section), model input, url + token inputs (token `EchoMode(huh.EchoModePassword)`, description "literal or ${VAR} reference"), final `huh.NewConfirm` titled with the destination path. URL/token validators require non-blank only when `v.Backend == "gateway"`.
- `runProfileAdd`: TTY gate → template `huh.NewSelect` (skip when only "custom" makes sense? no — always show) → seed values from template → `buildProfileForm(...).Run()` → on confirm `profile.Upsert` + `profile.Save(userConfigWritePath(...))` → print saved path + template note. Abort/cancel: non-zero exit, nothing written (match `promptOpencodeLocalConfig`'s contract).
- `runProfileEdit(name)`: `loadProfileSets`, find in merged (error listing names when absent), seed values, `editOriginal=name` when it's a user entry (builtin → treated as add-of-override: existing check skipped for that name), save.

**Step 4:** PASS. **Step 5:** Commit: `feat(clown): profile add/edit huh forms with openrouter template`

---

### Task 9: picker `+ add profile…` hook

**Files:**
- Modify: `cmd/clown/main.go` (`pickProfile`, `pickerModel`, the picker call site ~line 321)
- Test: `cmd/clown/main_test.go`

**Step 1: Failing test** for the seam (keep bubbletea out of it):

```go
func TestPickerItemsIncludeAddSentinel(t *testing.T) {
	items := pickerItems([]profile.Profile{{Name: "a", Display: "A"}})
	last := items[len(items)-1].(profileItem)
	if !last.add || last.Title() != "+ add profile…" {
		t.Fatalf("last item must be the add sentinel: %#v", last)
	}
}
```

**Step 2:** FAIL. **Step 3: Implement:** add `add bool` to `profileItem` (Title/Description/FilterValue branch on it); extract `pickerItems(profiles)` (used by `pickProfile`); `pickerModel` gains `addRequested bool` set when enter lands on the sentinel; `pickProfile` returns `(*profile.Profile, bool /*add*/, error)`. Call site:

```go
		chosen, addRequested, err := pickProfile(profiles)
		// ...err/quit handling as today...
		if addRequested {
			created, err := profileAddInteractive() // Task 8's core, returning the saved profile
			if err != nil { /* stderr, return 1 */ }
			chosen = created
		}
		selectedProfile = chosen
```

The clown#160 comment already explains why `flags.profile` is recorded — the new profile flows through the same lines.

**Step 4:** PASS. **Step 5:** Commit: `feat(clown): picker gains + add profile… hook`

---

### Task 10: bats coverage + smoke recipe

**Files:**
- Create: `zz-tests_bats/profile.bats` (follow the harness conventions in the existing `zz-tests_bats/*.bats`; see @eng:wiring-bats-tests before writing)
- Modify: `justfile` (smoke recipe next to `smoke-clown-against-tailnet`)

**Step 1:** Read one existing bats file for the build/binary-resolution pattern, then write three tests (adapt setup to match):

```bash
@test "profile list shows builtin profiles" {
  run "$CLOWN" profile list
  [ "$status" -eq 0 ]
  [[ "$output" == *"claude-anthropic"* ]]
  [[ "$output" == *"builtin"* ]]
}

@test "profile edit of unknown name fails listing available" {
  run "$CLOWN" profile edit no-such-profile
  [ "$status" -ne 0 ]
  [[ "$output" == *"no-such-profile"* ]]
}

@test "profile add refuses without a TTY" {
  run "$CLOWN" profile add </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"TTY"* || "$output" == *"interactive"* ]]
}
```

Point `HOME`/`XDG_CONFIG_HOME` at a bats temp dir so user profiles never leak in. If the repo's bats lane needs a new file registered (tags/lanes), follow @eng:wiring-bats-tests.

**Step 2:** Run the repo's bats lane the paved-path way (check `just --list` for the bats recipe) — new file passes.

**Step 3:** justfile recipe, modeled on `smoke-clown-against-tailnet`'s header style:

```just
# Launch claude-code against openrouter.ai via the claude+gateway profile
# (design: docs/plans/2026-07-06-openrouter-profiles-design.md). Requires
# OPENROUTER_API_KEY exported and a claude-openrouter profile
# (`clown profile add`, openrouter template). Diagnostic paved path.
[group('smoke')]
smoke-clown-against-openrouter *args="--version":
    #!/usr/bin/env bash
    set -euo pipefail
    [[ -n "${OPENROUTER_API_KEY:-}" ]] || { echo "OPENROUTER_API_KEY is not exported" >&2; exit 1; }
    nix build .#dev
    exec ./result/bin/clown --profile claude-openrouter -- {{args}}
```

**Step 4:** `just --list` shows the recipe; dry-run it (`just --dry-run smoke-clown-against-openrouter`). **Step 5:** Commit: `test(clown): profile bats lane + openrouter smoke recipe`

---

### Task 11: docs

**Files:**
- Modify: `AGENTS.md` (the "Profile system (planned)" paragraph is stale — profiles are implemented; describe claude+gateway, the pin key, `clown profile`, and the canonical `~/.config/clown/` paths with legacy fallback)
- Modify: the `clownfile(5)` man page source (locate with `rg -l 'clownfile' --glob '*.scd'` or wherever the man sources live; follow eng-manpages(7) via the man MCP tools) — document `[profile].profile`
- Modify: `printHelp` in `cmd/clown/main.go` if not already done in Task 7

**Steps:** update, `go build ./cmd/clown/...`, commit: `docs: profile registry, openrouter gateway, clownfile pin`.

Run @eng:doc-drift against the full diff before merge (pre-merge attestation requires it anyway).

---

## Execution notes

- Fresh subagent per task (eng:subagent-driven-development), code review between tasks.
- Tasks 1→2 and 4→5→6→7→8→9 are ordered by dependency; Task 10/11 last.
- Do not run full `just` at the end — `merge-this-session`'s pre-merge hook is the CI lane.
- New files must be `git add`ed before any `nix build` (dirty-tree builds only see tracked files).
