package main

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

func newTestChecklist(rows []checklistRow) list.Model {
	items := make([]list.Item, len(rows))
	for i, r := range rows {
		items[i] = r
	}
	m := list.New(items, checklistDelegate{}, 78, 20)
	m.SetShowStatusBar(false)
	m.SetShowHelp(false)
	m.SetFilteringEnabled(false)
	return m
}

func toggleAt(t *testing.T, m *list.Model, index int) {
	t.Helper()
	// GlobalIndex() == Index() here since filtering is disabled and every
	// row fits on one page (list defaults to enough per-page room for 20
	// items given the height passed to list.New).
	for m.GlobalIndex() != index {
		if m.GlobalIndex() < index {
			m.CursorDown()
		} else {
			m.CursorUp()
		}
	}
	d := checklistDelegate{}
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}, m)
}

func rowsOf(m list.Model) []checklistRow {
	items := m.Items()
	rows := make([]checklistRow, len(items))
	for i, it := range items {
		rows[i] = it.(checklistRow)
	}
	return rows
}

func TestChecklistDelegate_ToggleParentCascadesChildren(t *testing.T) {
	m := newTestChecklist([]checklistRow{
		{Key: "moxy\x00all", Label: "moxy (2 tools)", Checked: true, IsParent: true},
		{Key: "moxy\x00folio.read", Label: "folio.read", Checked: true, ParentKey: "moxy\x00all"},
		{Key: "moxy\x00grit.status", Label: "grit.status", Checked: true, ParentKey: "moxy\x00all"},
	})

	toggleAt(t, &m, 0) // uncheck the parent row

	rows := rowsOf(m)
	if rows[0].Checked {
		t.Fatalf("parent row still checked after toggle")
	}
	for _, r := range rows[1:] {
		if r.Checked {
			t.Errorf("child %q still checked after parent toggled off, want cascaded off", r.Key)
		}
	}

	toggleAt(t, &m, 0) // re-check the parent row

	rows = rowsOf(m)
	if !rows[0].Checked {
		t.Fatalf("parent row still unchecked after second toggle")
	}
	for _, r := range rows[1:] {
		if !r.Checked {
			t.Errorf("child %q still unchecked after parent toggled on, want cascaded on", r.Key)
		}
	}
}

func TestChecklistDelegate_ToggleChildDoesNotAffectParentOrSiblings(t *testing.T) {
	m := newTestChecklist([]checklistRow{
		{Key: "moxy\x00all", Label: "moxy (2 tools)", Checked: true, IsParent: true},
		{Key: "moxy\x00folio.read", Label: "folio.read", Checked: true, ParentKey: "moxy\x00all"},
		{Key: "moxy\x00grit.status", Label: "grit.status", Checked: true, ParentKey: "moxy\x00all"},
	})

	toggleAt(t, &m, 1) // uncheck just folio.read

	rows := rowsOf(m)
	if !rows[0].Checked {
		t.Errorf("parent row should stay checked when only a child is toggled")
	}
	if rows[1].Checked {
		t.Errorf("toggled child should be unchecked")
	}
	if !rows[2].Checked {
		t.Errorf("untouched sibling should remain checked")
	}
}

func TestChecklistDelegate_TogglePlainRowDoesNotCascade(t *testing.T) {
	m := newTestChecklist([]checklistRow{
		{Key: "caldav\x00all", Label: "caldav", Checked: true},
	})

	toggleAt(t, &m, 0)

	rows := rowsOf(m)
	if rows[0].Checked {
		t.Errorf("plain row should be unchecked after toggle")
	}
}

// threeLevelRows builds a server -> group -> tool tree matching what
// selectServers now produces for a multi-group server (moxy): the server
// row (Depth 0), one group row per moxin (Depth 1), and individual tool
// rows under each group (Depth 2).
func threeLevelRows() []checklistRow {
	serverKey := "moxy\x00all"
	folioKey := "moxy\x00\x00group\x00folio"
	gritKey := "moxy\x00\x00group\x00grit"
	return []checklistRow{
		{Key: serverKey, Label: "moxy (3 tools)", Checked: true, IsParent: true, Depth: 0},
		{Key: folioKey, Label: "folio (2 tools)", Checked: true, IsParent: true, ParentKey: serverKey, Depth: 1},
		{Key: "moxy\x00folio.read", Label: "folio.read", Checked: true, ParentKey: folioKey, Depth: 2},
		{Key: "moxy\x00folio.glob", Label: "folio.glob", Checked: true, ParentKey: folioKey, Depth: 2},
		{Key: gritKey, Label: "grit (1 tools)", Checked: true, IsParent: true, ParentKey: serverKey, Depth: 1},
		{Key: "moxy\x00grit.status", Label: "grit.status", Checked: true, ParentKey: gritKey, Depth: 2},
	}
}

func TestChecklistDelegate_ToggleServerCascadesThroughGroupsToTools(t *testing.T) {
	m := newTestChecklist(threeLevelRows())

	toggleAt(t, &m, 0) // uncheck the server row

	for _, r := range rowsOf(m) {
		if r.Checked {
			t.Errorf("row %q still checked after server-level toggle off, want fully cascaded off", r.Key)
		}
	}

	toggleAt(t, &m, 0) // re-check the server row

	for _, r := range rowsOf(m) {
		if !r.Checked {
			t.Errorf("row %q still unchecked after server-level toggle on, want fully cascaded on", r.Key)
		}
	}
}

func TestChecklistDelegate_ToggleGroupCascadesToItsToolsOnly(t *testing.T) {
	m := newTestChecklist(threeLevelRows())

	toggleAt(t, &m, 1) // uncheck the "folio" group row

	rows := rowsOf(m)
	if !rows[0].Checked {
		t.Errorf("server row should stay checked when only one group is toggled")
	}
	if rows[1].Checked {
		t.Errorf("toggled group row should be unchecked")
	}
	if rows[2].Checked || rows[3].Checked {
		t.Errorf("folio's tools should cascade off with their group: got %+v, %+v", rows[2], rows[3])
	}
	if !rows[4].Checked {
		t.Errorf("untouched sibling group (grit) should remain checked")
	}
	if !rows[5].Checked {
		t.Errorf("grit's tool should be unaffected by folio's toggle")
	}
}
