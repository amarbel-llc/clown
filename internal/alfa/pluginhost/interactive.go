package pluginhost

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

// IsInteractive reports whether the host is running with a user on the
// other end. Both stdin and stderr must be terminals: stdin is where huh
// reads keystrokes, and stderr is where huh renders its UI (leaving stdout
// clean for downstream pipelines).
func IsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) &&
		term.IsTerminal(int(os.Stderr.Fd()))
}

// ConfirmContinueWithFailures renders a huh confirmation listing the
// failed servers and returns the user's decision.
//
// Returns (true, nil) when the user chose to continue with the healthy
// subset, (false, nil) when they chose to abort, and (_, err) if huh
// itself failed (e.g. ctrl-c, broken terminal). Callers should check
// IsInteractive() before invoking this.
func ConfirmContinueWithFailures(failures []StartFailure) (bool, error) {
	if len(failures) == 0 {
		return true, nil
	}

	var desc strings.Builder
	for _, f := range failures {
		fmt.Fprintf(&desc, "  • %s: %v\n", f.Server.Name(), f.Err)
	}

	var cont bool
	form := huh.NewConfirm().
		Title(fmt.Sprintf("%d plugin server(s) failed to start.", len(failures))).
		Description(desc.String()).
		Affirmative("Continue with healthy servers").
		Negative("Abort").
		Value(&cont)

	if err := form.Run(); err != nil {
		return false, err
	}
	return cont, nil
}
