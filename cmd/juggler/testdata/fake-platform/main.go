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
package main

import (
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
	fmt.Fprintln(os.Stderr, "usage: fake-platform serve-decisions --dir <dir>\n       JUGGLERRUNTEST_FAKE=<ringmaster|troupe|systemd-run> JUGGLERRUNTEST_DIR=<dir> fake-platform <verb args...>")
	os.Exit(2)
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
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	tmp := filepath.Join(*dir, ".port.tmp")
	if err := os.WriteFile(tmp, []byte(port), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "fake-platform: %v\n", err)
		return 1
	}
	if err := os.Rename(tmp, filepath.Join(*dir, "port")); err != nil {
		fmt.Fprintf(os.Stderr, "fake-platform: %v\n", err)
		return 1
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	_ = srv.Close()
	return 0
}
