package bridgewire

import (
	"os"
	"path/filepath"
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

// What is not an absolute cgroup path is refused and writes nothing.
func TestWriteCgroupFileRefusesAnUnusableValue(t *testing.T) {
	for name, cg := range map[string]string{
		"empty": "", "relative": "a/b", "trailing slash": "/a/", "newline": "/a\n", "nul": "/a\x00b", "root only slash": "relative",
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
