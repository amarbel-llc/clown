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
