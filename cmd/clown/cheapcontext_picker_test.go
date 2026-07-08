package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/list"
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
