package bridgewire

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The file is exactly the path: 0600, a regular file, no trailing newline, no
// temporary file left beside it.
func TestWriteCgroupFileIsExactlyThePathAt0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CgroupFileName)
	const cg = "/user.slice/user-1001.slice/user@1001.service/clown-agent.slice/u.service"
	if err := WriteCgroupFile(path, cg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != cg {
		t.Fatalf("file = %q, %v; want exactly %q", b, err, cg)
	}
	fi, _ := os.Lstat(path)
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want a regular 0600 file", fi.Mode())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("directory holds %v, want only the file", ents)
	}
}

// A later write replaces the value whole, and replaces a symlink rather than
// writing through it.
func TestWriteCgroupFileReplacesAndNeverFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, CgroupFileName)
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if err := WriteCgroupFile(path, "/a/b"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("the symlink target was written through: %q", b)
	}
	if err := WriteCgroupFile(path, "/c"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "/c" {
		t.Errorf("file = %q, want /c", b)
	}
}

// The writer's output is always accepted by the reader (one package, so they
// cannot drift), and what the reader refuses is exactly what is unsafe to trust.
func TestReadCgroupFileAcceptsWhatTheWriterWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), CgroupFileName)
	const cg = "/user.slice/x.slice/u.service"
	if err := WriteCgroupFile(path, cg); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCgroupFile(path); err != nil || got != cg {
		t.Errorf("ReadCgroupFile = %q, %v; want %q", got, err, cg)
	}
	// one trailing newline is tolerated
	if err := os.WriteFile(path, []byte(cg+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCgroupFile(path); err != nil || got != cg {
		t.Errorf("with a trailing newline: %q, %v", got, err)
	}
}

func TestReadCgroupFileRefusesWhatIsNotTrustworthy(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // WriteFile is subject to the umask
			t.Fatal(err)
		}
		return p
	}
	for name, p := range map[string]string{
		"group-writable":        write("gw", "/a/b", 0o620),
		"world-writable":        write("ww", "/a/b", 0o602),
		"empty":                 write("empty", "", 0o600),
		"relative":              write("rel", "a/b", 0o600),
		"trailing slash":        write("slash", "/a/", 0o600),
		"embedded newline":      write("nl", "/a\n/b", 0o600),
		"nul":                   write("nul", "/a\x00b", 0o600),
		"tab":                   write("tab", "/a\tb", 0o600),
		"escape character":      write("esc", "/a\x1b[2Jb", 0o600),
		"delete character":      write("del", "/a\x7fb", 0o600),
		"two trailing newlines": write("nn", "/a/b\n\n", 0o600),
		"over-long":             write("long", "/"+strings.Repeat("x", 2*MaxCgroupFileBytes), 0o600),
		"missing":               filepath.Join(dir, "missing"),
	} {
		if got, err := ReadCgroupFile(p); err == nil {
			t.Errorf("%s: accepted %q", name, got)
		}
	}
	// a readable-by-all file is fine: only write bits are refused
	if got, err := ReadCgroupFile(write("ro", "/a/b", 0o644)); err != nil || got != "/a/b" {
		t.Errorf("0644: %q, %v", got, err)
	}
}

// The owner check, through the seam (a test cannot chown): another uid is
// refused, and a stat that does not say who owns the file is refused too (a
// check that cannot be made is not passed). The honest case still reads.
func TestReadCgroupFileOwnerCheckFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), CgroupFileName)
	if err := WriteCgroupFile(path, "/a/b"); err != nil {
		t.Fatal(err)
	}
	mine := os.Geteuid()
	if got, err := readCgroupFile(path, mine, statOwner); err != nil || got != "/a/b" {
		t.Fatalf("own file: %q, %v", got, err)
	}
	if got, err := readCgroupFile(path, mine+1, statOwner); err == nil {
		t.Errorf("a file of another uid was accepted: %q", got)
	}
	unknown := func(os.FileInfo) (int, bool) { return 0, false }
	if got, err := readCgroupFile(path, mine, unknown); err == nil {
		t.Errorf("a stat that does not name the owner was accepted: %q", got)
	}
	// the owner seam says the owner is "me" while the stat holds another: the
	// stat's answer is what counts
	other := func(os.FileInfo) (int, bool) { return mine + 1, true }
	if got, err := readCgroupFile(path, mine, other); err == nil {
		t.Errorf("a stat naming another owner was accepted: %q", got)
	}
}

// A path that is not a regular file of ours never reads, and never hangs the
// reader: a FIFO is not opened for blocking (O_NONBLOCK), a directory and a
// symlink are refused.
func TestReadCgroupFileRefusesAFIFOADirectoryAndASymlink(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no mkfifo here: %v", err)
	}
	sub := filepath.Join(dir, "dir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("/a/b"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"fifo": fifo, "directory": sub, "symlink": link} {
		done := make(chan error, 1)
		go func() { _, err := ReadCgroupFile(p); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: accepted", name)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("%s: the reader hung", name)
		}
	}
}

// What is not an absolute cgroup path is refused and writes nothing.
func TestWriteCgroupFileRefusesAnUnusableValue(t *testing.T) {
	for name, cg := range map[string]string{
		"empty": "", "relative": "a/b", "trailing slash": "/a/", "newline": "/a\n", "nul": "/a\x00b", "tab": "/a\tb", "delete": "/a\x7f",
	} {
		path := filepath.Join(t.TempDir(), CgroupFileName)
		if err := WriteCgroupFile(path, cg); err == nil {
			t.Errorf("%s: %q was written", name, cg)
		}
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("%s: a file exists after a refusal", name)
		}
	}
	if err := WriteCgroupFile("", "/a"); err == nil {
		t.Error("an empty path was accepted")
	}
}
