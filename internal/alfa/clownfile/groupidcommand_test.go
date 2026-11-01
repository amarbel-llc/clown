package clownfile

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveGroupIDCommand_SubstitutesCwdAndSessionID(t *testing.T) {
	a := Attach{GroupIDCommand: []string{"sc", "key", "--cwd", "{cwd}", "--id={session-id}"}}
	got, err := a.ResolveGroupIDCommand("/repo", "uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sc", "key", "--cwd", "/repo", "--id=uuid-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveGroupIDCommand_UnsetOrNoSessionIDSkips(t *testing.T) {
	if got, err := (Attach{}).ResolveGroupIDCommand("/repo", "uuid-1"); got != nil || err != nil {
		t.Fatalf("unset command: got %q, %v; want nil, nil", got, err)
	}
	// An unknown provider session id (claude --continue / --print) cannot key
	// an implicit session, so the command is not run at all.
	a := Attach{GroupIDCommand: []string{"sc", "{session-id}"}}
	if got, err := a.ResolveGroupIDCommand("/repo", ""); got != nil || err != nil {
		t.Fatalf("empty session id: got %q, %v; want nil, nil", got, err)
	}
}

func TestResolveGroupIDCommand_RejectsUnavailablePlaceholder(t *testing.T) {
	a := Attach{GroupIDCommand: []string{"sc", "{id}"}}
	_, err := a.ResolveGroupIDCommand("/repo", "uuid-1")
	if err == nil || !strings.Contains(err.Error(), "{id}") {
		t.Fatalf("want an error naming {id}, got %v", err)
	}
}

func TestMergeGroupIDCommand_DeeperEmptyListDisables(t *testing.T) {
	dst := Clownfile{Attach: Attach{GroupIDCommand: []string{"sc", "{session-id}"}}}
	mergeInto(&dst, Clownfile{Attach: Attach{GroupIDCommand: []string{}}})
	if len(dst.Attach.GroupIDCommand) != 0 {
		t.Fatalf("an explicit empty group-id-command must override the default, got %q", dst.Attach.GroupIDCommand)
	}
}
