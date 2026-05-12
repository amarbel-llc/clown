package tent

import "testing"

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
