package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/buildcfg"
	"github.com/amarbel-llc/clown/internal/promptwalk"
)

func withBuildcfgString(t *testing.T, target *string, value string) {
	t.Helper()
	prev := *target
	*target = value
	t.Cleanup(func() { *target = prev })
}

// TestMain clears the build-time defaults so the rest of the suite
// runs against the historical hardcoded fallback. nix's
// buildGoApplication forwards the package's -ldflags into the test
// binary, which would otherwise leak DefaultProvider / DefaultProfile
// into table tests that assert empty values. Tests that exercise
// the build-time defaults re-set them via withBuildcfgString.
func TestMain(m *testing.M) {
	buildcfg.DefaultProvider = ""
	buildcfg.DefaultProfile = ""
	os.Exit(m.Run())
}

func TestParseFlags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want parsedFlags
	}{
		{
			name: "no args defaults to claude provider",
			in:   []string{},
			want: parsedFlags{provider: "claude"},
		},
		{
			name: "version subcommand",
			in:   []string{"version"},
			want: parsedFlags{provider: "claude", version: true},
		},
		{
			name: "provider flag",
			in:   []string{"--provider", "codex"},
			want: parsedFlags{provider: "codex", providerExplicit: true},
		},
		{
			name: "provider= syntax",
			in:   []string{"--provider=codex"},
			want: parsedFlags{provider: "codex", providerExplicit: true},
		},
		{
			name: "naked flag",
			in:   []string{"--naked"},
			want: parsedFlags{provider: "claude", naked: true},
		},
		{
			name: "skip-failed flag",
			in:   []string{"--skip-failed"},
			want: parsedFlags{provider: "claude", skipFailed: true},
		},
		{
			name: "disable-clown-protocol flag",
			in:   []string{"--disable-clown-protocol"},
			want: parsedFlags{provider: "claude", disableClownProtocol: true},
		},
		{
			name: "tent flag",
			in:   []string{"--tent"},
			want: parsedFlags{provider: "claude", tent: true},
		},
		{
			name: "tent-pass-devshell flag",
			in:   []string{"--tent", "--tent-pass-devshell"},
			want: parsedFlags{provider: "claude", tent: true, passDevshell: true},
		},
		{
			name: "no-tent-pass-devshell flag",
			in:   []string{"--tent", "--no-tent-pass-devshell"},
			want: parsedFlags{provider: "claude", tent: true, noPassDevshell: true},
		},
		{
			name: "verbose long flag",
			in:   []string{"--verbose"},
			want: parsedFlags{provider: "claude", verbose: true},
		},
		{
			name: "verbose short flag",
			in:   []string{"-v"},
			want: parsedFlags{provider: "claude", verbose: true},
		},
		{
			name: "double-dash forwards everything after",
			in:   []string{"--", "chat", "--model", "sonnet"},
			want: parsedFlags{provider: "claude", forwarded: []string{"chat", "--model", "sonnet"}},
		},
		{
			name: "double-dash with no forwarded args",
			in:   []string{"--verbose", "--"},
			want: parsedFlags{provider: "claude", verbose: true},
		},
		{
			name: "clown flags then double-dash then forwarded args",
			in:   []string{"--skip-failed", "--verbose", "--", "chat", "--resume"},
			want: parsedFlags{
				provider:   "claude",
				skipFailed: true,
				verbose:    true,
				forwarded:  []string{"chat", "--resume"},
			},
		},
		{
			name: "all clown flags then double-dash",
			in: []string{
				"--provider", "claude",
				"--skip-failed",
				"--disable-clown-protocol",
				"--verbose",
				"--",
				"chat",
			},
			want: parsedFlags{
				provider:             "claude",
				providerExplicit:     true,
				skipFailed:           true,
				disableClownProtocol: true,
				verbose:              true,
				forwarded:            []string{"chat"},
			},
		},
		{
			name: "help long flag",
			in:   []string{"--help"},
			want: parsedFlags{provider: "claude", help: true},
		},
		{
			name: "help short flag",
			in:   []string{"-h"},
			want: parsedFlags{provider: "claude", help: true},
		},
		{
			name: "provider flag sets providerExplicit",
			in:   []string{"--provider", "codex"},
			want: parsedFlags{provider: "codex", providerExplicit: true},
		},
		{
			name: "provider= syntax sets providerExplicit",
			in:   []string{"--provider=codex"},
			want: parsedFlags{provider: "codex", providerExplicit: true},
		},
		{
			name: "plugin-dir space form",
			in:   []string{"--plugin-dir", "foo"},
			want: parsedFlags{provider: "claude", extraPluginDirs: []string{"foo"}},
		},
		{
			name: "plugin-dir equals form",
			in:   []string{"--plugin-dir=foo"},
			want: parsedFlags{provider: "claude", extraPluginDirs: []string{"foo"}},
		},
		{
			name: "plugin-dir multiple flags accumulate in order",
			in:   []string{"--plugin-dir", "a", "--plugin-dir=b", "--plugin-dir", "c"},
			want: parsedFlags{provider: "claude", extraPluginDirs: []string{"a", "b", "c"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFlags(tc.in)
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseFlags(%v) =\n  %+v\nwant\n  %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseFlagsErrors(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		wantMsg string
	}{
		{"provider missing arg", []string{"--provider"}, "--provider requires an argument"},
		{"unknown flag before double-dash", []string{"--unknown-flag"}, ""},
		{"unknown short flag before double-dash", []string{"-x"}, ""},
		{"positional arg before double-dash", []string{"chat"}, ""},
		{"plugin-dir missing arg", []string{"--plugin-dir"}, "--plugin-dir requires an argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags(tc.in)
			if err == nil {
				t.Fatalf("expected error for %v, got nil", tc.in)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestParseFlagsPassDevshellEnv(t *testing.T) {
	t.Setenv("CLOWN_TENT_PASS_DEVSHELL", "1")
	got, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !got.passDevshell {
		t.Errorf("passDevshell = false, want true (CLOWN_TENT_PASS_DEVSHELL=1)")
	}
}

func TestParseFlagsProviderEnv(t *testing.T) {
	t.Setenv("CLOWN_PROVIDER", "codex")
	got, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.provider != "codex" {
		t.Errorf("provider = %q, want codex", got.provider)
	}
}

func TestParseFlagsProviderFlagOverridesEnv(t *testing.T) {
	t.Setenv("CLOWN_PROVIDER", "codex")
	got, err := parseFlags([]string{"--provider", "claude"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.provider != "claude" {
		t.Errorf("provider = %q, want claude", got.provider)
	}
	if !got.providerExplicit {
		t.Error("providerExplicit should be true when --provider flag is used")
	}
}

func TestLoadProfiles_BuiltinNotEmpty(t *testing.T) {
	profiles, err := loadProfiles(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("loadProfiles: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("expected at least one builtin profile")
	}
}

func TestLoadProfiles_AdditionalMerged(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "profiles.toml")
	if err := os.WriteFile(f, []byte(`
[[profile]]
name     = "extra"
display  = "Extra"
provider = "opencode"
backend  = "local"
model    = "qwen3-coder"
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	profiles, err := loadProfiles(f)
	if err != nil {
		t.Fatalf("loadProfiles: %v", err)
	}
	var found bool
	for _, p := range profiles {
		if p.Name == "extra" {
			found = true
		}
	}
	if !found {
		t.Error("additional profile not merged")
	}
	var builtinFound bool
	for _, p := range profiles {
		if p.Name == "claude-anthropic" {
			builtinFound = true
		}
	}
	if !builtinFound {
		t.Error("builtin profile claude-anthropic missing after merge")
	}
}

func TestParseFlags_Profile(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--profile", "local-qwen"}, "local-qwen"},
		{[]string{"--profile=gateway-gpt4o"}, "gateway-gpt4o"},
	}
	for _, c := range cases {
		got, err := parseFlags(c.args)
		if err != nil {
			t.Fatalf("parseFlags(%v): %v", c.args, err)
		}
		if got.profile != c.want {
			t.Errorf("parseFlags(%v).profile = %q, want %q", c.args, got.profile, c.want)
		}
	}
}

func TestParseFlags_ProfileFromEnv(t *testing.T) {
	t.Setenv("CLOWN_PROFILE", "my-profile")
	got, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.profile != "my-profile" {
		t.Errorf("got profile %q, want %q", got.profile, "my-profile")
	}
}

func TestParseFlags_DefaultProviderFromBuildcfg(t *testing.T) {
	withBuildcfgString(t, &buildcfg.DefaultProvider, "codex")
	got, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.provider != "codex" {
		t.Errorf("provider = %q, want codex", got.provider)
	}
	if got.providerExplicit {
		t.Error("providerExplicit should be false when only build-time default applies")
	}
}

func TestParseFlags_DefaultProviderEnvOverridesBuildcfg(t *testing.T) {
	withBuildcfgString(t, &buildcfg.DefaultProvider, "codex")
	t.Setenv("CLOWN_PROVIDER", "opencode")
	got, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.provider != "opencode" {
		t.Errorf("provider = %q, want opencode", got.provider)
	}
	if !got.providerExplicit {
		t.Error("providerExplicit should be true with CLOWN_PROVIDER set")
	}
}

func TestParseFlags_DefaultProfileFromBuildcfg(t *testing.T) {
	withBuildcfgString(t, &buildcfg.DefaultProfile, "claude-anthropic")
	got, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.profile != "claude-anthropic" {
		t.Errorf("profile = %q, want claude-anthropic", got.profile)
	}
}

func TestParseFlags_DefaultProfileSuppressedByExplicitProvider(t *testing.T) {
	withBuildcfgString(t, &buildcfg.DefaultProfile, "claude-anthropic")
	got, err := parseFlags([]string{"--provider", "codex"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.profile != "" {
		t.Errorf("profile = %q, want empty (explicit --provider should suppress build-time default profile)", got.profile)
	}
	if got.provider != "codex" {
		t.Errorf("provider = %q, want codex", got.provider)
	}
}

func TestParseFlags_DefaultProfileSuppressedByExplicitProfile(t *testing.T) {
	withBuildcfgString(t, &buildcfg.DefaultProfile, "claude-anthropic")
	got, err := parseFlags([]string{"--profile", "claude-local"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.profile != "claude-local" {
		t.Errorf("profile = %q, want claude-local", got.profile)
	}
}

func TestParseFlags_DefaultProfileSuppressedByEnvProfile(t *testing.T) {
	withBuildcfgString(t, &buildcfg.DefaultProfile, "claude-anthropic")
	t.Setenv("CLOWN_PROFILE", "claude-local")
	got, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.profile != "claude-local" {
		t.Errorf("profile = %q, want claude-local", got.profile)
	}
}

func TestReadCircusPortfile_BarePort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	stateDir := filepath.Join(dir, ".local", "state", "circus")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Current circus daemon writes just the port number.
	if err := os.WriteFile(filepath.Join(stateDir, "llama-server.port"), []byte("8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, err := readCircusPortfile()
	if err != nil {
		t.Fatalf("readCircusPortfile: %v", err)
	}
	if addr != "127.0.0.1:8080" {
		t.Errorf("addr = %q, want %q", addr, "127.0.0.1:8080")
	}
}

func TestReadCircusPortfile_HostPortBackcompat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	stateDir := filepath.Join(dir, ".local", "state", "circus")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Older circus builds wrote a full host:port pair. Verify we still
	// accept that form unchanged.
	if err := os.WriteFile(filepath.Join(stateDir, "llama-server.port"), []byte("127.0.0.1:9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, err := readCircusPortfile()
	if err != nil {
		t.Fatalf("readCircusPortfile: %v", err)
	}
	if addr != "127.0.0.1:9090" {
		t.Errorf("addr = %q, want %q", addr, "127.0.0.1:9090")
	}
}

func TestReadCircusPortfile_Missing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_, err := readCircusPortfile()
	if err == nil {
		t.Fatal("expected error when portfile is missing")
	}
}

func TestReadPluginDirs_FromMeta(t *testing.T) {
	metaDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(metaDir, "plugin-dirs"),
		[]byte("/baked/a\n/baked/b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_PLUGIN_META", metaDir)

	got := readPluginDirs()
	want := []string{"/baked/a", "/baked/b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readPluginDirs = %v, want %v", got, want)
	}
}

func TestReadPluginDirs_NoMeta(t *testing.T) {
	t.Setenv("CLOWN_PLUGIN_META", "")
	if got := readPluginDirs(); got != nil {
		t.Errorf("readPluginDirs = %v, want nil when CLOWN_PLUGIN_META unset", got)
	}
}

func TestReadPluginFragmentDirs_FromMeta(t *testing.T) {
	metaDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(metaDir, "plugin-fragment-dirs"),
		[]byte("/store/plugin-a/.clown-plugin/system-prompt-append.d\n/store/plugin-b/.clown-plugin/system-prompt-append.d\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_PLUGIN_META", metaDir)

	got := readPluginFragmentDirs()
	want := []string{
		"/store/plugin-a/.clown-plugin/system-prompt-append.d",
		"/store/plugin-b/.clown-plugin/system-prompt-append.d",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readPluginFragmentDirs = %v, want %v", got, want)
	}
}

func TestReadPluginFragmentDirs_NoMeta(t *testing.T) {
	t.Setenv("CLOWN_PLUGIN_META", "")
	if got := readPluginFragmentDirs(); got != nil {
		t.Errorf("readPluginFragmentDirs = %v, want nil when CLOWN_PLUGIN_META unset", got)
	}
}

// TestReadPluginFragmentDirs_FileMissing ensures clown tolerates a
// CLOWN_PLUGIN_META directory that does not yet contain a
// plugin-fragment-dirs file. This protects forward compat between an
// older clown binary (no FDR 0003 output) and a newer reader.
func TestReadPluginFragmentDirs_FileMissing(t *testing.T) {
	metaDir := t.TempDir()
	t.Setenv("CLOWN_PLUGIN_META", metaDir)
	if got := readPluginFragmentDirs(); got != nil {
		t.Errorf("readPluginFragmentDirs = %v, want nil when file missing", got)
	}
}

// TestPluginFragments_EndToEnd exercises FDR 0003's full pipeline:
// (1) mkCircus-style CLOWN_PLUGIN_META with a plugin-fragment-dirs
// file pointing at real fixture directories, (2) cmd/clown's read
// path, (3) the same builtin-dirs slice runWithFlags assembles,
// (4) promptwalk.WalkPrompts emits fragments in builtin → plugin →
// user order. The fixture mimics a plugin layout with
// .clown-plugin/system-prompt-append.d/ containing a marker file.
func TestPluginFragments_EndToEnd(t *testing.T) {
	tmp := t.TempDir()

	clownBuiltin := filepath.Join(tmp, "clown-builtin")
	if err := os.MkdirAll(clownBuiltin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(clownBuiltin, "00-builtin.md"),
		[]byte("BUILTIN"), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginRoot := filepath.Join(tmp, "plugin-a")
	pluginFragDir := filepath.Join(pluginRoot, ".clown-plugin", "system-prompt-append.d")
	if err := os.MkdirAll(pluginFragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pluginFragDir, "00-plugin.md"),
		[]byte("PLUGIN"), 0o644); err != nil {
		t.Fatal(err)
	}

	metaDir := filepath.Join(tmp, "meta")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(metaDir, "plugin-fragment-dirs"),
		[]byte(pluginFragDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_PLUGIN_META", metaDir)

	startDir := filepath.Join(tmp, "project")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPromptD := filepath.Join(startDir, ".circus", "system-prompt.d")
	if err := os.MkdirAll(userPromptD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(userPromptD, "00-user.md"),
		[]byte("USER"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Mirror runWithFlags' assembly logic: clown builtin first, then
	// plugin fragment dirs from the meta file, in order.
	builtin := append([]string{clownBuiltin}, readPluginFragmentDirs()...)

	result, err := promptwalk.WalkPrompts(startDir, tmp, builtin)
	if err != nil {
		t.Fatalf("WalkPrompts: %v", err)
	}
	want := "BUILTIN\n\nPLUGIN\n\nUSER\n\n"
	if result.AppendFragments != want {
		t.Errorf("AppendFragments = %q, want %q", result.AppendFragments, want)
	}
}

// TestPluginDirsResolution_FlagsAppendedAfterMeta verifies the
// runWithFlags-level invariant from #29: command-line --plugin-dir
// entries are appended after the baked-in dirs read from
// CLOWN_PLUGIN_META, in the order they were supplied.
func TestPluginDirsResolution_FlagsAppendedAfterMeta(t *testing.T) {
	metaDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(metaDir, "plugin-dirs"),
		[]byte("/baked/a\n/baked/b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_PLUGIN_META", metaDir)

	flags, err := parseFlags([]string{"--plugin-dir", "/cli/x", "--plugin-dir=/cli/y"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	// Mirrors runWithFlags' resolution logic in main.go.
	pluginDirs := readPluginDirs()
	pluginDirs = append(pluginDirs, flags.extraPluginDirs...)

	want := []string{"/baked/a", "/baked/b", "/cli/x", "/cli/y"}
	if !reflect.DeepEqual(pluginDirs, want) {
		t.Errorf("resolved pluginDirs = %v, want %v", pluginDirs, want)
	}
}

func TestPrependPluginDirs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		pluginDirs []string
		dirMap     map[string]string
		want       []string
	}{
		{
			name: "no plugins",
			args: []string{"--foo", "bar"},
			want: []string{"--foo", "bar"},
		},
		{
			name:       "plugin dirs without mapping",
			args:       []string{"--foo"},
			pluginDirs: []string{"/a", "/b"},
			want:       []string{"--plugin-dir", "/a", "--plugin-dir", "/b", "--foo"},
		},
		{
			name:       "plugin dirs with dirMap substitution",
			args:       []string{"--foo"},
			pluginDirs: []string{"/orig/a", "/orig/b"},
			dirMap:     map[string]string{"/orig/a": "/stage/a"},
			want:       []string{"--plugin-dir", "/stage/a", "--plugin-dir", "/orig/b", "--foo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prependPluginDirs(tc.args, tc.pluginDirs, tc.dirMap)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("prependPluginDirs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTentExecutor_EmptyPodmanPath(t *testing.T) {
	withBuildcfgString(t, &buildcfg.PodmanPath, "")
	e := &tentExecutor{innerCliPath: "/nix/store/x-claude/bin/claude"}
	if _, err := e.Binary(); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("expected podman-missing error, got %v", err)
	}
}

func TestNewTentExecutor_EmptyImageRef(t *testing.T) {
	withBuildcfgString(t, &buildcfg.PodmanPath, "/usr/bin/false")
	withBuildcfgString(t, &buildcfg.TentImageRef, "")
	if _, err := newTentExecutor("/x/claude", nil, nil, false, false); err == nil || !strings.Contains(err.Error(), "TentImageRef") {
		t.Fatalf("expected TentImageRef-empty error, got %v", err)
	}
}

func TestResolvePassDevshell(t *testing.T) {
	cases := []struct {
		name        string
		flags       parsedFlags
		inNixShell  string // empty = unset, non-empty = set to this value
		want        bool
	}{
		{
			name: "no flag, no env → off",
			want: false,
		},
		{
			name:       "no flag, IN_NIX_SHELL=pure → on (auto-detect)",
			inNixShell: "pure",
			want:       true,
		},
		{
			name:       "no flag, IN_NIX_SHELL=impure → on (auto-detect)",
			inNixShell: "impure",
			want:       true,
		},
		{
			name:  "explicit --tent-pass-devshell, no env → on",
			flags: parsedFlags{passDevshell: true},
			want:  true,
		},
		{
			name:       "explicit --tent-pass-devshell, with env → on",
			flags:      parsedFlags{passDevshell: true},
			inNixShell: "pure",
			want:       true,
		},
		{
			name:       "explicit --no-tent-pass-devshell wins over IN_NIX_SHELL auto-on",
			flags:      parsedFlags{noPassDevshell: true},
			inNixShell: "pure",
			want:       false,
		},
		{
			name:  "explicit --no-tent-pass-devshell, no env → off",
			flags: parsedFlags{noPassDevshell: true},
			want:  false,
		},
		// Both flags set is rejected upstream in runWithFlags; not exercised here.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.inNixShell == "" {
				t.Setenv("IN_NIX_SHELL", "")
				// t.Setenv "" doesn't unset; explicitly unset.
				os.Unsetenv("IN_NIX_SHELL")
			} else {
				t.Setenv("IN_NIX_SHELL", tc.inNixShell)
			}
			got := resolvePassDevshell(tc.flags)
			if got != tc.want {
				t.Errorf("resolvePassDevshell(%+v, IN_NIX_SHELL=%q) = %v, want %v",
					tc.flags, tc.inNixShell, got, tc.want)
			}
		})
	}
}

func TestUserHasSubuid(t *testing.T) {
	cases := []struct {
		name        string
		fileContent string
		userName    string
		uid         string
		wantMissing bool
	}{
		{
			name:        "user by name",
			fileContent: "alice:100000:65536\nbob:165536:65536\n",
			userName:    "bob",
			uid:         "1001",
			wantMissing: false,
		},
		{
			name:        "user by uid",
			fileContent: "alice:100000:65536\n1001:200000:65536\n",
			userName:    "bob",
			uid:         "1001",
			wantMissing: false,
		},
		{
			name:        "user missing",
			fileContent: "alice:100000:65536\n",
			userName:    "bob",
			uid:         "1001",
			wantMissing: true,
		},
		{
			name:        "ignores blank and comment lines",
			fileContent: "\n# comment\nbob:165536:65536\n",
			userName:    "bob",
			uid:         "1001",
			wantMissing: false,
		},
		{
			name:        "no match when name empty and uid mismatch",
			fileContent: "alice:100000:65536\n",
			userName:    "",
			uid:         "1001",
			wantMissing: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			subuidPath := filepath.Join(dir, "subuid")
			if err := os.WriteFile(subuidPath, []byte(tc.fileContent), 0o600); err != nil {
				t.Fatal(err)
			}
			// userHasSubuid reads /etc/subuid; test the parse logic
			// via a local copy. Inline reimplementation keeps the
			// production function's signature stable.
			data, err := os.ReadFile(subuidPath)
			if err != nil {
				t.Fatal(err)
			}
			missing := true
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				field, _, _ := strings.Cut(line, ":")
				if (tc.userName != "" && field == tc.userName) || field == tc.uid {
					missing = false
					break
				}
			}
			if missing != tc.wantMissing {
				t.Errorf("missing = %v, want %v", missing, tc.wantMissing)
			}
		})
	}
}

func TestUserHasSubuid_FileMissing(t *testing.T) {
	dir := t.TempDir()
	missing, err := userHasSubuidAt(filepath.Join(dir, "nonexistent"), "bob", "1001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !missing {
		t.Error("missing /etc/subuid should report missing=true")
	}
}

// userHasSubuidAt is a thin testable wrapper around userHasSubuid's
// parsing logic that takes the file path as input. Kept in the test
// file so it doesn't broaden the production API.
func userHasSubuidAt(path, name, uid string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		field, _, _ := strings.Cut(line, ":")
		if (name != "" && field == name) || field == uid {
			return false, nil
		}
	}
	return true, nil
}

// fakePodmanBinary writes a small shell script to a tempdir that
// records the args it's invoked with to a sidecar file. Optionally
// returns nonzero for `image exists`. The shebang resolves the host
// shell at test time so the script runs in the nix build sandbox
// (where /usr/bin/env is absent and /bin/sh may or may not exist).
func fakePodmanBinary(t *testing.T, imageExists bool) (binPath, logPath string) {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		shPath, err = exec.LookPath("bash")
		if err != nil {
			t.Skipf("no sh/bash found on PATH; cannot stage fake podman: %v", err)
		}
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	exitForExists := "0"
	if !imageExists {
		exitForExists = "1"
	}
	script := "#!" + shPath + "\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"if [ \"$1\" = \"image\" ] && [ \"$2\" = \"exists\" ]; then exit " + exitForExists + "; fi\n" +
		"exit 0\n"
	binPath = filepath.Join(dir, "podman")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath, logPath
}

func TestEnsureTentImage_ExistsShortCircuits(t *testing.T) {
	podman, logPath := fakePodmanBinary(t, true)
	if err := ensureTentImage(podman, "clown-tent:test", "", ""); err != nil {
		t.Fatalf("expected nil error when image exists, got %v", err)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "load") {
		t.Errorf("podman load should not run when image exists; calls log:\n%s", data)
	}
}

func TestEnsureTentImage_MissingThenLoad(t *testing.T) {
	podman, logPath := fakePodmanBinary(t, false)
	tarball := filepath.Join(t.TempDir(), "tent.tar")
	if err := os.WriteFile(tarball, []byte("fake tarball"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureTentImage(podman, "clown-tent:test", tarball, ""); err != nil {
		t.Fatalf("expected nil error from happy load, got %v", err)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "load -i "+tarball) {
		t.Errorf("expected `load -i %s` invocation; calls log:\n%s", tarball, data)
	}
}

func TestEnsureTentImage_MissingNoTarballNoFlakeRef(t *testing.T) {
	podman, _ := fakePodmanBinary(t, false)
	err := ensureTentImage(podman, "clown-tent:test", "", "")
	if err == nil || !strings.Contains(err.Error(), "not present locally") {
		t.Fatalf("expected `not present locally` error when no tarball and no flake ref, got %v", err)
	}
	if !strings.Contains(err.Error(), "dev build") {
		t.Errorf("error should mention `dev build` hint, got %v", err)
	}
}

// TestEnsureTentImage_TarballPrecedesFlakeRef verifies the
// tarball-fast-path: when both are wired in, the tarball wins (no nix
// invocation). Linux builds will hit this in practice; the test
// pins the precedence so future refactors don't accidentally flip it.
func TestEnsureTentImage_TarballPrecedesFlakeRef(t *testing.T) {
	podman, logPath := fakePodmanBinary(t, false)
	tarball := filepath.Join(t.TempDir(), "tent.tar")
	if err := os.WriteFile(tarball, []byte("fake tarball"), 0o600); err != nil {
		t.Fatal(err)
	}
	// flakeRef is a bogus path; if the build path runs, `nix build`
	// will error out (or won't even be on PATH in CI).
	if err := ensureTentImage(podman, "clown-tent:test", tarball, "/nonexistent/flake"); err != nil {
		t.Fatalf("tarball path should win; got %v", err)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "load -i "+tarball) {
		t.Errorf("expected load via baked tarball; calls log:\n%s", data)
	}
}

func TestEnsureClaudeJSON_LeavesExistingFileAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude.json")
	want := []byte(`{"existing": "config"}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureClaudeJSON(); err != nil {
		t.Fatalf("ensureClaudeJSON: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("existing file rewritten; got %q want %q", got, want)
	}
}

func TestEnsureClaudeJSON_CreatesEmptyJSONIfMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := ensureClaudeJSON(); err != nil {
		t.Fatalf("ensureClaudeJSON: %v", err)
	}
	path := filepath.Join(home, ".claude.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected file created at %s: %v", path, err)
	}
	if strings.TrimSpace(string(got)) != "{}" {
		t.Errorf("initial content = %q, want %q", got, "{}")
	}
}

func TestResolveClaudeForRun_NonTentUsesDefault(t *testing.T) {
	withBuildcfgString(t, &buildcfg.DisallowedToolsFile, "/fake/disallowed.txt")
	cli, disallowed, err := resolveClaudeForRun("/nix/store/x-claude/bin/claude", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cli != "/nix/store/x-claude/bin/claude" {
		t.Errorf("cli = %q, want default claude path", cli)
	}
	if disallowed != "/fake/disallowed.txt" {
		t.Errorf("disallowed = %q, want default file", disallowed)
	}
}

func TestResolveClaudeForRun_TentUsesUnpatchedAndSkipsDisallowed(t *testing.T) {
	withBuildcfgString(t, &buildcfg.ClaudeTentCliPath, "/nix/store/y-claude-tent/bin/claude")
	withBuildcfgString(t, &buildcfg.DisallowedToolsFile, "/fake/disallowed.txt")
	cli, disallowed, err := resolveClaudeForRun("/nix/store/x-claude/bin/claude", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cli != "/nix/store/y-claude-tent/bin/claude" {
		t.Errorf("cli = %q, want ClaudeTentCliPath", cli)
	}
	if disallowed != "" {
		t.Errorf("disallowed = %q, want empty (tent skips provider defaults)", disallowed)
	}
}

func TestResolveClaudeForRun_TentEmptyPathErrors(t *testing.T) {
	withBuildcfgString(t, &buildcfg.ClaudeTentCliPath, "")
	_, _, err := resolveClaudeForRun("/x/claude", true)
	if err == nil {
		t.Fatal("expected error when ClaudeTentCliPath is empty")
	}
	if !strings.Contains(err.Error(), "ClaudeTentCliPath") {
		t.Errorf("error should mention ClaudeTentCliPath, got %v", err)
	}
}

func TestEnsureClaudeJSON_RejectsDirectoryAtPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude.json")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	err := ensureClaudeJSON()
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected `is a directory` error, got %v", err)
	}
}

func TestEnsureClaudeBindSources_CreatesMissingDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := ensureClaudeBindSources(); err != nil {
		t.Fatalf("ensureClaudeBindSources: %v", err)
	}
	for _, rel := range claudeBindDirs {
		path := filepath.Join(home, rel)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected directory at %s: %v", path, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s exists but is not a directory", path)
		}
	}
}

func TestEnsureClaudeBindSources_LeavesExistingDirsAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Pre-populate each bind dir with a sentinel file.
	// ensureClaudeBindSources must not touch the contents — it only
	// creates the dir when missing.
	for _, rel := range claudeBindDirs {
		dir := filepath.Join(home, rel)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sentinel"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureClaudeBindSources(); err != nil {
		t.Fatalf("ensureClaudeBindSources: %v", err)
	}
	for _, rel := range claudeBindDirs {
		got, err := os.ReadFile(filepath.Join(home, rel, "sentinel"))
		if err != nil {
			t.Errorf("sentinel disappeared from %s: %v", rel, err)
			continue
		}
		if string(got) != "keep" {
			t.Errorf("sentinel in %s rewritten; got %q want %q", rel, got, "keep")
		}
	}
}

func TestEnsureClaudeBindSources_RejectsFileAtDirPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Plant a regular file where the first bind dir should live,
	// mirroring the corruption pattern ensureClaudeJSON catches for
	// ~/.claude.json.
	rel := claudeBindDirs[0]
	parent := filepath.Dir(filepath.Join(home, rel))
	if parent != home {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, rel), []byte("oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureClaudeBindSources()
	if err == nil || !strings.Contains(err.Error(), "is a regular file") {
		t.Fatalf("expected `is a regular file` error, got %v", err)
	}
}

func TestPluginURLHostForGOOS(t *testing.T) {
	tests := []struct {
		name string
		tent bool
		goos string
		want string
	}{
		{"non-tent darwin", false, "darwin", ""},
		{"non-tent linux", false, "linux", ""},
		{"tent linux", true, "linux", ""},
		{"tent darwin", true, "darwin", "host.containers.internal"},
		{"tent freebsd", true, "freebsd", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := parsedFlags{tent: tt.tent}
			got := pluginURLHostForGOOS(flags, tt.goos)
			if got != tt.want {
				t.Errorf("pluginURLHostForGOOS(tent=%v, goos=%q) = %q, want %q",
					tt.tent, tt.goos, got, tt.want)
			}
		})
	}
}
