package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// server is the ringmaster daemon's RPC dispatcher. It owns the
// registry and (later) the llama-server launcher.
type server struct {
	registry *rm.Registry
	launcher Launcher // nil-safe; methods check before use
	log      *slog.Logger
}

// Launcher abstracts how new llama-server instances are spawned. The
// real implementation calls exec.Command; tests pass a fake.
type Launcher interface {
	Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error)
	Stop(ctx context.Context, alias string) error
}

func newServer(reg *rm.Registry, l Launcher) *server {
	return &server{
		registry: reg,
		launcher: l,
		log:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// Serve accepts connections until ctx is cancelled. Each connection is
// handled in its own goroutine. Errors on individual connections are
// logged, not returned.
func (s *server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.log.Error("accept", "err", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *server) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		env, err := rm.ReadFrame(br)
		if err != nil {
			return
		}
		resp := s.dispatch(env)
		if err := rm.WriteFrame(conn, resp); err != nil {
			s.log.Error("write frame", "err", err)
			return
		}
	}
}

func (s *server) dispatch(req rm.Envelope) rm.Envelope {
	switch req.Method {
	case rm.MethodListInstances:
		out := rm.ListInstancesResult{Instances: s.registry.List()}
		data, _ := json.Marshal(out)
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: data}
	default:
		return rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &rm.Error{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}
