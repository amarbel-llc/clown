package pluginhost

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

type testServer struct {
	*http.Server
	Addr string
}

func TestWaitHealthyImmediate(t *testing.T) {
	srv := startHealthServer(t, http.StatusOK)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := WaitHealthy(ctx, srv.Addr, "/healthz", 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitHealthyTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := WaitHealthy(ctx, addr, "/healthz", 100*time.Millisecond); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitHealthyRetryThenSucceed(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := WaitHealthy(ctx, ln.Addr().String(), "/healthz", 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls, got %d", calls)
	}
}

func startHealthServer(t *testing.T, statusCode int) *testServer {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
	})
	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	return &testServer{Server: srv, Addr: ln.Addr().String()}
}
