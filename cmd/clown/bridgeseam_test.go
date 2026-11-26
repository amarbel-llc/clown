package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

// The verb writes through the real writers: a cgroup file the real reader
// accepts, and a unit record that carries a version and capabilities.
func TestBridgeSeamWriteUsesTheRealWriters(t *testing.T) {
	dir := t.TempDir()
	cg := "/user.slice/clown-agent.slice/u.service"
	cgFile := filepath.Join(dir, bridgewire.CgroupFileName)
	var out, errb bytes.Buffer
	if code := runBridgeSeamWrite([]string{"cgroup-file", "--path", cgFile, "--cgroup", cg}, &out, &errb); code != 0 {
		t.Fatalf("cgroup-file: exit %d: %s", code, errb.String())
	}
	if got, err := bridgewire.ReadCgroupFile(cgFile); err != nil || got != cg {
		t.Errorf("the real reader read %q, %v from what the verb wrote", got, err)
	}

	res := filepath.Join(dir, "result.json")
	out.Reset()
	if code := runBridgeSeamWrite([]string{"result", "--path", res, "--unit", "u.service", "--cgroup", cg, "--principal", "p1", "--bridge", "recorded", "--main-pid", "7"}, &out, &errb); code != 0 {
		t.Fatalf("result: exit %d: %s", code, errb.String())
	}
	raw, err := os.ReadFile(res)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version      int      `json:"version"`
		Capabilities []string `json:"capabilities"`
		Unit, Cgroup string
		Principal    string `json:"principal"`
		Bridge       string `json:"bridge"`
		MainPID      int    `json:"main_pid"`
		UID          int    `json:"uid"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Capabilities) == 0 || got.Principal != "p1" || got.Bridge != "recorded" || got.MainPID != 7 || got.UID != os.Getuid() {
		t.Errorf("unit record = %s", raw)
	}
}

func TestBridgeSeamWriteRefusesWhatTheWritersRefuse(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runBridgeSeamWrite([]string{"cgroup-file", "--path", filepath.Join(dir, "c"), "--cgroup", "relative"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "unusable cgroup") {
		t.Errorf("a relative cgroup: exit %d, %q", code, errb.String())
	}
	for _, args := range [][]string{{}, {"nope", "--path", "x"}, {"cgroup-file"}, {"result", "--path"}} {
		if code := runBridgeSeamWrite(args, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
