package jobwake

import (
	"os"
	"testing"
	"time"
)

func TestRegisterAndListPresence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "instance-1")
	t.Setenv("CLOWN_GROUP_ID", "repo/branch")
	t.Setenv("CLOWN_GROUP_DESCRIPTION", "fixing the thing")

	now := time.Now()
	if err := RegisterPresence(now); err != nil {
		t.Fatal(err)
	}
	ps, err := ListPresence(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("want one presence record, got %+v", ps)
	}
	p := ps[0]
	if p.SessionKey != "instance-1" || p.ChannelID != ChannelID("instance-1") {
		t.Errorf("key/channel = %q/%q", p.SessionKey, p.ChannelID)
	}
	if p.Decoration != "repo/branch" || p.Description != "fixing the thing" {
		t.Errorf("decoration/description = %q/%q", p.Decoration, p.Description)
	}
}

func TestRemovePresence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "instance-1")
	t.Setenv("CLOWN_GROUP_ID", "")
	now := time.Now()
	if err := RegisterPresence(now); err != nil {
		t.Fatal(err)
	}
	RemovePresence()
	ps, err := ListPresence(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("presence must be empty after remove, got %+v", ps)
	}
}

func TestListPresencePrunesStale(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "instance-1")
	t.Setenv("CLOWN_GROUP_ID", "")
	past := time.Now().Add(-2 * presenceStale)
	if err := RegisterPresence(past); err != nil {
		t.Fatal(err)
	}
	// A read "now" sees the entry as stale, drops it, and removes the file.
	ps, err := ListPresence(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("stale presence must be pruned, got %+v", ps)
	}
	if _, err := os.Stat(presenceFile(ChannelID("instance-1"))); !os.IsNotExist(err) {
		t.Fatalf("stale presence file must be removed, stat err = %v", err)
	}
}
