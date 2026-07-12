package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// checklistRow is one row of the --cheap-context picker, at any depth: a
// whole-server row (Depth 0), a moxin/group row nested under it (Depth 1),
// or an individual-tool row nested under a group (Depth 2). IsParent marks
// a row that owns children (server and group rows); ParentKey is the
// immediate parent's Key, empty at the root. Checked is the row's own live
// checkbox state, mutated in place by checklistDelegate.Update — this is
// the state a huh.MultiSelect can't expose a hook to mutate, which is why
// this picker is a bare bubbles/list program instead of a huh.Form.
type checklistRow struct {
	Key       string // globally unique; see rowKey
	Label     string
	Checked   bool
	IsParent  bool
	ParentKey string // empty at the root (a server row, or an ungrouped flat row)
	Depth     int    // 0 = server, 1 = group, 2 = individual tool
}

func (r checklistRow) FilterValue() string { return r.Label }

// checklistDelegate renders one row per line (checkbox + label, indented by
// Depth) and intercepts the toggle key itself — the actual cascade lives
// here, not in bubbles/list's own Update, since list has no notion of
// parent/child rows.
type checklistDelegate struct{}

func (checklistDelegate) Height() int  { return 1 }
func (checklistDelegate) Spacing() int { return 0 }

var (
	checklistNormalStyle   = lipgloss.NewStyle()
	checklistSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
)

func (checklistDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	row, ok := item.(checklistRow)
	if !ok {
		return
	}
	box := "[ ]"
	if row.Checked {
		box = "[x]"
	}
	indent := strings.Repeat("    ", row.Depth)
	line := fmt.Sprintf("%s%s %s", indent, box, row.Label)
	if index == m.Index() {
		line = checklistSelectedStyle.Render("> " + line)
	} else {
		line = checklistNormalStyle.Render("  " + line)
	}
	fmt.Fprint(w, line)
}

// toggleKeys are the keys that flip a row's checkbox — huh's MultiSelect
// convention (space or x), kept for muscle-memory consistency with the
// rest of clown's huh-based pickers.
func isToggleKey(s string) bool {
	return s == " " || s == "x"
}

func (checklistDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !isToggleKey(keyMsg.String()) {
		return nil
	}
	index := m.GlobalIndex()
	items := m.Items()
	if index < 0 || index >= len(items) {
		return nil
	}
	row, ok := items[index].(checklistRow)
	if !ok {
		return nil
	}
	row.Checked = !row.Checked
	_ = m.SetItem(index, row)

	if row.IsParent {
		cascadeChecklistState(m, items, row.Key, row.Checked)
	}
	return nil
}

// cascadeChecklistState recursively flips every descendant of parentKey to
// checked, live, so toggling a server or an intermediate group cascades all
// the way down to individual tools (not just its immediate children) —
// e.g. unchecking "moxy" flips every moxin group AND every tool under
// every group, in one keypress.
func cascadeChecklistState(m *list.Model, items []list.Item, parentKey string, checked bool) {
	for i, it := range items {
		child, ok := it.(checklistRow)
		if !ok || child.ParentKey != parentKey {
			continue
		}
		child.Checked = checked
		_ = m.SetItem(i, child)
		if child.IsParent {
			cascadeChecklistState(m, items, child.Key, checked)
		}
	}
}

// checklistModel is the bare bubbletea program wrapping list.Model for the
// --cheap-context picker — a bubbles/list program, not a huh.Form, because
// huh.MultiSelect exposes no hook to intercept a toggle and cascade it to
// other rows (confirmed: its Update mutates cursor/selection internally
// with no interception point). list.ItemDelegate.Update runs BEFORE the
// list consumes the keypress, which is exactly that hook.
type checklistModel struct {
	list      list.Model
	confirmed bool
	quit      bool
}

func (m checklistModel) Init() tea.Cmd { return nil }

func (m checklistModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			m.confirmed = true
			return m, tea.Quit
		case "q", "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width)
		m.list.SetHeight(msg.Height - 4)
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m checklistModel) View() string {
	var b strings.Builder
	b.WriteString(m.list.View())
	b.WriteString("\n(space/x toggle, enter confirm, q/esc cancel — toggling a server row also toggles its tools)\n")
	return b.String()
}

// runChecklistPicker drives the picker and returns the final Checked state
// of every row, keyed by checklistRow.Key. ok is false if the user
// cancelled (q/ctrl+c/esc), in which case the caller should treat the
// launch as if --cheap-context had not been passed at all — cancelling a
// destructive-feeling picker should never silently drop every server.
func runChecklistPicker(title string, rows []checklistRow) (checked map[string]bool, ok bool, err error) {
	items := make([]list.Item, len(rows))
	for i, r := range rows {
		items[i] = r
	}
	l := list.New(items, checklistDelegate{}, 78, 20)
	l.Title = title
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	// less-style paging, additive to bubbles/list's defaults
	// (left/h/pgup/b/u and right/l/pgdown/f/d) — SetKeys replaces the whole
	// key list, so read-then-append rather than overwrite the existing
	// bindings.
	l.KeyMap.PrevPage.SetKeys(append(l.KeyMap.PrevPage.Keys(), "ctrl+b")...)
	l.KeyMap.NextPage.SetKeys(append(l.KeyMap.NextPage.Keys(), "ctrl+f")...)

	m, err := tea.NewProgram(checklistModel{list: l}, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, false, err
	}
	cm := m.(checklistModel)
	if cm.quit {
		return nil, false, nil
	}

	checked = make(map[string]bool, len(rows))
	for _, it := range cm.list.Items() {
		row := it.(checklistRow)
		checked[row.Key] = row.Checked
	}
	return checked, true, nil
}
