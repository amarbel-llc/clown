package sessionstatus

import (
	"encoding/json"
	"os"
	"testing"
)

// clown#342: the posture is written so a holder sees it, and sees it change.
func TestSetUnattendedIsWritten(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	w, err := Open("p-posture", "repo/branch", nil)
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	path, err := Path("p-posture")
	if err != nil {
		t.Fatal(err)
	}
	read := func() Status {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var st Status
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if read().Unattended {
		t.Fatal("unattended before anything reported it")
	}
	w.SetUnattended(true)
	if !read().Unattended {
		t.Fatal("SetUnattended(true) not written")
	}
	w.SetUnattended(false)
	if read().Unattended {
		t.Fatal("SetUnattended(false) not written")
	}
	var none *Writer
	none.SetUnattended(true) // a nil writer is a no-op
}
