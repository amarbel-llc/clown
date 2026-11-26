package agentscope

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// F12 review follow-up (FDR 0020).

// spinclass reads the result with plain json.Unmarshal: the key is
// serve_socket.
func TestWrittenResultSpellsServeSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-unit.json")
	if err := WriteResult(path, Result{Unit: "u-1.service", ServeSocket: "/run/user/1001/clown/serve/p1/spinclass.sock"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["serve_socket"] != "/run/user/1001/clown/serve/p1/spinclass.sock" {
		t.Errorf("serve_socket = %v in %s", got["serve_socket"], raw)
	}
	caps, _ := got["capabilities"].([]any)
	found := false
	for _, c := range caps {
		found = found || c == "frontend-serve-socket"
	}
	if !found {
		t.Errorf("written capabilities %v lack frontend-serve-socket", caps)
	}
}
