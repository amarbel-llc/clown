// Command fake-platform is the bats lane's stand-in for the platform half of
// the juggler agent substrate (FDR 0019), in two modes:
//
//   - JUGGLERRUNTEST_FAKE=<ringmaster|troupe|systemd-run>
//     JUGGLERRUNTEST_DIR=<dir> fake-platform <verb args...> behaves as that
//     binary, exactly as the Go tests' fakes do (internal/jugglerrun/
//     jugglerruntest): a stateful per-target ringmaster journal, a troupe
//     that records MUC posts and wakes, a systemd-run that records and
//     succeeds. Every invocation is appended to <dir>/calls.jsonl, and a
//     file <dir>/fail/<tool>-<verb> makes that verb exit 1. The bats file
//     wraps it in one tiny shell script per tool name so juggler can be
//     pointed at the wrappers through JUGGLER_{RINGMASTER,TROUPE,
//     SYSTEMD_RUN}_BIN.
//
//   - fake-platform serve-decisions --dir <dir> serves an OpenRouter
//     Decisions endpoint (POST /api/alpha/decisions) on a loopback port,
//     written to <dir>/port. Each request answers with the HTTP status in
//     <dir>/status (default 200) and the body in <dir>/body, both re-read
//     per request so a test swaps the answer between calls; the request body
//     and Authorization header are saved to <dir>/last-request and
//     <dir>/last-auth.
//
//   - fake-platform serve-agent --dir <dir> serves `juggler run`'s model and
//     tool plane on one loopback port (see serveAgent).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"code.linenisgreat.com/clown/internal/jugglerrun/jugglerruntest"
)

func main() {
	// Exits when JUGGLERRUNTEST_FAKE names a tool; otherwise returns.
	jugglerruntest.RunFakeIfRequested()

	if len(os.Args) >= 2 && os.Args[1] == "serve-decisions" {
		os.Exit(serveDecisions(os.Args[2:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "serve-agent" {
		os.Exit(serveAgent(os.Args[2:]))
	}
	fmt.Fprintln(os.Stderr, "usage: fake-platform serve-decisions --dir <dir>\n       fake-platform serve-agent --dir <dir>\n       JUGGLERRUNTEST_FAKE=<ringmaster|troupe|systemd-run> JUGGLERRUNTEST_DIR=<dir> fake-platform <verb args...>")
	os.Exit(2)
}

// serveAgent is `juggler run`'s world on one loopback port: a scripted
// OpenAI-compatible model at /v1/chat/completions answering request N with
// the Nth element of the JSON array in <dir>/replies.json (the last repeats),
// and an MCP upstream at /mcp advertising <dir>/tools.json and answering every
// tools/call with <dir>/call-result.json. Each model request appends a line to
// <dir>/model-requests; the port is written to <dir>/port last.
func serveAgent(args []string) int {
	fs := flag.NewFlagSet("serve-agent", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory: replies.json, tools.json, call-result.json, port, model-requests")
	if err := fs.Parse(args); err != nil || *dir == "" {
		fmt.Fprintln(os.Stderr, "fake-platform: serve-agent: --dir is required")
		return 2
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(*dir, name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake-platform: %v\n", err)
			os.Exit(1)
		}
		return b
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(read("replies.json"), &raw); err != nil || len(raw) == 0 {
		fmt.Fprintf(os.Stderr, "fake-platform: replies.json must be a non-empty JSON array: %v\n", err)
		return 1
	}
	replies := make([]string, len(raw))
	for i, r := range raw {
		replies[i] = string(r)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-platform: listen: %v\n", err)
		return 1
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", jugglerruntest.ScriptedModelHandler(replies, nil, func(int) {
		f, err := os.OpenFile(filepath.Join(*dir, "model-requests"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString("request\n")
			_ = f.Close()
		}
	}))
	mux.Handle("/mcp", jugglerruntest.MCPHandler(string(read("tools.json")), string(read("call-result.json")), nil))
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	if err := writePort(*dir, ln); err != nil {
		fmt.Fprintf(os.Stderr, "fake-platform: %v\n", err)
		return 1
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	_ = srv.Close()
	return 0
}

// writePort writes ln's port to <dir>/port atomically: the readiness signal.
func writePort(dir string, ln net.Listener) error {
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	tmp := filepath.Join(dir, ".port.tmp")
	if err := os.WriteFile(tmp, []byte(port), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "port"))
}

func serveDecisions(args []string) int {
	fs := flag.NewFlagSet("serve-decisions", flag.ContinueOnError)
	dir := fs.String("dir", "", "state directory: port, status, body, last-request, last-auth")
	if err := fs.Parse(args); err != nil || *dir == "" {
		fmt.Fprintln(os.Stderr, "fake-platform: serve-decisions: --dir is required")
		return 2
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-platform: listen: %v\n", err)
		return 1
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/alpha/decisions", func(w http.ResponseWriter, r *http.Request) {
		req, _ := io.ReadAll(r.Body)
		_ = os.WriteFile(filepath.Join(*dir, "last-request"), req, 0o644)
		_ = os.WriteFile(filepath.Join(*dir, "last-auth"), []byte(r.Header.Get("Authorization")), 0o644)

		status := http.StatusOK
		if b, err := os.ReadFile(filepath.Join(*dir, "status")); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				status = n
			}
		}
		body, _ := os.ReadFile(filepath.Join(*dir, "body"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	// The port file is the readiness signal: written last, atomically.
	if err := writePort(*dir, ln); err != nil {
		fmt.Fprintf(os.Stderr, "fake-platform: %v\n", err)
		return 1
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	_ = srv.Close()
	return 0
}
