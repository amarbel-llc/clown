// Command clown-bridge-seam is a test seam, shipped as its own flake output
// (.#bridge-seam), NOT in .#default and not a verb of `clown`: it calls the REAL
// writers a launch uses, so spinclass's two-real-halves proof can produce the
// bridge's cgroup file and the unit record without importing clown or writing
// the formats by hand. It adds no authority: it writes one file at a path the
// caller can already write. Nothing in a launch runs it, and no production
// binary carries it (a test in cmd/clown fails if the verb comes back).
//
//	clown-bridge-seam cgroup-file --path P --cgroup CG
//	    bridgewire.WriteCgroupFile(P, CG): 0600, exactly CG, atomic.
//	clown-bridge-seam result --path P --unit U --cgroup CG
//	    [--principal X] [--bridge recorded|off:...] [--main-pid N]
//	    agentscope.WriteResult(P, ...): the unit record (version and capabilities
//	    filled as in a launch; uid is this process's).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
	"code.linenisgreat.com/clown/internal/bravo/agentscope"
)

const usage = "usage: clown-bridge-seam cgroup-file --path P --cgroup CG | result --path P --unit U --cgroup CG [--principal X] [--bridge V] [--main-pid N]"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "clown-bridge-seam: "+usage)
		return 2
	}
	fs := flag.NewFlagSet("clown-bridge-seam "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("path", "", "file to write")
	cgroup := fs.String("cgroup", "", "the unit's exact cgroup path")
	unit := fs.String("unit", "", "the unit name (result)")
	principal := fs.String("principal", "", "the session principal (result)")
	bridge := fs.String("bridge", "", "the result's bridge field (result)")
	mainPID := fs.Int("main-pid", 0, "the unit's main pid (result)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *path == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "clown-bridge-seam: "+usage)
		return 2
	}
	switch args[0] {
	case "cgroup-file":
		if err := bridgewire.WriteCgroupFile(*path, *cgroup); err != nil {
			fmt.Fprintf(stderr, "clown-bridge-seam: %v\n", err)
			return 1
		}
	case "result":
		r := agentscope.Result{Unit: *unit, Cgroup: *cgroup, Principal: *principal, Bridge: *bridge, MainPID: *mainPID, UID: os.Getuid()}
		if err := agentscope.WriteResult(*path, r); err != nil {
			fmt.Fprintf(stderr, "clown-bridge-seam: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "clown-bridge-seam: unknown form %q; %s\n", args[0], usage)
		return 2
	}
	fmt.Fprintf(stdout, "wrote %s\n", *path)
	return 0
}
