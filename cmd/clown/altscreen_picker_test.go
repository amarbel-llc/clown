package main

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// clown#200: the four bubbletea pickers share one run-and-unwrap tail.

// quitAtOnce is a picker that ends as soon as it starts, carrying a marker so
// the test can tell the returned model is the final one.
type quitAtOnce struct{ marker string }

func (m quitAtOnce) Init() tea.Cmd { return tea.Quit }
func (m quitAtOnce) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}
func (m quitAtOnce) View() string { return "" }

// becomesOther hands bubbletea a model of another type, which a hand-written
// `res.(T)` would panic on.
type becomesOther struct{}

func (becomesOther) Init() tea.Cmd { return func() tea.Msg { return struct{}{} } }
func (becomesOther) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return quitAtOnce{marker: "other"}, tea.Quit
}
func (becomesOther) View() string { return "" }

var headlessPicker = []tea.ProgramOption{tea.WithInput(nil), tea.WithOutput(io.Discard)}

func TestRunAltScreenPickerReturnsTheFinalModel(t *testing.T) {
	got, err := runAltScreenPicker(quitAtOnce{marker: "final"}, headlessPicker...)
	if err != nil {
		t.Fatal(err)
	}
	if got.marker != "final" {
		t.Fatalf("model = %+v, want the final quitAtOnce", got)
	}
}

// neverQuits waits for input it never gets.
type neverQuits struct{ marker string }

func (neverQuits) Init() tea.Cmd                         { return nil }
func (m neverQuits) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (neverQuits) View() string                          { return "" }

// A program that fails to run (here: its context is already cancelled)
// returns the error and the zero model, never a half-run one.
func TestRunAltScreenPickerReturnsRunErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := runAltScreenPicker(neverQuits{marker: "started"}, append(headlessPicker, tea.WithContext(ctx))...)
	if err == nil {
		t.Fatal("want the run error of a cancelled program, got nil")
	}
	if got.marker != "" {
		t.Fatalf("model = %+v, want the zero model with the error", got)
	}
}

func TestRunAltScreenPickerRefusesAModelOfAnotherType(t *testing.T) {
	_, err := runAltScreenPicker(becomesOther{}, headlessPicker...)
	if err == nil || !strings.Contains(err.Error(), "quitAtOnce") {
		t.Fatalf("err = %v, want an error naming the unexpected model type", err)
	}
}
