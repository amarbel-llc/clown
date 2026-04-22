package main

import (
	"reflect"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want parsedArgs
	}{
		{
			name: "plugin-dir and downstream only",
			in:   []string{"--plugin-dir", "a", "--", "echo", "hi"},
			want: parsedArgs{
				pluginDirs: []string{"a"},
				downstream: []string{"echo", "hi"},
			},
		},
		{
			name: "multiple plugin dirs",
			in:   []string{"--plugin-dir", "a", "--plugin-dir", "b", "--", "cmd"},
			want: parsedArgs{
				pluginDirs: []string{"a", "b"},
				downstream: []string{"cmd"},
			},
		},
		{
			name: "skip-failed + verbose long",
			in:   []string{"--skip-failed", "--verbose", "--plugin-dir", "d", "--", "cmd"},
			want: parsedArgs{
				pluginDirs: []string{"d"},
				downstream: []string{"cmd"},
				skipFailed: true,
				verbose:    true,
			},
		},
		{
			name: "verbose short",
			in:   []string{"-v", "--", "cmd"},
			want: parsedArgs{
				downstream: []string{"cmd"},
				verbose:    true,
			},
		},
		{
			name: "no downstream marker",
			in:   []string{"--plugin-dir", "a"},
			want: parsedArgs{
				pluginDirs: []string{"a"},
			},
		},
		{
			name: "disable-clown-protocol alone",
			in:   []string{"--disable-clown-protocol", "--", "cmd"},
			want: parsedArgs{
				downstream:           []string{"cmd"},
				disableClownProtocol: true,
			},
		},
		{
			name: "disable-clown-protocol combined with skip-failed and plugin-dir",
			in: []string{
				"--disable-clown-protocol",
				"--skip-failed",
				"--plugin-dir", "a",
				"--plugin-dir", "b",
				"--verbose",
				"--", "cmd",
			},
			want: parsedArgs{
				pluginDirs:           []string{"a", "b"},
				downstream:           []string{"cmd"},
				skipFailed:           true,
				disableClownProtocol: true,
				verbose:              true,
			},
		},
		{
			name: "disable-clown-protocol after -- is downstream arg, not flag",
			in:   []string{"--", "cmd", "--disable-clown-protocol"},
			want: parsedArgs{
				downstream: []string{"cmd", "--disable-clown-protocol"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.in)
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseArgs = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestBuildDownstreamArgs(t *testing.T) {
	cases := []struct {
		name          string
		downstream    []string
		pluginDirs    []string
		dirMap        map[string]string
		mcpConfigPath string
		want          []string
	}{
		{
			name:       "no plugins, no mcp config",
			downstream: []string{"claude", "--foo"},
			want:       []string{"claude", "--foo"},
		},
		{
			name:          "mcp config only",
			downstream:    []string{"claude", "--foo"},
			mcpConfigPath: "/tmp/mcp.json",
			want:          []string{"claude", "--mcp-config", "/tmp/mcp.json", "--foo"},
		},
		{
			name:       "plugin dirs pass through (no dirMap)",
			downstream: []string{"claude"},
			pluginDirs: []string{"/a", "/b"},
			want:       []string{"claude", "--plugin-dir", "/a", "--plugin-dir", "/b"},
		},
		{
			name:       "plugin dirs substituted by dirMap",
			downstream: []string{"claude"},
			pluginDirs: []string{"/orig/a", "/orig/b"},
			dirMap:     map[string]string{"/orig/a": "/stage/a"},
			want:       []string{"claude", "--plugin-dir", "/stage/a", "--plugin-dir", "/orig/b"},
		},
		{
			name:          "full combination",
			downstream:    []string{"claude", "chat"},
			pluginDirs:    []string{"/orig/a"},
			dirMap:        map[string]string{"/orig/a": "/stage/a"},
			mcpConfigPath: "/tmp/mcp.json",
			want:          []string{"claude", "--mcp-config", "/tmp/mcp.json", "--plugin-dir", "/stage/a", "chat"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildDownstreamArgs(tc.downstream, tc.pluginDirs, tc.dirMap, tc.mcpConfigPath)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildDownstreamArgs = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := []struct {
		name string
		in   []string
	}{
		{"unknown flag", []string{"--what"}},
		{"plugin-dir missing arg", []string{"--plugin-dir"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseArgs(tc.in); err == nil {
				t.Errorf("expected error for %v, got nil", tc.in)
			}
		})
	}
}
