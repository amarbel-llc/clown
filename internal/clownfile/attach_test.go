package clownfile

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestAttachParseAndEnabled(t *testing.T) {
	home := t.TempDir()
	write(t, home, `
[attach]
multiplexer  = "zmx"
start        = ["zmx", "attach", "{id}", "{entry}"]
resume       = ["zmx", "attach", "{id}", "{entry}"]
resume-title = "{id}"
spawn        = ["zmx", "attach", "{id}", "--detach", "{entry}"]
spawn-entry  = ["clown", "--", "{prompt}"]
spawn-window = ["open-term", "{dir}", "{id}"]
`)
	cf, err := Discover(home, home, "")
	if err != nil {
		t.Fatal(err)
	}
	a := cf.Attach
	if !a.Enabled() {
		t.Fatal("multiplexer zmx must be Enabled()")
	}
	if a.Multiplexer != "zmx" || a.ResumeTitle != "{id}" {
		t.Fatalf("scalars wrong: %+v", a)
	}
	if !reflect.DeepEqual(a.Spawn, []string{"zmx", "attach", "{id}", "--detach", "{entry}"}) {
		t.Fatalf("spawn parsed wrong: %v", a.Spawn)
	}
	if !reflect.DeepEqual(a.SpawnEntry, []string{"clown", "--", "{prompt}"}) {
		t.Fatalf("spawn-entry parsed wrong: %v", a.SpawnEntry)
	}
	if !reflect.DeepEqual(a.SpawnWindow, []string{"open-term", "{dir}", "{id}"}) {
		t.Fatalf("spawn-window parsed wrong: %v", a.SpawnWindow)
	}
}

func TestAttachEnabledFalse(t *testing.T) {
	if (Attach{}).Enabled() {
		t.Error("absent [attach] must not be Enabled()")
	}
	if (Attach{Multiplexer: "none"}).Enabled() {
		t.Error(`multiplexer "none" must not be Enabled()`)
	}
}

// A deeper clownfile replaces an argv template wholesale (no element merge).
func TestAttachMergeDeeperWinsWholesale(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	write(t, home, "[attach]\nmultiplexer = \"zmx\"\nstart = [\"zmx\", \"attach\", \"{id}\", \"{entry}\"]\nresume-title = \"shallow\"\n")
	write(t, repo, "[attach]\nstart = [\"posh\", \"{id}\", \"{entry}\"]\n")

	cf, err := Discover(repo, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"posh", "{id}", "{entry}"}; !reflect.DeepEqual(cf.Attach.Start, want) {
		t.Fatalf("start = %v, want the deeper template wholesale %v", cf.Attach.Start, want)
	}
	// Untouched scalars inherit from the shallower file.
	if cf.Attach.Multiplexer != "zmx" || cf.Attach.ResumeTitle != "shallow" {
		t.Fatalf("inherited scalars wrong: %+v", cf.Attach)
	}
}

func TestAttachResolveStartSplicesEntryAndID(t *testing.T) {
	a := Attach{Multiplexer: "zmx", Start: []string{"zmx", "attach", "{id}", "{entry}"}}
	got, err := a.Resolve(ModeStart, "sess-1", []string{"clown", "--clown-attach-id", "sess-1", "--", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"zmx", "attach", "sess-1", "clown", "--clown-attach-id", "sess-1", "--", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve = %v, want %v", got, want)
	}
}

func TestAttachResolveResumeMode(t *testing.T) {
	a := Attach{Multiplexer: "zmx", Resume: []string{"zmx", "attach", "{id}", "{entry}"}}
	got, err := a.Resolve(ModeResume, "k", []string{"clown", "resume"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"zmx", "attach", "k", "clown", "resume"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve resume = %v, want %v", got, want)
	}
}

func TestAttachResolveRejectsUnknownPlaceholder(t *testing.T) {
	// {prompt} is not available in the interactive Start mode -> rejected.
	a := Attach{Multiplexer: "zmx", Start: []string{"zmx", "{prompt}", "{entry}"}}
	if _, err := a.Resolve(ModeStart, "k", []string{"clown"}); err == nil {
		t.Fatal("Resolve must reject a surviving {prompt} placeholder in start mode")
	}
}

func TestAttachResolveRejectsDisabledAndEmpty(t *testing.T) {
	if _, err := (Attach{Multiplexer: "none", Start: []string{"x"}}).Resolve(ModeStart, "k", nil); err == nil {
		t.Fatal("Resolve must error when multiplexer is none")
	}
	if _, err := (Attach{Multiplexer: "zmx"}).Resolve(ModeStart, "k", nil); err == nil {
		t.Fatal("Resolve must error on an empty template")
	}
	if _, err := (Attach{Multiplexer: "screen", Start: []string{"x"}}).Resolve(ModeStart, "k", nil); err == nil {
		t.Fatal("Resolve must reject an unknown multiplexer")
	}
}

func TestAttachTitleSubstitutesID(t *testing.T) {
	if got := (Attach{ResumeTitle: "clown {id}"}).Title("abc"); got != "clown abc" {
		t.Fatalf("Title = %q, want \"clown abc\"", got)
	}
	if got := (Attach{}).Title("abc"); got != "" {
		t.Fatalf("empty ResumeTitle must yield empty Title, got %q", got)
	}
}
