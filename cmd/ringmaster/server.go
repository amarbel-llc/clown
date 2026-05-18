package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

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
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Backoff on accept errors. Doubles up to a cap.
			if backoff == 0 {
				backoff = 10 * time.Millisecond
			} else if backoff < time.Second {
				backoff *= 2
			}
			s.log.Error("accept", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}
		backoff = 0
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
		return rpcResult(req.ID, rm.ListInstancesResult{Instances: s.registry.List()})

	case rm.MethodStartInstance:
		var p rm.StartInstanceParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		if s.launcher == nil {
			return rpcError(req.ID, -32000, "launcher not configured")
		}
		// TODO: thread a per-connection context through dispatch so a
		// client disconnect cancels an in-flight Start (currently a
		// canceled start holds a child up to launcher.healthTimeout).
		in, err := s.launcher.Start(context.Background(), p)
		if err != nil {
			return rpcError(req.ID, -32000, err.Error())
		}
		return rpcResult(req.ID, rm.StartInstanceResult{Instance: in})

	case rm.MethodStopInstance:
		var p rm.StopInstanceParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		if s.launcher == nil {
			return rpcError(req.ID, -32000, "launcher not configured")
		}
		// TODO: see StartInstance — same context-propagation gap.
		if err := s.launcher.Stop(context.Background(), p.Alias); err != nil {
			return rpcError(req.ID, -32000, err.Error())
		}
		// StopInstance returns no result type; use null.
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: []byte("null")}

	case rm.MethodGetInstance:
		var p rm.GetInstanceParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		in, ok := s.registry.Get(p.Alias)
		if !ok {
			return rpcError(req.ID, -32001, fmt.Sprintf("alias %q not found", p.Alias))
		}
		return rpcResult(req.ID, rm.GetInstanceResult{Instance: in})

	default:
		return rpcError(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func rpcResult(id json.Number, v any) rm.Envelope {
	data, err := json.Marshal(v)
	if err != nil {
		return rpcError(id, -32603, fmt.Sprintf("marshal result: %v", err))
	}
	return rm.Envelope{JSONRPC: "2.0", ID: id, Result: data}
}

func rpcError(id json.Number, code int, msg string) rm.Envelope {
	return rm.Envelope{JSONRPC: "2.0", ID: id, Error: &rm.Error{Code: code, Message: msg}}
}
