package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/clown/internal/circusmodels"
)

// pickCircusModel resolves the circus model when the user has not
// passed --model. It lists `~/.local/share/circus/models/*.gguf`
// and either returns one name or prints a diagnostic and returns
// a non-zero exit code:
//
//   - models dir empty / missing: hint the user to run `circus download`
//   - non-interactive stdin/stdout: refuse, ask for an explicit --model
//   - exactly one model: pick it without prompting
//   - 2+ models: render a huh.NewSelect picker
//
// On user abort (Ctrl+C / Esc) the returned exit code is 130.
func pickCircusModel() (string, int) {
	dir := circusmodels.Dir()
	names, err := circusmodels.List(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: listing circus models in %s: %v\n", dir, err)
		return "", 1
	}
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr,
			"clown: no circus models found in %s.\n"+
				"       Run `circus download <name>` first, or pass --model <path>.\n",
			dir)
		return "", 1
	}
	if len(names) == 1 {
		return names[0], 0
	}
	if !isInteractiveTerminal() {
		fmt.Fprintf(os.Stderr,
			"clown: %d circus models available in %s but stdin/stdout is not a terminal;\n"+
				"       pass --model <name> to pick one non-interactively.\n",
			len(names), dir)
		return "", 1
	}

	selected := names[0]
	options := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		options = append(options, huh.NewOption(n, n))
	}

	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"))

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Pick a circus model").
				Description(fmt.Sprintf("From %s. Pass --model to skip this prompt.", dir)).
				Options(options...).
				Value(&selected),
		),
	).WithKeyMap(km).WithShowHelp(false)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", 130
		}
		fmt.Fprintf(os.Stderr, "clown: model picker: %v\n", err)
		return "", 1
	}
	return selected, 0
}
