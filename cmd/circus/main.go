package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: circus <start|stop|status|models> [--model <name-or-path>]")
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
		if err := statusDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		return 0
	case "models":
		return cmdModels()
	default:
		fmt.Fprintf(os.Stderr, "circus: unknown command %q\n", args[0])
		return 1
	}
}

func cmdModels() int {
	names, err := listModels(modelsDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}
	for _, name := range names {
		fmt.Println(name)
	}
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
