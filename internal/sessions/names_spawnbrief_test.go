package sessions

import "testing"

// A spawned session's brief is recorded by digest only, so a restart's
// relaunch can leave exactly that operand out (the brief is large and is
// already in the conversation). The last record for the session wins and a
// later record without one does not clear it.
func TestSpawnBriefDigestOf(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, ok, err := SpawnBriefDigestOf("id-1"); ok || err != nil {
		t.Fatalf("empty sidecar: ok %v err %v, want not recorded", ok, err)
	}
	// A record with only the digest (no name, profile or scope) is still written.
	if err := RecordSession(NameRecord{SessionID: "id-1", SpawnBriefSHA256: BriefDigest("do the task")}); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-2", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-1", Name: "bozo"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := SpawnBriefDigestOf("id-1")
	if err != nil || !ok || got != BriefDigest("do the task") {
		t.Fatalf("SpawnBriefDigestOf(id-1) = %q, %v, %v; want the recorded digest, kept across a later record without one", got, ok, err)
	}
	if _, ok, _ := SpawnBriefDigestOf("id-2"); ok {
		t.Error("id-2 never had a brief recorded")
	}
	if BriefDigest("a") == BriefDigest("b") || len(BriefDigest("a")) != 64 {
		t.Errorf("BriefDigest = %q / %q, want distinct sha256 hex", BriefDigest("a"), BriefDigest("b"))
	}
}
