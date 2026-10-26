package main

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/sessions"
)

// clown#309: while a filter is being typed, q is filter text, enter applies
// the filter and esc clears it; only outside filtering do they quit or
// select. ctrl+c always quits.

var (
	keySlash = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	keyQ     = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
)

// filterPicker is the shape both pickers share for this test.
type filterPicker interface {
	tea.Model
	filterState() list.FilterState
	done() (quit, chose bool)
}

func (m pickerModel) filterState() list.FilterState {
	return m.list.FilterState()
}

func (m pickerModel) done() (bool, bool) {
	return m.quit, m.chosen != nil || m.addRequested
}

func (m sessionPickerModel) filterState() list.FilterState {
	return m.list.FilterState()
}

func (m sessionPickerModel) done() (bool, bool) {
	return m.quit, m.chosen != nil
}

func press(t *testing.T, m filterPicker, k tea.KeyMsg) (filterPicker, bool) {
	t.Helper()
	next, cmd := m.Update(k)
	quitting := false
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			quitting = true
		}
	}
	return next.(filterPicker), quitting
}

func TestPickersLetTheFilterHaveQEnterAndEsc(t *testing.T) {
	newProfilePicker := func() filterPicker {
		l := list.New(pickerItems([]profile.Profile{{Name: "quux", Display: "Quux"}}), list.NewDefaultDelegate(), 40, 14)
		l.SetFilteringEnabled(true)
		return pickerModel{list: l}
	}
	newSessionPicker := func() filterPicker {
		l := list.New([]list.Item{sessionItem{sessions.Session{ID: "11111111-2222-3333-4444-555555555555", Title: "quux"}}}, list.NewDefaultDelegate(), 60, 16)
		l.SetFilteringEnabled(true)
		return sessionPickerModel{list: l}
	}
	for name, newPicker := range map[string]func() filterPicker{"profile": newProfilePicker, "session": newSessionPicker} {
		t.Run(name, func(t *testing.T) {
			m := newPicker()
			m, _ = press(t, m, keySlash)
			if m.filterState() != list.Filtering {
				t.Fatalf("after /: filter state %v, want Filtering", m.filterState())
			}
			m, quitting := press(t, m, keyQ)
			if q, c := m.done(); q || c || quitting {
				t.Fatal("q while filtering ended the picker")
			}
			m, quitting = press(t, m, keyEnter)
			if q, c := m.done(); q || c || quitting {
				t.Fatal("enter while filtering ended the picker instead of applying the filter")
			}
			if m.filterState() != list.FilterApplied {
				t.Fatalf("after enter: filter state %v, want FilterApplied", m.filterState())
			}
			m, quitting = press(t, m, keyEsc)
			if q, _ := m.done(); q || quitting {
				t.Fatal("esc with a filter applied quit instead of clearing the filter")
			}
			if m.filterState() != list.Unfiltered {
				t.Fatalf("after esc: filter state %v, want Unfiltered", m.filterState())
			}
			m, quitting = press(t, m, keyEsc)
			if q, _ := m.done(); !q || !quitting {
				t.Fatal("esc with no filter did not quit")
			}
			m = newPicker()
			m, _ = press(t, m, keySlash)
			if m, quitting = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlC}); !quitting {
				t.Fatal("ctrl+c while filtering did not quit")
			}
			m = newPicker()
			if m, quitting = press(t, m, keyQ); !quitting {
				t.Fatal("q outside filtering did not quit")
			}
			m = newPicker()
			if m, quitting = press(t, m, keyEnter); !quitting {
				t.Fatal("enter outside filtering did not select")
			}
			if _, c := m.done(); !c {
				t.Fatal("enter outside filtering chose nothing")
			}
		})
	}
}
