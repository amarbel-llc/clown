package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

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
