package bridgewire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
