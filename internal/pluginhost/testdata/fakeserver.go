//go:build ignore

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	mode := "sleep"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	// crash-before-handshake exercises the clown#72 path: the plugin
	// writes a final stderr diagnostic and exits without ever emitting
	// the clown-protocol handshake on stdout.
	if mode == "crash-before-handshake" {
		fmt.Fprintln(os.Stderr, "starting up")
		fmt.Fprintln(os.Stderr, "fatal: fakeserver crash diagnostic")
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	fmt.Printf("1|1|tcp|%s|streamable-http\n", ln.Addr().String())

	switch mode {
	case "exit-immediate":
		time.Sleep(50 * time.Millisecond)
		os.Exit(0)
	case "exit-code":
		time.Sleep(50 * time.Millisecond)
		os.Exit(42)
	case "sleep":
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		os.Exit(0)
	}
}
