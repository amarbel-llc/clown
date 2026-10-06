package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglerbrief"
	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const runUsage = "usage: juggler run --brief <file|-> [--job <id>] [--brief-stanza <id>] [--moxy <path> | --moxy-url <http url>] [--ringmaster <path>] [--troupe <path>]"

// platformBins are the consumed binaries' paths: flag, then
// JUGGLER_{RINGMASTER,TROUPE,SYSTEMD_RUN}_BIN, then the bare name on PATH.
// userManager selects `systemd-run --user` (spawn's --user).
type platformBins struct {
	ringmaster, troupe, systemdRun string
	userManager                    bool
}

func (p *platformBins) register(fs *flag.FlagSet, withSystemdRun bool) {
	fs.StringVar(&p.ringmaster, "ringmaster", "", "ringmaster binary (default $"+jr.RingmasterBinEnv+" or ringmaster)")
	p.registerTroupe(fs)
	if withSystemdRun {
		fs.StringVar(&p.systemdRun, "systemd-run", "", "systemd-run binary (default $"+jr.SystemdRunBinEnv+" or systemd-run)")
	}
}

func (p *platformBins) registerTroupe(fs *flag.FlagSet) {
	fs.StringVar(&p.troupe, "troupe", "", "troupe binary (default $"+jr.TroupeBinEnv+" or troupe)")
}

// resolveBin is jr.ResolveBinary with the build-time path as the default
// ahead of the bare name: flag, then env, then burned-in, then PATH.
func resolveBin(flagValue, envVar, burnedIn, bareName string) string {
	if burnedIn != "" {
		bareName = burnedIn
	}
	return jr.ResolveBinary(flagValue, envVar, bareName)
}

func (p platformBins) ringmasterClient() jr.ExecRingmaster {
	return jr.ExecRingmaster{Bin: resolveBin(p.ringmaster, jr.RingmasterBinEnv, RingmasterPath, "ringmaster")}
}

func (p platformBins) troupeClient() jr.ExecTroupe {
	return jr.ExecTroupe{Bin: resolveBin(p.troupe, jr.TroupeBinEnv, TroupePath, "troupe")}
}

// lifecycleDeps wires the exec-backed collaborators and the default store.
func (p platformBins) lifecycleDeps() (jr.LifecycleDeps, error) {
	store, err := jr.DefaultStore()
	if err != nil {
		return jr.LifecycleDeps{}, err
	}
	troupe := p.troupeClient()
	return jr.LifecycleDeps{
		Ringmaster: p.ringmasterClient(),
		Troupe:     troupe,
		Rooms:      jr.ExecRoomProvisioner(troupe),
		Units:      jr.ExecSystemdRun{Bin: resolveBin(p.systemdRun, jr.SystemdRunBinEnv, SystemdRunPath, "systemd-run"), UserManager: p.userManager},
		Store:      store,
	}, nil
}

// fail reports err as `juggler: <verb>: <err>` and returns the usage/config
// exit code (1).
func fail(stderr io.Writer, verb string, err error) int {
	fmt.Fprintf(stderr, "juggler: %s: %v\n", verb, err)
	return jr.ExitUsage
}

// withDeps wires bins' lifecycle collaborators and runs fn under a context
// derived from parent, bounded by timeout when it is positive. A wiring
// failure is reported as verb's and exits 1.
func withDeps(parent context.Context, stderr io.Writer, verb string, bins platformBins, timeout time.Duration, fn func(context.Context, jr.LifecycleDeps) int) int {
	deps, err := bins.lifecycleDeps()
	if err != nil {
		return fail(stderr, verb, err)
	}
	ctx, cancel := parent, context.CancelFunc(func() {})
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	defer cancel()
	return fn(ctx, deps)
}

// readInput reads path, or stdin for "-".
func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

// errExtraPositional is leadingJob's "more than one <job>" error.
var errExtraPositional = errors.New("unexpected argument")

// leadingJob parses args with fs, taking the job id from a leading
// positional or the sole trailing one, so `juggler resolve <job> --state …`
// and `--state … <job>` both parse. The job is "" when none was given; a
// second positional is an error wrapping errExtraPositional, and a flag
// error is fs.Parse's (already printed by fs).
func leadingJob(fs *flag.FlagSet, args []string) (string, error) {
	var job string
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		job, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	switch {
	case job == "" && fs.NArg() == 1:
		return fs.Arg(0), nil
	case fs.NArg() != 0:
		return "", fmt.Errorf("%w %q", errExtraPositional, fs.Arg(0))
	}
	return job, nil
}

func printJSON(w io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(w, "{\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintln(w, string(b))
}

// resolveAgentModel resolves a registry name daemon-free when it is a
// remote entry (FDR 0019 §10) and through the juggler daemon otherwise
// (local models, whose llama-server the daemon owns).
func resolveAgentModel(ctx context.Context, name string) (rm.ResolveModelResult, error) {
	res, err := rm.ResolveRemoteModelFromFile(name)
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, rm.ErrDaemonRequired) {
		return rm.ResolveModelResult{}, err
	}
	cli, dialErr := dialClient()
	if dialErr != nil {
		return rm.ResolveModelResult{}, fmt.Errorf("%w (daemon unreachable: %v)", err, dialErr)
	}
	defer cli.Close()
	return cli.ResolveModel(ctx, rm.ResolveModelParams{Name: name})
}

// runOutcomeJSON is `juggler run`'s stdout object.
type runOutcomeJSON struct {
	Job     string `json:"job"`
	State   string `json:"state"`
	Message string `json:"message"`
	Ledger  string `json:"ledger"`
}

// cmdRun is `juggler run`, the agent (FDR 0019 §1, §6). ctx is cancelled on
// SIGTERM/SIGINT (a holder's `systemctl stop`), which terminalizes the job
// aborted; a holder's job_cancel is observed on the journal to the same
// effect. The exit code mirrors the terminal state: 0 succeeded, 2 failed,
// 3 aborted, 4 interrupted; 1 is a usage or identity error with no job
// written.
func cmdRun(ctx context.Context, resolve func(context.Context, string) (rm.ResolveModelResult, error), httpClient *http.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var (
		bins                            platformBins
		briefPath, job, stanza, moxyURL string
		moxyBin                         string
	)
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&briefPath, "brief", "", "the brief (TOML) file, or - for stdin")
	fs.StringVar(&job, "job", "", "adopt this pre-started ringmaster job (juggler spawn passes it)")
	fs.StringVar(&stanza, "brief-stanza", "", "the brief's stanza id in the room (the first turn's provenance parent)")
	fs.StringVar(&moxyURL, "moxy-url", "", "use this running MCP upstream instead of launching moxy (default $"+jr.MoxyURLEnv+"; tests)")
	fs.StringVar(&moxyBin, "moxy", "", "moxy binary launched from the brief's moxyfile (default $"+jr.MoxyBinEnv+" or moxy)")
	bins.register(fs, false)
	if err := fs.Parse(args); err != nil {
		return jr.ExitUsage
	}
	if briefPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "juggler: run: --brief is required\n"+runUsage)
		return jr.ExitUsage
	}
	data, err := readInput(briefPath, stdin)
	if err != nil {
		return fail(stderr, "run", fmt.Errorf("reading brief: %w", err))
	}
	brief, err := jugglerbrief.Parse(data)
	if err != nil {
		return fail(stderr, "run", err)
	}
	if moxyURL == "" {
		moxyURL = os.Getenv(jr.MoxyURLEnv)
	}
	return withDeps(ctx, stderr, "run", bins, 0, func(ctx context.Context, deps jr.LifecycleDeps) int {
		out, err := jr.RunAgent(ctx, jr.AgentDeps{
			Ringmaster:    deps.Ringmaster,
			Troupe:        deps.Troupe,
			ResolveModel:  resolve,
			HTTPClient:    httpClient,
			Stderr:        stderr,
			Moxy:          jr.ExecMoxy{Bin: resolveBin(moxyBin, jr.MoxyBinEnv, MoxyPath, "moxy"), Stderr: stderr},
			AgentStateDir: filepath.Join(deps.Store.Root, "agents", brief.Principal),
		}, jr.AgentRequest{
			Brief:        brief,
			Job:          job,
			BriefStanza:  stanza,
			MoxyURL:      moxyURL,
			EnvPrincipal: os.Getenv(jr.SessionIDEnv),
			WatchCancel:  true,
		})
		if err != nil {
			return fail(stderr, "run", err)
		}
		printJSON(stdout, runOutcomeJSON{Job: out.Job, State: out.State, Message: out.Message, Ledger: out.LedgerPath})
		return out.ExitCode()
	})
}
