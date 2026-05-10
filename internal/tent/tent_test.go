package tent

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildArgs_DefaultShape(t *testing.T) {
	opts := Options{
		Image:          "clown-tent:test",
		Workdir:        "/home/u/repo",
		Home:           "/home/u",
		TmpDir:         "/tmp",
		EnvPassthrough: []string{"HOME", "ANTHROPIC_API_KEY"},
	}
	got := BuildArgs("/nix/store/aaa-claude/bin/claude", []string{"--version"}, opts)

	wantHead := []string{
		"run", "--rm", "-i",
		"--network=host", "--userns=keep-id",
		"--volume", "/nix/store:/nix/store:ro",
		"--volume", "/home/u/repo:/home/u/repo",
		"--volume", "/home/u/.claude:/home/u/.claude",
		"--volume", "/home/u/.config/claude:/home/u/.config/claude",
		"--volume", "/home/u/.claude.json:/home/u/.claude.json",
		"--volume", "/tmp:/tmp",
		"--workdir", "/home/u/repo",
		"--env", "HOME",
		"--env", "ANTHROPIC_API_KEY",
		"clown-tent:test",
		"/nix/store/aaa-claude/bin/claude",
		"--version",
	}
	if !slices.Equal(got, wantHead) {
		t.Fatalf("argv mismatch:\n got: %q\nwant: %q", got, wantHead)
	}
}

func TestBuildArgs_TtyAddsDashT(t *testing.T) {
	opts := Options{
		Image:   "img",
		Workdir: "/w",
		Home:    "/h",
		Tty:     true,
	}
	got := BuildArgs("/c", nil, opts)

	tIdx := slices.Index(got, "-t")
	iIdx := slices.Index(got, "-i")
	if tIdx == -1 {
		t.Fatalf("-t flag missing when Tty=true; got %q", got)
	}
	if tIdx != iIdx+1 {
		t.Errorf("-t should immediately follow -i; got -i at %d, -t at %d", iIdx, tIdx)
	}
}

func TestBuildArgs_NoTtyOmitsDashT(t *testing.T) {
	opts := Options{Image: "img", Workdir: "/w", Home: "/h"}
	got := BuildArgs("/c", nil, opts)
	if slices.Contains(got, "-t") {
		t.Errorf("-t must not appear when Tty=false; got %q", got)
	}
}

func TestBuildArgs_PluginDirsMountedReadOnly(t *testing.T) {
	opts := Options{
		Image:      "clown-tent:test",
		Workdir:    "/w",
		Home:       "/h",
		PluginDirs: []string{"/tmp/staged-a", "/tmp/staged-b"},
	}
	got := BuildArgs("/c", nil, opts)

	if !containsPair(got, "--volume", "/tmp/staged-a:/tmp/staged-a:ro") {
		t.Errorf("missing read-only mount for plugin dir a; got %q", got)
	}
	if !containsPair(got, "--volume", "/tmp/staged-b:/tmp/staged-b:ro") {
		t.Errorf("missing read-only mount for plugin dir b; got %q", got)
	}
}

func TestBuildArgs_ClaudeArgsPreservedAtEnd(t *testing.T) {
	opts := Options{Image: "img", Workdir: "/w", Home: "/h"}
	got := BuildArgs("/c", []string{"--foo", "bar", "--baz=qux"}, opts)

	if got[len(got)-4] != "/c" {
		t.Fatalf("claude binary not at expected position: got %q", got)
	}
	tail := got[len(got)-3:]
	want := []string{"--foo", "bar", "--baz=qux"}
	if !slices.Equal(tail, want) {
		t.Fatalf("claude args mangled: got %q want %q", tail, want)
	}
}

func TestBuildArgs_ImageImmediatelyBeforeBinary(t *testing.T) {
	opts := Options{Image: "img:1", Workdir: "/w", Home: "/h"}
	got := BuildArgs("/bin/claude", []string{"a"}, opts)

	idx := slices.Index(got, "img:1")
	if idx == -1 {
		t.Fatalf("image not in argv: %q", got)
	}
	if got[idx+1] != "/bin/claude" {
		t.Fatalf("image not immediately followed by binary: %q", got)
	}
}

func TestBuildArgs_NoNixStoreWritable(t *testing.T) {
	opts := Options{Image: "img", Workdir: "/w", Home: "/h"}
	got := BuildArgs("/c", nil, opts)

	for i, a := range got {
		if a == "--volume" && i+1 < len(got) && strings.HasPrefix(got[i+1], "/nix/store:") {
			if !strings.HasSuffix(got[i+1], ":ro") {
				t.Fatalf("/nix/store mount must be read-only: %q", got[i+1])
			}
		}
	}
}

func TestBuildArgs_NoBlankEnvOrPluginDirs(t *testing.T) {
	opts := Options{
		Image:          "img",
		Workdir:        "/w",
		Home:           "/h",
		PluginDirs:     []string{"", "/real"},
		EnvPassthrough: []string{"", "REAL"},
	}
	got := BuildArgs("/c", nil, opts)

	if containsPair(got, "--volume", ":") || containsPair(got, "--volume", "::ro") {
		t.Errorf("blank plugin dir produced a mount: %q", got)
	}
	for i, a := range got {
		if a == "--env" && i+1 < len(got) && got[i+1] == "" {
			t.Errorf("blank env var name passed through: idx %d", i)
		}
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
