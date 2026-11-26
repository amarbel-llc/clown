package unitresult

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The writer writes what it is given and nothing else: no Version and no
// capabilities appear that the caller did not set.
func TestWriteResultWritesExactlyWhatItIsGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "result.json")
	want := Result{Version: 1, Unit: "u.service", Cgroup: "/c/u.service", UID: 1001, Bridge: "recorded"}
	if err := WriteResult(path, want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Result
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Unit != want.Unit || got.Cgroup != want.Cgroup || got.Bridge != "recorded" || got.UID != 1001 || got.Version != 1 {
		t.Errorf("read back %+v from %s", got, raw)
	}
	var zero Result
	zeroPath := filepath.Join(t.TempDir(), "zero.json")
	if err := WriteResult(zeroPath, zero); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(zeroPath)
	var back Result
	if err := json.Unmarshal(raw, &back); err != nil || back.Version != 0 || back.Capabilities != nil {
		t.Errorf("a zero Result gained defaults: %+v (%v)", back, err)
	}
}

// A reader never sees a part of a result: no temporary file is left beside it.
func TestWriteResultLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteResult(filepath.Join(dir, "r.json"), Result{Unit: "u"}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "r.json" {
		t.Errorf("directory holds %v, want only r.json", ents)
	}
}
