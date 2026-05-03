package main

import (
	"os"
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

func TestReadCircusPortfile_Present(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	stateDir := filepath.Join(dir, ".local", "state", "circus")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "portfile"), []byte("127.0.0.1:8080\n"), 0o600); err != nil {
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
