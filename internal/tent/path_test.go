package tent

import (
	"errors"
	"testing"
)

func TestFilterPathToNixStore(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "single store bin",
			in:   "/nix/store/aaa-foo/bin",
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "single store sbin kept",
			in:   "/nix/store/bbb-bar/sbin",
			want: "/nix/store/bbb-bar/sbin",
		},
		{
			name: "all non-store entries dropped",
			in:   "/usr/local/bin:/usr/bin:/bin:/home/u/.local/bin",
			want: "",
		},
		{
			name: "mixed preserves store entries in order",
			in:   "/usr/bin:/nix/store/aaa-foo/bin:/home/u/.local/bin:/nix/store/bbb-bar/bin",
			want: "/nix/store/aaa-foo/bin:/nix/store/bbb-bar/bin",
		},
		{
			name: "duplicates preserved",
			in:   "/nix/store/aaa-foo/bin:/nix/store/aaa-foo/bin",
			want: "/nix/store/aaa-foo/bin:/nix/store/aaa-foo/bin",
		},
		{
			name: "trailing colon (empty entry) ignored",
			in:   "/nix/store/aaa-foo/bin:",
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "leading colon (empty entry) ignored",
			in:   ":/nix/store/aaa-foo/bin",
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "store path that isn't a bin dir dropped",
			in:   "/nix/store/aaa-foo/lib:/nix/store/aaa-foo/bin",
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "store root without subdir dropped",
			in:   "/nix/store/aaa-foo:/nix/store/aaa-foo/bin",
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "non-store path containing /bin dropped",
			in:   "/home/u/bin:/nix/store/aaa-foo/bin",
			want: "/nix/store/aaa-foo/bin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterPathToNixStore(tc.in)
			if got != tc.want {
				t.Errorf("FilterPathToNixStore(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// stubResolver returns a resolver that returns table[path] for known
// paths and a generic "not found" error otherwise. Lets RewritePathToNixStore
// tests run without an actual /nix/store on disk.
func stubResolver(table map[string]string) func(string) (string, error) {
	return func(p string) (string, error) {
		if v, ok := table[p]; ok {
			return v, nil
		}
		return "", errors.New("not found")
	}
}

func TestRewritePathToNixStore(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		resolve map[string]string
		want    string
	}{
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "direct store path passes through",
			in:   "/nix/store/aaa-foo/bin",
			resolve: map[string]string{
				"/nix/store/aaa-foo/bin": "/nix/store/aaa-foo/bin",
			},
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "home-manager profile-link rewritten",
			in:   "/home/u/.nix-profile/bin",
			resolve: map[string]string{
				"/home/u/.nix-profile/bin": "/nix/store/zzz-home-manager-path/bin",
			},
			want: "/nix/store/zzz-home-manager-path/bin",
		},
		{
			name: "system profile-link rewritten",
			in:   "/nix/var/nix/profiles/default/bin",
			resolve: map[string]string{
				"/nix/var/nix/profiles/default/bin": "/nix/store/yyy-system-path/bin",
			},
			want: "/nix/store/yyy-system-path/bin",
		},
		{
			name: "non-store-resolving entries dropped",
			in:   "/usr/bin:/home/u/.local/bin",
			resolve: map[string]string{
				"/usr/bin":           "/usr/bin",
				"/home/u/.local/bin": "/home/u/.local/bin",
			},
			want: "",
		},
		{
			name: "unresolved entries dropped",
			in:   "/missing/path:/nix/store/aaa-foo/bin",
			resolve: map[string]string{
				// /missing/path absent → resolver returns error → dropped.
				"/nix/store/aaa-foo/bin": "/nix/store/aaa-foo/bin",
			},
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "order preserved",
			in:   "/home/u/.nix-profile/bin:/nix/store/aaa-foo/bin:/usr/bin:/nix/store/bbb-bar/bin",
			resolve: map[string]string{
				"/home/u/.nix-profile/bin": "/nix/store/zzz-home-manager-path/bin",
				"/nix/store/aaa-foo/bin":   "/nix/store/aaa-foo/bin",
				"/usr/bin":                 "/usr/bin",
				"/nix/store/bbb-bar/bin":   "/nix/store/bbb-bar/bin",
			},
			want: "/nix/store/zzz-home-manager-path/bin:/nix/store/aaa-foo/bin:/nix/store/bbb-bar/bin",
		},
		{
			name: "duplicates after rewrite preserved",
			in:   "/home/u/.nix-profile/bin:/home/u/.local/state/nix/profiles/profile/bin",
			resolve: map[string]string{
				"/home/u/.nix-profile/bin":                      "/nix/store/zzz-hm/bin",
				"/home/u/.local/state/nix/profiles/profile/bin": "/nix/store/zzz-hm/bin",
			},
			want: "/nix/store/zzz-hm/bin:/nix/store/zzz-hm/bin",
		},
		{
			name: "empty entries (consecutive colons) skipped",
			in:   "::/nix/store/aaa-foo/bin:",
			resolve: map[string]string{
				"/nix/store/aaa-foo/bin": "/nix/store/aaa-foo/bin",
			},
			want: "/nix/store/aaa-foo/bin",
		},
		{
			name: "store-adjacent paths (e.g. /nix/var) dropped",
			in:   "/nix/var/nix/profiles/default/lib",
			resolve: map[string]string{
				// Resolves to /nix/var which does NOT start with /nix/store/.
				"/nix/var/nix/profiles/default/lib": "/nix/var/somewhere/lib",
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RewritePathToNixStore(tc.in, stubResolver(tc.resolve))
			if got != tc.want {
				t.Errorf("RewritePathToNixStore(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
