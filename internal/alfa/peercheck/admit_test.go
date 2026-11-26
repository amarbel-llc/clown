package peercheck

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

type admitStep struct {
	File  string `json:"file"`
	Peer  string `json:"peer"`
	Admit bool   `json:"admit"`
}

type admitVector struct {
	Name  string      `json:"name"`
	Steps []admitStep `json:"steps"`
}

// place makes the cgroup file look like the step says, at path.
func place(t *testing.T, path, spec string) {
	t.Helper()
	_ = os.RemoveAll(path)
	switch {
	case spec == "absent":
	case spec == "dir":
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	case spec == "long":
		if err := os.WriteFile(path, []byte("/"+strings.Repeat("x", 2*bridgewire.MaxCgroupFileBytes)), 0o600); err != nil {
			t.Fatal(err)
		}
	case strings.HasPrefix(spec, "symlink:"):
		target := path + ".target"
		if err := os.WriteFile(target, []byte(strings.TrimPrefix(spec, "symlink:")), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	case strings.HasPrefix(spec, "abs:"), strings.HasPrefix(spec, "raw:"):
		if err := os.WriteFile(path, []byte(spec[4:]), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown file spec %q", spec)
	}
}

// The vectors are spinclass's (internal/0/bridge/testdata, the contract's one
// home), vendored byte for byte with their digest file: the pin is that file.
func TestAdmissionVectorsAreSpinclasssBytes(t *testing.T) {
	b, err := os.ReadFile("testdata/admission-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/admission-vectors.json.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x  admission-vectors.json\n", sha256.Sum256(b)); got != string(want) {
		t.Errorf("admission-vectors.json drifted from its .sha256 (%q); re-copy both files from spinclass", got)
	}
}

// The listener's admission decision as vectors (round 2, M4), run by both
// repositories: a missing, empty, unreadable or non-regular file admits nobody;
// the file is read per connection; equality is strict. Steps in one case share
// one file path, so a rewrite or removal between steps shows whether the file
// is re-read.
func TestAdmissionVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/admission-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []admitVector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 20 {
		t.Fatalf("only %d vectors", len(vs))
	}
	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bridge.cgroup")
			for i, s := range v.Steps {
				place(t, path, s.File)
				err := AdmitCgroup(path, s.Peer)
				if (err == nil) != s.Admit {
					t.Errorf("step %d (file %q, peer %q): err = %v, want admit=%v", i, s.File, s.Peer, err, s.Admit)
				}
			}
		})
	}
}
