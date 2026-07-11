package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	rm "github.com/amarbel-llc/clown/internal/juggler"
	"github.com/amarbel-llc/clown/internal/jugglermodels"
)

// server is the control-plane daemon's RPC dispatcher (the `juggler
// daemon` verb). It owns the registry and the llama-server launcher.
type server struct {
	registry  *rm.Registry
	launcher  Launcher // nil-safe; methods check before use
	log       *slog.Logger
	modelsDir string // root for ListAvailableModels lookups
	// models is an optional override for the downloadable-model
	// registry. Production callers leave this nil and dispatch falls
	// back to jugglermodels.Registry(); tests inject a fixture pointed
	// at an httptest.Server.
	models []jugglermodels.RegistryEntry
	// remoteModelsPath is where the remote-model registry file lives.
	// Empty in zero-value servers used by tests that don't exercise the
	// new methods; newServer sets it via rm.RemoteModelsPath().
	remoteModelsPath string
	// remoteModelsMu serializes the load->mutate->save sequence in
	// AddRemoteModel/RemoveRemoteModel. Each connection is handled on its
	// own goroutine (see Serve), and SaveRemoteModels's atomic
	// temp-file+rename only guards against a corrupted file, not against
	// two concurrent handlers each reading the same stale snapshot and one
	// clobbering the other's change (a lost update). Zero-value sync.Mutex
	// is ready to use — newServer doesn't need to initialize it.
	remoteModelsMu sync.Mutex
}

// Launcher abstracts how new llama-server instances are spawned. The
// real implementation calls exec.Command; tests pass a fake.
type Launcher interface {
	Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error)
	Stop(ctx context.Context, alias string) error
}

func newServer(reg *rm.Registry, l Launcher) *server {
	// Best-effort: an error here (e.g. no home dir) shouldn't fail daemon
	// startup — just leave the path empty and let the affected RPCs
	// surface the failure when actually invoked.
	remoteModelsPath, _ := rm.RemoteModelsPath()
	return &server{
		registry:         reg,
		launcher:         l,
		log:              slog.New(slog.NewTextHandler(os.Stderr, nil)),
		modelsDir:        jugglermodels.Dir(),
		remoteModelsPath: remoteModelsPath,
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

	case rm.MethodStopAll:
		if s.launcher == nil {
			return rpcError(req.ID, -32000, "launcher not configured")
		}
		// Snapshot the alias list before iterating; Stop mutates the registry.
		snapshot := s.registry.List()
		stopped := make([]string, 0, len(snapshot))
		for _, in := range snapshot {
			// TODO: thread a per-connection context through dispatch (see StartInstance).
			if err := s.launcher.Stop(context.Background(), in.Alias); err != nil {
				// A concurrent StopInstance on another connection may have
				// drained the launcher's children map between our snapshot and
				// this call. If the alias is no longer in the registry, it
				// was stopped — count it. Otherwise the failure is real.
				if _, stillRunning := s.registry.Get(in.Alias); !stillRunning {
					stopped = append(stopped, in.Alias)
					continue
				}
				s.log.Error("stopAll", "alias", in.Alias, "err", err)
				continue
			}
			stopped = append(stopped, in.Alias)
		}
		return rpcResult(req.ID, rm.StopAllResult{Stopped: stopped})

	case rm.MethodListAvailableModels:
		models, err := listAvailableModels(s.modelsDir)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list models: %v", err))
		}
		return rpcResult(req.ID, rm.ListAvailableModelsResult{Models: models})

	case rm.MethodDownloadModel:
		var p rm.DownloadModelParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		entries := s.models
		if entries == nil {
			loaded, err := jugglermodels.Registry()
			if err != nil {
				return rpcError(req.ID, -32000, fmt.Sprintf("load registry: %v", err))
			}
			entries = loaded
		}
		entry, ok := jugglermodels.FindEntry(p.Name, entries)
		if !ok {
			return rpcError(req.ID, -32000, fmt.Sprintf("unknown model %q", p.Name))
		}
		// TODO: thread a per-connection context through dispatch (see StartInstance).
		dest, err := jugglermodels.Download(context.Background(), entry, s.modelsDir, nil)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("download: %v", err))
		}
		st, err := os.Stat(dest)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("stat: %v", err))
		}
		return rpcResult(req.ID, rm.DownloadModelResult{Model: rm.AvailableModel{
			Name: p.Name,
			Path: dest,
			Size: st.Size(),
		}})

	case rm.MethodListModels:
		local, err := jugglermodels.List(s.modelsDir)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list local models: %v", err))
		}
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list remote models: %v", err))
		}
		models := make([]rm.Model, 0, len(local)+len(remote))
		for _, name := range local {
			models = append(models, rm.Model{Name: name, Kind: rm.ModelKindLocal})
		}
		for _, m := range remote {
			models = append(models, rm.Model{Name: m.Name, Kind: rm.ModelKindRemote, Style: m.Style})
		}
		return rpcResult(req.ID, rm.ListModelsResult{Models: models})

	case rm.MethodAddRemoteModel:
		var p rm.AddRemoteModelParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		s.remoteModelsMu.Lock()
		defer s.remoteModelsMu.Unlock()
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("load remote models: %v", err))
		}
		remote = rm.UpsertRemoteModel(remote, rm.RemoteModel{
			Name: p.Name, Style: p.Style, URL: p.URL, Token: p.Token,
		})
		if err := rm.SaveRemoteModels(s.remoteModelsPath, remote); err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("save remote models: %v", err))
		}
		return rpcResult(req.ID, rm.AddRemoteModelResult{})

	case rm.MethodRemoveRemoteModel:
		var p rm.RemoveRemoteModelParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		s.remoteModelsMu.Lock()
		defer s.remoteModelsMu.Unlock()
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("load remote models: %v", err))
		}
		remaining, found := rm.RemoveRemoteModel(remote, p.Name)
		if !found {
			return rpcError(req.ID, -32001, fmt.Sprintf("remote model %q not found", p.Name))
		}
		if err := rm.SaveRemoteModels(s.remoteModelsPath, remaining); err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("save remote models: %v", err))
		}
		// RemoveRemoteModel returns no result type; use null (mirrors StopInstance).
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: []byte("null")}

	case rm.MethodResolveModel:
		var p rm.ResolveModelParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list remote models: %v", err))
		}
		for _, m := range remote {
			if m.Name == p.Name {
				return rpcResult(req.ID, rm.ResolveModelResult{
					Kind: rm.ModelKindRemote, Style: m.Style,
					URL: os.ExpandEnv(m.URL), Token: os.ExpandEnv(m.Token),
				})
			}
		}
		// Not a remote entry — try local. Reuse a running instance if the
		// alias is already up; otherwise fall through to the same start
		// path MethodStartInstance uses.
		if in, ok := s.registry.Get(p.Name); ok {
			return rpcResult(req.ID, rm.ResolveModelResult{
				Kind: rm.ModelKindLocal, URL: fmt.Sprintf("http://%s:%d", in.Bind, in.Port),
			})
		}
		local, err := jugglermodels.List(s.modelsDir)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list local models: %v", err))
		}
		found := false
		for _, name := range local {
			if name == p.Name {
				found = true
				break
			}
		}
		if !found {
			return rpcError(req.ID, -32001, fmt.Sprintf("model %q not found (local or remote)", p.Name))
		}
		if s.launcher == nil {
			return rpcError(req.ID, -32000, "launcher not configured")
		}
		// TODO: thread a per-connection context through dispatch (see StartInstance).
		in, err := s.launcher.Start(context.Background(), rm.StartInstanceParams{Alias: p.Name, Model: p.Name})
		if err != nil {
			return rpcError(req.ID, -32000, err.Error())
		}
		return rpcResult(req.ID, rm.ResolveModelResult{
			Kind: rm.ModelKindLocal, URL: fmt.Sprintf("http://%s:%d", in.Bind, in.Port),
		})

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
