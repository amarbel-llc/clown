package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/logcap"
)

// close-self-run.log (and every log opened through openLogForAppend) rotates
// to one ".1" generation once it is over the cap (clown#321).
func TestOpenLogForAppendRotatesAnOverCapLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-self-run.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("l", logcap.MaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openLogForAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, err := os.Stat(path)
	if err != nil || fi.Size() != 0 {
		t.Fatalf("live log after rotation: %v, %v", fi, err)
	}
	if old, err := os.Stat(path + ".1"); err != nil || old.Size() != logcap.MaxBytes+1 {
		t.Fatalf("rotated generation: %v, %v", old, err)
	}
	// Under the cap it is left alone.
	logSelfClose(path, "small")
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Fatal("a second generation appeared")
	}
}
