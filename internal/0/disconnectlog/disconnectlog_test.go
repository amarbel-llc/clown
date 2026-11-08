package disconnectlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const principal = "9eff073f-a924-48c2-b076-8b02d38f1938"

func newRecord(kind string) Record {
	return Record{Session: principal, Plugin: "cutting-garden", Server: "cg", Kind: kind, ClosedBy: ClosedByChild}
}

func TestWriteReadAndListRoundTrip(t *testing.T) {
	root := t.TempDir()
	rec := newRecord(KindChildExit)
	rec.ChildExit = &Exit{Code: 2}
	rec.InFlight = []InFlight{{ID: "7", Method: "tools/call", StartedAt: Now(), ElapsedMS: 1500}}
	got, err := Write(root, rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.N != 1 || got.Version != Version || got.At == "" {
		t.Fatalf("written record = %+v; want n 1, the version and a time", got)
	}
	if want := "clown://session/" + principal + "/plugins/cutting-garden/cg/disconnects/1"; got.URI != want {
		t.Fatalf("uri = %q, want %q", got.URI, want)
	}
	raw, err := Read(root, principal, "cutting-garden", "cg", 1)
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(raw, &back); err != nil || back.Kind != KindChildExit || back.ChildExit == nil || back.ChildExit.Code != 2 || len(back.InFlight) != 1 || back.InFlight[0].Method != "tools/call" {
		t.Fatalf("read back %s (%v)", raw, err)
	}
	if back.Abandoned == nil || back.StderrTail == nil {
		t.Errorf("empty lists must be [] in the JSON, not null: %s", raw)
	}
	list := List(root, principal, "cutting-garden", "cg")
	if len(list) != 1 || list[0].N != 1 || list[0].Kind != KindChildExit || list[0].URI != got.URI {
		t.Fatalf("list = %+v", list)
	}
	if all := ListAll(root, principal); len(all) != 1 || all[0].Plugin != "cutting-garden" || all[0].Server != "cg" {
		t.Fatalf("list all = %+v", all)
	}
	if List(root, "another-session", "cutting-garden", "cg") != nil || ListAll(root, "another-session") != nil {
		t.Error("another principal sees nothing")
	}
	fi, err := os.Stat(filepath.Join(root, SessionDir(principal), "cutting-garden", "cg", "1.json"))
	if err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("record file mode %v (%v); want owner-only", fi.Mode(), err)
	}
}

func TestNumbersIncreaseAndTheOldestGoBeyondTheBound(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= MaxRecords+3; i++ {
		got, err := Write(root, newRecord(KindSignal))
		if err != nil || got.N != i {
			t.Fatalf("write %d: n %d, %v", i, got.N, err)
		}
	}
	list := List(root, principal, "cutting-garden", "cg")
	if len(list) != MaxRecords {
		t.Fatalf("kept %d records, want %d", len(list), MaxRecords)
	}
	if list[0].N != MaxRecords+3 || list[len(list)-1].N != 4 {
		t.Errorf("kept numbers %d down to %d; want %d down to 4", list[0].N, list[len(list)-1].N, MaxRecords+3)
	}
	if _, err := Read(root, principal, "cutting-garden", "cg", 1); err == nil {
		t.Error("record 1 should have been removed")
	}
}

// Concurrent writers (the bridge and the host for one event) never take the
// same number and never lose a record.
func TestConcurrentWritersTakeDistinctNumbers(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	got := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, err := Write(root, newRecord(KindBridgeExit))
			if err != nil {
				t.Error(err)
				return
			}
			got <- rec.N
		}()
	}
	wg.Wait()
	close(got)
	seen := map[int]bool{}
	for n := range got {
		if seen[n] {
			t.Errorf("number %d taken twice", n)
		}
		seen[n] = true
	}
	if len(seen) != 8 {
		t.Errorf("%d distinct numbers, want 8", len(seen))
	}
}

func TestWriteRefusesWhatItCannotTrust(t *testing.T) {
	if _, err := Write("", newRecord(KindSignal)); err == nil {
		t.Error("no root: want an error")
	}
	bad := newRecord(KindSignal)
	bad.Server = ""
	if _, err := Write(t.TempDir(), bad); err == nil {
		t.Error("no server: want an error")
	}
	// A root that is a symlink is never written through.
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "disconnects")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(link, newRecord(KindSignal)); err == nil {
		t.Error("symlinked root: want an error")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("a symlinked root was written through: %v", entries)
	}
}

func TestSegmentNeverEscapesOrNamesADotDir(t *testing.T) {
	for in, want := range map[string]string{
		"cutting-garden": "cutting-garden",
		"a/b":            "a_b",
		"..":             "_..",
		".":              "_.",
		"":               "_",
		"clown builtin":  "clown_builtin",
		".hidden":        "_.hidden",
	} {
		if got := Segment(in); got != want {
			t.Errorf("Segment(%q) = %q, want %q", in, got, want)
		}
	}
	root := t.TempDir()
	rec := newRecord(KindSignal)
	rec.Plugin, rec.Server = "../../etc", "x/y"
	got, err := Write(root, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.URI, URIScheme+principal+"/plugins/_.._.._etc/x_y/") {
		t.Errorf("uri = %q", got.URI)
	}
	if _, err := os.Stat(filepath.Join(root, SessionDir(principal), "_.._.._etc", "x_y", "1.json")); err != nil {
		t.Errorf("the record is not under the escaped segments: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(root), "etc*"))
	if len(matches) != 0 {
		t.Errorf("a record escaped the root: %v", matches)
	}
}

func TestParseURI(t *testing.T) {
	cases := []struct {
		uri  string
		want Ref
		bad  bool
	}{
		{uri: AllURI("p1"), want: Ref{Kind: RefAll, Principal: "p1"}},
		{uri: ListURI("p1", "cg", "srv"), want: Ref{Kind: RefList, Principal: "p1", Plugin: "cg", Server: "srv"}},
		{uri: URI("p1", "cg", "srv", 12), want: Ref{Kind: RefOne, Principal: "p1", Plugin: "cg", Server: "srv", N: 12}},
		{uri: "clown://claude/p1", bad: true},
		{uri: "file:///etc/passwd", bad: true},
		{uri: "clown://session/p1/plugins/cg/srv/disconnects/0", bad: true},
		{uri: "clown://session/p1/plugins/cg/srv/disconnects/x", bad: true},
		{uri: "clown://session/p1/plugins/cg/srv/other", bad: true},
		{uri: "clown://session/p1/plugins/cg/srv/disconnects/1/2", bad: true},
		{uri: "clown://session//plugins/disconnects", bad: true},
		{uri: "clown://session/p1/other/disconnects", bad: true},
	}
	for _, c := range cases {
		got, err := ParseURI(c.uri)
		if c.bad {
			if err == nil {
				t.Errorf("ParseURI(%q) = %+v; want an error", c.uri, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseURI(%q) = %+v, %v; want %+v", c.uri, got, err, c.want)
		}
	}
}

func TestSweepRemovesOnlySessionsOlderThanRetention(t *testing.T) {
	root := t.TempDir()
	old, fresh := newRecord(KindSignal), newRecord(KindSignal)
	old.Session, fresh.Session = "old-session", "fresh-session"
	for _, r := range []Record{old, fresh} {
		if _, err := Write(root, r); err != nil {
			t.Fatal(err)
		}
	}
	oldFile := filepath.Join(root, SessionDir("old-session"), "cutting-garden", "cg", "1.json")
	past := time.Now().Add(-Retention - time.Hour)
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatal(err)
	}
	if n := Sweep(root, time.Now(), Retention); n != 1 {
		t.Fatalf("swept %d sessions, want 1", n)
	}
	if ListAll(root, "old-session") != nil {
		t.Error("the old session's records should be gone")
	}
	if len(ListAll(root, "fresh-session")) != 1 {
		t.Error("the fresh session's records must stay")
	}
	if err := RemoveSession(root, "fresh-session"); err != nil || ListAll(root, "fresh-session") != nil {
		t.Errorf("RemoveSession: %v", err)
	}
}

func TestScrub(t *testing.T) {
	cases := map[string]struct{ in, mustNot, must string }{
		"key value":   {"starting with TOKEN=abc123def and more", "abc123def", "TOKEN=<redacted> and more"},
		"lower key":   {"api_key=sk_live_notreal password=hunter2", "hunter2", "password=<redacted>"},
		"bearer":      {"Authorization: Bearer eyJhbGciOi.payload.sig trailing", "eyJhbGciOi", "Bearer <redacted> trailing"},
		"prefixed":    {"saw ghp_abcdefghij1234567890 in the env", "ghp_abcdefghij", "saw <redacted> in the env"},
		"aws":         {"id AKIAABCDEFGHIJKLMNOP", "AKIAABCDEFGH", "id <redacted>"},
		"hex":         {"digest 0123456789abcdef0123456789abcdef0123 end", "0123456789abcdef0123456789abcdef", "digest <redacted> end"},
		"base64":      {"blob aGVsbG8gd29ybGQgdGhpcyBpcyBhIGxvbmcgYmFzZTY0IHN0cmluZw== end", "aGVsbG8gd29ybGQ", "blob <redacted> end"},
		"escape":      {"red \x1b[31mtext\x1b[0m", "\x1b", "red"},
		"zero width":  {"a​b", "​", "ab"},
		"plain words": {"the server started on port 8080 and read 12 files", "<redacted>", "the server started on port 8080 and read 12 files"},
		"long path":   {"/nix/store/aaaaaaaa-some-package-1.2.3/bin/tool failed", "<redacted>", "/nix/store/aaaaaaaa-some-package-1.2.3/bin/tool failed"},
	}
	for name, c := range cases {
		got := Scrub(c.in)
		if c.mustNot != "" && strings.Contains(got, c.mustNot) {
			t.Errorf("%s: %q still holds %q", name, got, c.mustNot)
		}
		if !strings.Contains(got, c.must) {
			t.Errorf("%s: %q lacks %q", name, got, c.must)
		}
	}
	long := Scrub(strings.Repeat("é", 400))
	if len(long) > StderrLineBytes || !strings.HasSuffix(long, "é") {
		t.Errorf("a long multibyte line is cut on a character boundary: %d bytes, %q", len(long), long[len(long)-4:])
	}
}

func TestRingKeepsTheLastLinesScrubbed(t *testing.T) {
	r := NewRing()
	for i := 0; i < StderrTailLines+20; i++ {
		r.Add("line " + string(rune('A'+i%26)) + " TOKEN=secretvalue")
	}
	got := r.Snapshot()
	if len(got) != StderrTailLines {
		t.Fatalf("kept %d lines, want %d", len(got), StderrTailLines)
	}
	for _, l := range got {
		if strings.Contains(l.Line, "secretvalue") || l.At == "" {
			t.Fatalf("line %+v kept an unscrubbed value or has no time", l)
		}
	}
	var nilRing *Ring
	nilRing.Add("ignored")
	if s := nilRing.Snapshot(); s == nil || len(s) != 0 {
		t.Errorf("a nil ring snapshots as an empty list, got %#v", s)
	}
}

func TestSummaryNamesTheKindTheExitAndTheURI(t *testing.T) {
	rec := newRecord(KindChildExit)
	rec.ChildExit = &Exit{Code: -1, Signal: "killed"}
	rec.URI = URI(principal, "cutting-garden", "cg", 3)
	s := Summary(rec)
	for _, want := range []string{"cutting-garden/cg", "child_exit", "child exit -1 by killed", rec.URI, "clown-builtin-diagnostics/disconnects"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q lacks %q", s, want)
		}
	}
}
