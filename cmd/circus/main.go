package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: circus <start|stop|status|list|models|download> [args]")
		return 1
	}

	switch args[0] {
	case "start":
		cli, err := dialClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		defer cli.Close()
		return cmdStart(cli, args[1:])
	case "stop":
		cli, err := dialClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		defer cli.Close()
		return cmdStop(cli, args[1:])
	case "status":
		cli, err := dialClient()
		if err != nil {
			// dialClient prints its own home-manager hint for the
			// missing-socket/refused cases; surface any other error
			// (e.g. SocketPath() failure, OS errors) so the user
			// isn't left with a bare nonzero exit.
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		defer cli.Close()
		return cmdStatus(cli, args[1:])
	case "list":
		cli, err := dialClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		defer cli.Close()
		return cmdList(cli)
	case "models":
		cli, err := dialClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		defer cli.Close()
		return cmdModels(cli)
	case "download":
		return cmdDownload(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "circus: unknown command %q\n", args[0])
		return 1
	}
}

// cmdModels asks ringmaster for the list of installed GGUFs and prints
// each model's bare name, one per line — same shape as the pre-ringmaster
// circusmodels.List scan, so existing shell pipelines keep working.
func cmdModels(cli *rm.Client) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cli.ListAvailableModels(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: models: %v\n", err)
		return 1
	}
	for _, m := range res.Models {
		fmt.Println(m.Name)
	}
	return 0
}

// cmdList asks ringmaster for the current set of live llama-server
// instances and prints them as a stable columnar table. The empty
// result set is rendered as no output (rc=0) rather than just a header
// row — quieter for scripts.
func cmdList(cli *rm.Client) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cli.ListInstances(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: list: %v\n", err)
		return 1
	}
	if len(res.Instances) == 0 {
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ALIAS\tMODEL\tBIND\tPORT\tPID\tUPTIME")
	now := time.Now()
	for _, in := range res.Instances {
		uptime := now.Sub(in.StartedAt).Round(time.Second)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			in.Alias, in.Model, in.Bind, in.Port, in.PID, uptime)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "circus: list: %v\n", err)
		return 1
	}
	return 0
}

// cmdStatus dispatches to a per-alias detail dump or the summary list
// based on argv. Unlike cmdList (which is silent on empty for scripts),
// the no-alias / empty-list path prints "no instances running" so a
// human invoker isn't left guessing whether ringmaster is reachable.
func cmdStatus(cli *rm.Client, args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if len(args) == 0 {
		return statusList(ctx, cli)
	}
	return statusOne(ctx, cli, args[0])
}

func statusList(ctx context.Context, cli *rm.Client) int {
	res, err := cli.ListInstances(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: status: %v\n", err)
		return 1
	}
	if len(res.Instances) == 0 {
		fmt.Println("circus: no instances running")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ALIAS\tMODEL\tBIND\tPORT\tPID\tUPTIME")
	now := time.Now()
	for _, in := range res.Instances {
		uptime := now.Sub(in.StartedAt).Round(time.Second)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			in.Alias, in.Model, in.Bind, in.Port, in.PID, uptime)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "circus: status: %v\n", err)
		return 1
	}
	return 0
}

func statusOne(ctx context.Context, cli *rm.Client, alias string) int {
	res, err := cli.GetInstance(ctx, rm.GetInstanceParams{Alias: alias})
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: status: %v\n", err)
		return 1
	}
	in := res.Instance
	uptime := time.Since(in.StartedAt).Round(time.Second)
	fmt.Printf("alias:      %s\n", in.Alias)
	fmt.Printf("model:      %s\n", in.Model)
	fmt.Printf("bind:       %s\n", in.Bind)
	fmt.Printf("port:       %d\n", in.Port)
	fmt.Printf("pid:        %d\n", in.PID)
	fmt.Printf("started:    %s (%s ago)\n", in.StartedAt.UTC().Format(time.RFC3339), uptime)
	return 0
}

// cmdStart parses argv for the new ringmaster-backed start command and
// issues a StartInstance RPC. First positional arg is the model name
// (required). --alias/--bind support both space and equals forms. Any
// args after `--` are passed through to llama-server as-is via the
// StartInstanceParams.Args field. The 90s timeout accommodates the
// launcher's 60s health check window with headroom.
func cmdStart(cli *rm.Client, args []string) int {
	var (
		model    string
		alias    string
		bind     string
		passArgs []string
	)
parse:
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			passArgs = append(passArgs, args[i+1:]...)
			break parse
		case a == "--alias":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "circus: --alias requires an argument")
				return 1
			}
			alias = args[i+1]
			i++
		case strings.HasPrefix(a, "--alias="):
			alias = strings.TrimPrefix(a, "--alias=")
		case a == "--bind":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "circus: --bind requires an argument")
				return 1
			}
			bind = args[i+1]
			i++
		case strings.HasPrefix(a, "--bind="):
			bind = strings.TrimPrefix(a, "--bind=")
		case strings.HasPrefix(a, "--"):
			fmt.Fprintf(os.Stderr, "circus: unknown flag %q\n", a)
			return 1
		default:
			if model != "" {
				fmt.Fprintf(os.Stderr, "circus: unexpected positional arg %q (model already set to %q)\n", a, model)
				return 1
			}
			model = a
		}
	}
	if model == "" {
		fmt.Fprintln(os.Stderr, "circus: missing required model argument\nusage: circus start <model> [--alias name] [--bind addr] [-- ...passthrough]")
		return 1
	}
	if alias == "" {
		alias = model
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := cli.StartInstance(ctx, rm.StartInstanceParams{
		Alias: alias,
		Model: model,
		Bind:  bind,
		Args:  passArgs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: start: %v\n", err)
		return 1
	}
	in := res.Instance
	fmt.Printf("circus: started %s at %s:%d (pid %d)\n", in.Alias, in.Bind, in.Port, in.PID)
	return 0
}

// cmdStop issues a StopInstance RPC for the given alias. The 30s
// timeout covers the launcher's 5s grace period plus SIGKILL with
// margin.
func cmdStop(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "circus: missing alias\nusage: circus stop <alias>")
		return 1
	}
	alias := args[0]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cli.StopInstance(ctx, rm.StopInstanceParams{Alias: alias}); err != nil {
		fmt.Fprintf(os.Stderr, "circus: stop: %v\n", err)
		return 1
	}
	fmt.Printf("circus: stopped %s\n", alias)
	return 0
}
