package main

import (
	"reflect"
	"testing"
)

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
			want: parsedFlags{provider: "codex"},
		},
		{
			name: "provider= syntax",
			in:   []string{"--provider=codex"},
			want: parsedFlags{provider: "codex"},
		},
		{
			name: "clean flag",
			in:   []string{"--clean"},
			want: parsedFlags{provider: "claude", clean: true},
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
			name: "unknown args forwarded",
			in:   []string{"chat", "--model", "sonnet"},
			want: parsedFlags{provider: "claude", forwarded: []string{"chat", "--model", "sonnet"}},
		},
		{
			name: "clown flags before forwarded args",
			in:   []string{"--skip-failed", "--verbose", "chat", "--resume"},
			want: parsedFlags{
				provider:   "claude",
				skipFailed: true,
				verbose:    true,
				forwarded:  []string{"chat", "--resume"},
			},
		},
		{
			name: "all flags combined",
			in: []string{
				"--provider", "claude",
				"--skip-failed",
				"--disable-clown-protocol",
				"--verbose",
				"chat",
			},
			want: parsedFlags{
				provider:             "claude",
				skipFailed:           true,
				disableClownProtocol: true,
				verbose:              true,
				forwarded:            []string{"chat"},
			},
		},
		{
			name: "version only matches as first arg",
			in:   []string{"--verbose", "version"},
			want: parsedFlags{
				provider:  "claude",
				verbose:   true,
				forwarded: []string{"version"},
			},
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
		name string
		in   []string
	}{
		{"provider missing arg", []string{"--provider"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFlags(tc.in); err == nil {
				t.Errorf("expected error for %v, got nil", tc.in)
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
