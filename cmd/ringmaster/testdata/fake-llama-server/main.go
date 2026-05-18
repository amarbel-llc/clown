// Command fake-llama-server is a minimal stand-in for llama-server
// used in cmd/ringmaster's Launcher tests. It binds an HTTP server on
// the requested port and serves only the two endpoints ringmaster
// probes: /health (used by Launcher.waitHealthy) and /v1/models
// (used by future model-identity sanity checks).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func main() {
	port := flag.Int("port", 0, "port to bind")
	host := flag.String("host", "127.0.0.1", "host to bind")
	alias := flag.String("alias", "", "model alias to report at /v1/models")
	_ = flag.String("model", "", "model path (ignored)")
	flag.Parse()

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-llama-server: listen %s: %v\n", addr, err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": *alias, "object": "model"},
			},
		})
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	srv.Close()
}
