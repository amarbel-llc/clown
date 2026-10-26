package main

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// pickerKeyIsOurs reports whether a filterable picker acts on key itself
// rather than leaving it to the list: never while a filter is being typed
// (q is text, enter applies the filter), and esc only with no filter applied
// (otherwise it clears the filter), as openRouterPickerModel does (clown#309).
func pickerKeyIsOurs(state list.FilterState, key string) bool {
	switch {
	case state == list.Filtering:
		return false
	case key == "esc":
		return state == list.Unfiltered
	}
	return true
}

// runAltScreenPicker runs a bubbletea picker model on the alternate screen
// and returns the model it ended with (clown#200). opts are added after
// tea.WithAltScreen (tests pass headless input and output). A final model of
// another type than m's is an error rather than a panic. What "cancelled"
// means stays with each picker's model (a quit flag, an empty choice), so
// callers read it from the returned model.
func runAltScreenPicker[M tea.Model](m M, opts ...tea.ProgramOption) (M, error) {
	var zero M
	res, err := tea.NewProgram(m, append([]tea.ProgramOption{tea.WithAltScreen()}, opts...)...).Run()
	if err != nil {
		return zero, err
	}
	final, ok := res.(M)
	if !ok {
		return zero, fmt.Errorf("picker ended with a %T, want a %T", res, zero)
	}
	return final, nil
}
