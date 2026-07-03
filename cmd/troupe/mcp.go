package main

import (
	"os"

	"github.com/amarbel-llc/clown/internal/jobmcp"
)

// troupeMCP serves the troupe tool surface of the job-platform MCP server
// (RFC-0011, RFC-0015 §6): the messaging tools (chat_send/chat_read/chat_list
// and the standalone job_message), replacing the former
// `clown job-mcp --surface troupe`. clown injects it as a stdioServers entry in
// the synthesized clown-builtin-jobs plugin; it is not run by hand.
func troupeMCP(args []string) int {
	jobmcp.Serve(os.Stdin, os.Stdout, jobmcp.SurfaceTroupe)
	return 0
}
