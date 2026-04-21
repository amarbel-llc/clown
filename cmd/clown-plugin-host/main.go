package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	pluginDirs, downstream := splitArgs(os.Args[1:])
	if len(downstream) == 0 {
		fmt.Fprintln(os.Stderr, "clown-plugin-host: missing downstream command after --")
		os.Exit(1)
	}

	_ = pluginDirs // Phase 2: scan these for clown.json

	execDownstream(downstream)
}

// splitArgs separates our flags (before --) from the downstream command
// (after --). Only --plugin-dir is recognized.
func splitArgs(args []string) (pluginDirs []string, downstream []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			downstream = args[i+1:]
			return
		}
		if args[i] == "--plugin-dir" && i+1 < len(args) {
			pluginDirs = append(pluginDirs, args[i+1])
			i++
			continue
		}
		fmt.Fprintf(os.Stderr, "clown-plugin-host: unknown flag %q\n", args[i])
		os.Exit(1)
	}
	return
}

func execDownstream(args []string) {
	binary, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		os.Exit(1)
	}
	if err := syscall.Exec(binary, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: exec %s: %v\n", binary, err)
		os.Exit(1)
	}
}
