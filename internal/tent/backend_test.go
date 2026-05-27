package tent

import (
	"slices"
	"testing"
)

// TestPodmanBackend_ImageExistsArgs pins the Podman image-exists
// invocation shape. `--connection` goes before the subcommand when
// set.
func TestPodmanBackend_ImageExistsArgs(t *testing.T) {
	cases := []struct {
		name     string
		conn     string
		wantArgs []string
	}{
		{"no connection", "", []string{"image", "exists", "ref:tag"}},
		{"with connection", "clown-dev", []string{"--connection", "clown-dev", "image", "exists", "ref:tag"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewPodman("/nix/store/aaa-podman/bin/podman", tc.conn)
			gotPath, gotArgs := b.ImageExistsArgs("ref:tag")
			if gotPath != "/nix/store/aaa-podman/bin/podman" {
				t.Errorf("path = %q, want podman path", gotPath)
			}
			if !slices.Equal(gotArgs, tc.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

// TestPodmanBackend_LoadImageArgs pins the Podman load invocation.
func TestPodmanBackend_LoadImageArgs(t *testing.T) {
	b := NewPodman("/nix/store/aaa-podman/bin/podman", "clown-dev")
	gotPath, gotArgs := b.LoadImageArgs("/tmp/img.tar")
	if gotPath != "/nix/store/aaa-podman/bin/podman" {
		t.Errorf("path = %q", gotPath)
	}
	want := []string{"--connection", "clown-dev", "load", "-i", "/tmp/img.tar"}
	if !slices.Equal(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

// TestPodmanBackend_RunArgs pins the Podman run argv shape:
// connection flag before `run`, binary path at argv[0].
func TestPodmanBackend_RunArgs(t *testing.T) {
	b := NewPodman("/nix/store/aaa-podman/bin/podman", "clown-dev")
	opts := Options{
		Image:   "clown-tent:test",
		Workdir: "/w",
		Home:    "/h",
	}
	got := b.RunArgs("/bin/claude", []string{"--version"}, opts)

	if len(got) < 4 {
		t.Fatalf("argv too short: %v", got)
	}
	if got[0] != "/nix/store/aaa-podman/bin/podman" {
		t.Errorf("argv[0] = %q, want podman path", got[0])
	}
	if got[1] != "--connection" || got[2] != "clown-dev" || got[3] != "run" {
		t.Errorf("argv[1..3] = %v %v %v, want `--connection clown-dev run`", got[1], got[2], got[3])
	}
}

// TestLimaBackend_ImageExistsArgs pins the Lima image-exists shape.
// nerdctl uses `image inspect` rather than `image exists` (the
// semantic equivalent: exit non-zero when absent).
func TestLimaBackend_ImageExistsArgs(t *testing.T) {
	b := NewLima("/nix/store/aaa-lima/bin/limactl", "clown-tent")
	gotPath, gotArgs := b.ImageExistsArgs("ref:tag")
	if gotPath != "/nix/store/aaa-lima/bin/limactl" {
		t.Errorf("path = %q", gotPath)
	}
	want := []string{"shell", "clown-tent", "--", "sudo", "nerdctl", "image", "inspect", "ref:tag"}
	if !slices.Equal(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

// TestLimaBackend_LoadImageArgs pins the Lima load shape.
func TestLimaBackend_LoadImageArgs(t *testing.T) {
	b := NewLima("/nix/store/aaa-lima/bin/limactl", "clown-tent")
	gotPath, gotArgs := b.LoadImageArgs("/tmp/img.tar")
	if gotPath != "/nix/store/aaa-lima/bin/limactl" {
		t.Errorf("path = %q", gotPath)
	}
	want := []string{"shell", "clown-tent", "--", "sudo", "nerdctl", "load", "-i", "/tmp/img.tar"}
	if !slices.Equal(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

// TestLimaBackend_RunArgs pins the Lima run argv shape: limactl
// path at argv[0], then `shell <machine> -- sudo nerdctl run ...`.
// Crucially: no `--connection` flag (nerdctl has no equivalent;
// the machine identity comes from `limactl shell <name>`).
func TestLimaBackend_RunArgs(t *testing.T) {
	b := NewLima("/nix/store/aaa-lima/bin/limactl", "clown-tent")
	opts := Options{
		Image:          "clown-tent:test",
		Workdir:        "/w",
		Home:           "/h",
		ConnectionName: "should-be-ignored",
	}
	got := b.RunArgs("/bin/claude", []string{"--version"}, opts)

	if len(got) < 7 {
		t.Fatalf("argv too short: %v", got)
	}
	wantHead := []string{
		"/nix/store/aaa-lima/bin/limactl",
		"shell", "clown-tent", "--", "sudo", "nerdctl", "run",
	}
	for i, w := range wantHead {
		if got[i] != w {
			t.Errorf("argv[%d] = %q, want %q (full head: %v)", i, got[i], w, got[:len(wantHead)])
		}
	}
	// And NO --connection in the argv anywhere — the in-Lima nerdctl
	// has no such flag.
	if slices.Contains(got, "--connection") {
		t.Errorf("Lima RunArgs leaked --connection into argv: %v", got)
	}
}

// TestLimaBackend_RunArgs_PreservesMountList confirms BuildArgs's
// mount-list output ends up in the argv. Sanity check that the
// `BuildArgs` call from Lima.RunArgs still happens.
func TestLimaBackend_RunArgs_PreservesMountList(t *testing.T) {
	b := NewLima("/nix/store/aaa-lima/bin/limactl", "clown-tent")
	opts := Options{
		Image:   "clown-tent:test",
		Workdir: "/w",
		Home:    "/h",
	}
	got := b.RunArgs("/bin/claude", nil, opts)
	if !slices.Contains(got, "/nix/store:/nix/store:ro") {
		t.Errorf("Lima RunArgs dropped the /nix/store bind: %v", got)
	}
	if !slices.Contains(got, "clown-tent:test") {
		t.Errorf("Lima RunArgs dropped the image ref: %v", got)
	}
}
