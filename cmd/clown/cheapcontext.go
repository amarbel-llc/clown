package main

import (
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/clown/internal/pluginhost"
)

// selectServers renders a huh multi-select letting the user trim which
// discovered plugin MCP servers get started for this session
// (--cheap-context). All servers are pre-selected, so accepting the form
// unmodified reproduces the default "load everything" behavior. Requires an
// interactive TTY; returns an error otherwise (mirrors the TTY gate in
// profileAddInteractive/profileEditInteractive, cmd/clown/profileform.go).
func selectServers(discovered []pluginhost.DiscoveredServer) ([]pluginhost.DiscoveredServer, error) {
	if !pluginhost.IsInteractive() {
		return nil, fmt.Errorf("--cheap-context requires an interactive TTY")
	}
	if len(discovered) == 0 {
		return discovered, nil
	}

	byName := make(map[string]pluginhost.DiscoveredServer, len(discovered))
	options := make([]huh.Option[string], len(discovered))
	selected := make([]string, 0, len(discovered))
	for i, d := range discovered {
		name := d.Name()
		byName[name] = d
		options[i] = huh.NewOption(name, name).Selected(true)
		selected = append(selected, name)
	}

	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Select MCP servers to load for this session").
			Description("Deselect a server to keep its tools out of the agent's context").
			Options(options...).
			Value(&selected),
	))
	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("cheap-context prompt: %w", err)
	}

	chosen := make(map[string]bool, len(selected))
	for _, name := range selected {
		chosen[name] = true
	}

	// Preserve discovery order rather than the picker's (possibly
	// filtered/reordered) selection order.
	result := make([]pluginhost.DiscoveredServer, 0, len(selected))
	for _, d := range discovered {
		if chosen[d.Name()] {
			result = append(result, d)
		}
	}
	return result, nil
}
