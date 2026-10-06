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

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglerbrief"
	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const runUsage = "usage: juggler run --brief <file|-> [--job <id>] [--brief-stanza <id>] [--moxy <path> | --moxy-url <http url>] [--ringmaster <path>] [--troupe <path>]"

// platformBins are the consumed binaries' paths: flag, then
// JUGGLER_{RINGMASTER,TROUPE,SYSTEMD_RUN}_BIN, then the bare name on PATH.
type platformBins struct {
	ringmaster, troupe, systemdRun string
}

func (p *platformBins) register(fs *flag.FlagSet, withSystemdRun bool) {
	fs.StringVar(&p.ringmaster, "ringmaster", "", "ringmaster binary (default $"+jr.RingmasterBinEnv+" or ringmaster)")
	fs.StringVar(&p.troupe, "troupe", "", "troupe binary (default $"+jr.TroupeBinEnv+" or troupe)")
	if withSystemdRun {
		fs.StringVar(&p.systemdRun, "systemd-run", "", "systemd-run binary (default $"+jr.SystemdRunBinEnv+" or systemd-run)")
	}
}

func (p platformBins) ringmasterClient() jr.ExecRingmaster {
	return jr.ExecRingmaster{Bin: jr.ResolveBinary(p.ringmaster, jr.RingmasterBinEnv, "ringmaster")}
}

func (p platformBins) troupeClient() jr.ExecTroupe {
	return jr.ExecTroupe{Bin: jr.ResolveBinary(p.troupe, jr.TroupeBinEnv, "troupe")}
}

// lifecycleDeps wires the exec-backed collaborators and the default store.
func (p platformBins) lifecycleDeps(userManager bool) (jr.LifecycleDeps, error) {
	store, err := jr.DefaultStore()
	if err != nil {
		return jr.LifecycleDeps{}, err
	}
	return jr.LifecycleDeps{
		Ringmaster: p.ringmasterClient(),
		Troupe:     p.troupeClient(),
		Units:      jr.ExecSystemdRun{Bin: jr.ResolveBinary(p.systemdRun, jr.SystemdRunBinEnv, "systemd-run"), UserManager: userManager},
		Store:      store,
	}, nil
}

// readInput reads path, or stdin for "-".
func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

// leadingArg splits a leading positional (a job id) from the flags after it,
// so `juggler resolve <job> --state …` and `--state … <job>` both parse.
func leadingArg(args []string) (string, []string) {
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		return args[0], args[1:]
	}
	return "", args
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
		fmt.Fprintf(stderr, "juggler: run: reading brief: %v\n", err)
		return jr.ExitUsage
	}
	brief, err := jugglerbrief.Parse(data)
	if err != nil {
		fmt.Fprintf(stderr, "juggler: run: %v\n", err)
		return jr.ExitUsage
	}
	if moxyURL == "" {
		moxyURL = os.Getenv(jr.MoxyURLEnv)
	}
	store, err := jr.DefaultStore()
	if err != nil {
		fmt.Fprintf(stderr, "juggler: run: %v\n", err)
		return jr.ExitUsage
	}

	out, err := jr.RunAgent(ctx, jr.AgentDeps{
		Ringmaster:    bins.ringmasterClient(),
		Troupe:        bins.troupeClient(),
		ResolveModel:  resolve,
		HTTPClient:    httpClient,
		Stderr:        stderr,
		Moxy:          jr.ExecMoxy{Bin: jr.ResolveBinary(moxyBin, jr.MoxyBinEnv, "moxy"), Stderr: stderr},
		AgentStateDir: filepath.Join(store.Root, "agents", brief.Principal),
	}, jr.AgentRequest{
		Brief:        brief,
		Job:          job,
		BriefStanza:  stanza,
		MoxyURL:      moxyURL,
		EnvPrincipal: os.Getenv(jr.SessionIDEnv),
		WatchCancel:  true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "juggler: run: %v\n", err)
		return jr.ExitUsage
	}
	printJSON(stdout, runOutcomeJSON{Job: out.Job, State: out.State, Message: out.Message, Ledger: out.LedgerPath})
	return out.ExitCode()
}
