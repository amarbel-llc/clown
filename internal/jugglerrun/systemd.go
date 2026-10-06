package jugglerrun

import (
	"context"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TransientUnit is one agent's systemd transient service (FDR 0019 §9).
type TransientUnit struct {
	Name string
	// RuntimeMax is the RuntimeMaxSec backstop: the brief's wall clock plus a
	// grace, so the loop's own budget expires first.
	RuntimeMax   time.Duration
	ExecStopPost []string
	// Env is the unit's complete environment (each entry one --setenv).
	Env  map[string]string
	Exec []string
}

// UnitLauncher starts a transient unit, failing synchronously when systemd
// refuses it.
type UnitLauncher interface {
	Launch(ctx context.Context, u TransientUnit) error
}

// SystemdRunArgv is the systemd-run argv for u (systemd-run(1),
// systemd.service(5)): Type=exec so an exec failure is reported
// synchronously, --collect so a failed unit does not linger and block a
// retry under the same name, Delegate=yes so the runtime scope may later
// carve out the agent scope (§9 leaves its realisation open; nothing here
// builds it), and ExecStopPost as the sole exit-wake emitter (§6).
func SystemdRunArgv(bin string, userManager bool, u TransientUnit) []string {
	argv := []string{bin}
	if userManager {
		argv = append(argv, "--user")
	}
	argv = append(argv,
		"--unit", u.Name,
		"--collect",
		"--property=Type=exec",
		"--property=RuntimeMaxSec="+strconv.Itoa(int(math.Ceil(u.RuntimeMax.Seconds()))),
		"--property=Delegate=yes",
		"--property=ExecStopPost="+SystemdCommandLine(u.ExecStopPost),
	)
	keys := make([]string, 0, len(u.Env))
	for k := range u.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		argv = append(argv, "--setenv="+k+"="+u.Env[k])
	}
	return append(argv, u.Exec...)
}

// SystemdCommandLine renders argv as a systemd unit command line: specifier
// (%) and variable ($) characters are doubled, and an argument holding
// whitespace or quotes is double-quoted with C-style escapes.
func SystemdCommandLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		a = strings.ReplaceAll(a, "%", "%%")
		a = strings.ReplaceAll(a, "$", "$$")
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\") {
			a = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(a) + `"`
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}

// ExecSystemdRun launches units with the systemd-run binary. The client
// process runs with the spawner's environment minus its principal
// (StripPrincipalEnv); the unit sees only TransientUnit.Env.
type ExecSystemdRun struct {
	Bin         string
	UserManager bool
	Runner      CommandRunner
}

func (s ExecSystemdRun) Launch(ctx context.Context, u TransientUnit) error {
	runner := s.Runner
	if runner == nil {
		runner = OSCommandRunner{}
	}
	_, err := runner.Run(ctx, Command{Argv: SystemdRunArgv(s.Bin, s.UserManager, u), Env: StripPrincipalEnv(os.Environ())})
	return err
}
