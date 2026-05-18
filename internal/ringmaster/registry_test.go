package ringmaster

import (
	"testing"
	"time"
)

func TestRegistry_AddAndGet(t *testing.T) {
	r := NewRegistry()
	in := Instance{Alias: "a", Model: "m", Port: 1, PID: 2, StartedAt: time.Now()}
	if err := r.Add(in); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("a")
	if !ok || got.Alias != "a" {
		t.Errorf("got=%+v ok=%v", got, ok)
	}
}

func TestRegistry_DuplicateAlias(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Instance{Alias: "a"})
	err := r.Add(Instance{Alias: "a"})
	if err == nil {
		t.Fatal("expected duplicate-alias error")
	}
}

func TestRegistry_RemoveAndList(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Instance{Alias: "a", Port: 1})
	_ = r.Add(Instance{Alias: "b", Port: 2})
	if got := len(r.List()); got != 2 {
		t.Errorf("len=%d", got)
	}
	r.Remove("a")
	if got := len(r.List()); got != 1 {
		t.Errorf("after remove len=%d", got)
	}
}

func TestRegistry_ListSorted(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Instance{Alias: "c"})
	_ = r.Add(Instance{Alias: "a"})
	_ = r.Add(Instance{Alias: "b"})
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Alias != "a" || got[1].Alias != "b" || got[2].Alias != "c" {
		t.Errorf("not sorted: %+v", got)
	}
}
