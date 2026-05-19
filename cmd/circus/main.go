package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/amarbel-llc/clown/internal/circusmodels"
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
		return cmdStart(args[1:])
	case "stop":
		if err := stopDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		return 0
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
		return cmdModels()
	case "download":
		return cmdDownload(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "circus: unknown command %q\n", args[0])
		return 1
	}
}

func cmdModels() int {
	names, err := circusmodels.List(circusmodels.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}
	for _, name := range names {
		fmt.Println(name)
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

func cmdStart(args []string) int {
	var model string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--model" && i+1 < len(args):
			model = args[i+1]
			i++
		case len(args[i]) > 8 && args[i][:8] == "--model=":
			model = args[i][8:]
		default:
			fmt.Fprintf(os.Stderr, "circus: unknown flag %q\n", args[i])
			return 1
		}
	}
	if model != "" {
		os.Setenv("CIRCUS_MODEL", model)
	}

	port, spawned, err := attachOrStart()
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}

	if !spawned && model != "" {
		fmt.Fprintf(os.Stderr, "circus: warning: --model ignored; llama-server already running (stop it first to switch models)\n")
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// If stdout is not a terminal, we were launched by clown: emit handshake
	// and block until stdin closes (clown shutting down).
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		// Clown-protocol handshake: 1|1|tcp|<addr>|streamable-http
		fmt.Printf("1|1|tcp|%s|streamable-http\n", addr)
		os.Stdout.Sync()

		// Block until clown closes our stdin.
		buf := make([]byte, 1)
		for {
			_, err := os.Stdin.Read(buf)
			if err != nil {
				break
			}
		}

		if !spawned {
			// Attached to existing daemon — leave it running.
			return 0
		}
		if err := stopDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "circus: stop on exit: %v\n", err)
		}
		return 0
	}

	// Interactive: just print status.
	action := "attached to existing"
	if spawned {
		action = "started"
	}
	fmt.Printf("circus: %s llama-server at http://%s\n", action, addr)
	return 0
}
