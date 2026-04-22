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

	mode := "sleep"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

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
