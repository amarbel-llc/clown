package main

import (
	"os"

	"github.com/amarbel-llc/clown/internal/jobmcp"
)

// ringmasterMCP serves the ringmaster tool surface of the job-platform MCP
// server (RFC-0011, RFC-0015 §6): the hand-rolled stdio JSON-RPC server exposing
// the job-control tools (job lifecycle + status), replacing the former
// `clown job-mcp --surface ringmaster`. clown injects it as a stdioServers entry
// in the synthesized clown-builtin-jobs plugin; it is not run by hand.
func ringmasterMCP(args []string) int {
	jobmcp.Serve(os.Stdin, os.Stdout, jobmcp.SurfaceRingmaster)
	return 0
}
