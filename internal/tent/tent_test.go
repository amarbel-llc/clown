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

func TestBuildArgs_ExtraBindsMountedWritable(t *testing.T) {
	opts := Options{
		Image:      "clown-tent:test",
		Workdir:    "/w",
		Home:       "/h",
		ExtraBinds: []string{"/repo/.git", "/secrets"},
	}
	got := BuildArgs("/c", nil, opts)

	if !containsPair(got, "--volume", "/repo/.git:/repo/.git") {
		t.Errorf("missing writable mount for extra bind /repo/.git; got %q", got)
	}
	if !containsPair(got, "--volume", "/secrets:/secrets") {
		t.Errorf("missing writable mount for extra bind /secrets; got %q", got)
	}
	for i, a := range got {
		if a == "--volume" && i+1 < len(got) {
			if strings.HasPrefix(got[i+1], "/repo/.git:") && strings.HasSuffix(got[i+1], ":ro") {
				t.Errorf("extra bind /repo/.git emitted as :ro: %q", got[i+1])
			}
		}
	}
}

func TestBuildArgs_BlankExtraBindsSkipped(t *testing.T) {
	opts := Options{
		Image:      "img",
		Workdir:    "/w",
		Home:       "/h",
		ExtraBinds: []string{"", "/real"},
	}
	got := BuildArgs("/c", nil, opts)
	if containsPair(got, "--volume", ":") {
		t.Errorf("blank extra bind produced a mount: %q", got)
	}
	if !containsPair(got, "--volume", "/real:/real") {
		t.Errorf("non-blank extra bind /real not mounted: %q", got)
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

func TestBuildArgs_PathOverrideEmitsEnvPath(t *testing.T) {
	opts := Options{
		Image:        "img",
		Workdir:      "/w",
		Home:         "/h",
		PathOverride: "/nix/store/aaa-foo/bin:/nix/store/bbb-bar/bin",
	}
	got := BuildArgs("/c", nil, opts)

	if !containsPair(got, "--env", "PATH=/nix/store/aaa-foo/bin:/nix/store/bbb-bar/bin") {
		t.Errorf("PathOverride did not produce --env PATH=...; got %q", got)
	}
}

func TestBuildArgs_NoPathOverrideOmitsEnvPath(t *testing.T) {
	opts := Options{Image: "img", Workdir: "/w", Home: "/h"}
	got := BuildArgs("/c", nil, opts)

	for i, a := range got {
		if a == "--env" && i+1 < len(got) && strings.HasPrefix(got[i+1], "PATH=") {
			t.Errorf("--env PATH=... emitted without PathOverride: %q", got[i+1])
		}
	}
}

func TestBuildArgs_ReadOnlyBindsEmittedRO(t *testing.T) {
	opts := Options{
		Image:         "img",
		Workdir:       "/w",
		Home:          "/h",
		ReadOnlyBinds: []string{"/nix/var", "/etc/nix", "/h/.nix-profile"},
	}
	got := BuildArgs("/c", nil, opts)

	for _, p := range opts.ReadOnlyBinds {
		want := p + ":" + p + ":ro"
		if !containsPair(got, "--volume", want) {
			t.Errorf("missing read-only mount for %q; got %q", p, got)
		}
	}
}

func TestBuildArgs_ReadOnlyBindsBlanksSkipped(t *testing.T) {
	opts := Options{
		Image:         "img",
		Workdir:       "/w",
		Home:          "/h",
		ReadOnlyBinds: []string{"", "/etc/nix", ""},
	}
	got := BuildArgs("/c", nil, opts)

	if containsPair(got, "--volume", ":") || containsPair(got, "--volume", "::ro") {
		t.Errorf("blank read-only bind produced a mount: %q", got)
	}
	if !containsPair(got, "--volume", "/etc/nix:/etc/nix:ro") {
		t.Errorf("non-blank read-only bind /etc/nix not mounted: %q", got)
	}
}

func TestBuildArgs_SSHAuthSockEmitted(t *testing.T) {
	opts := Options{
		Image:       "img",
		Workdir:     "/w",
		Home:        "/h",
		SSHAuthSock: "/run/user/1001/keyring/ssh",
	}
	got := BuildArgs("/c", nil, opts)

	want := "/run/user/1001/keyring/ssh:/run/user/1001/keyring/ssh"
	if !containsPair(got, "--volume", want) {
		t.Errorf("missing SSH socket bind; got %q", got)
	}
	// Bind must be writable (no :ro suffix) — see the BuildArgs comment.
	for i, a := range got {
		if a == "--volume" && i+1 < len(got) && got[i+1] == want+":ro" {
			t.Errorf("SSH socket mount emitted as :ro: %q", got[i+1])
		}
	}
}

func TestBuildArgs_NoSSHAuthSockOmitsBind(t *testing.T) {
	opts := Options{Image: "img", Workdir: "/w", Home: "/h"}
	got := BuildArgs("/c", nil, opts)

	for i, a := range got {
		if a == "--volume" && i+1 < len(got) {
			v := got[i+1]
			// No volume value should reference ssh-agent style paths
			// when SSHAuthSock is empty.
			if strings.Contains(v, "/keyring/ssh") || strings.Contains(v, "/ssh-agent") {
				t.Errorf("ssh-related mount emitted with no SSHAuthSock set: %q", v)
			}
		}
	}
}

func TestDefaultEnvPassthrough_IncludesSSHAuthSock(t *testing.T) {
	if !slices.Contains(DefaultEnvPassthrough, "SSH_AUTH_SOCK") {
		t.Errorf("DefaultEnvPassthrough must include SSH_AUTH_SOCK; got %v", DefaultEnvPassthrough)
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
