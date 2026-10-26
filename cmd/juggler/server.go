package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglermodels"
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

	// onReadEnd and onHandleDone, when set, are called when a connection's
	// read side ends and when its handler returns (tests).
	onReadEnd, onHandleDone func()
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

// maxQueuedRequests bounds the requests one connection may pipeline ahead
// of the one being dispatched; a client that sends more is dropped.
const maxQueuedRequests = 16

// handle serves one connection (clown#82).
//
// The protocol rule: a client keeps its write side open until its reply
// arrives. The read side ending, whether by a close or a half-close
// (shutdown of the client's write side; it cannot be told apart from a
// close here), means the client is gone: the connection's ctx is
// cancelled, which aborts the client-scoped work in flight, requests still
// queued are not dispatched, and no reply is written. internal/juggler's
// Client never half-closes.
//
// Frames are read on their own goroutine, which keeps reading while a
// request is dispatched, so a disconnect is seen at once. Requests are
// dispatched one at a time, in order.
func (s *server) handle(conn net.Conn) {
	defer func() {
		if s.onHandleDone != nil {
			s.onHandleDone()
		}
	}()
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames := make(chan rm.Envelope, maxQueuedRequests)
	go func() {
		// Run in reverse: cancel, then the hook (so a test that sees it
		// knows ctx is already cancelled), then close the queue.
		defer close(frames)
		defer func() {
			if s.onReadEnd != nil {
				s.onReadEnd()
			}
		}()
		defer cancel()
		br := bufio.NewReader(conn)
		for {
			env, err := rm.ReadFrame(br)
			if err != nil {
				return
			}
			select {
			case frames <- env:
			default:
				s.log.Error("dropping a client that pipelined too many requests", "max", maxQueuedRequests)
				return
			}
		}
	}()

	for env := range frames {
		if ctx.Err() != nil {
			// The client is gone: queued requests are not dispatched.
			return
		}
		resp := s.dispatch(ctx, env)
		if ctx.Err() != nil {
			// The client is gone: nobody reads the answer.
			return
		}
		if err := rm.WriteFrame(conn, resp); err != nil {
			s.log.Error("write frame", "err", err)
			return
		}
	}
}

// dispatch answers one request. ctx is the connection's, cancelled when the
// client disconnects. Client-scoped work takes it: a start's wait for health
// (StartInstance, and ResolveModel's start) and a download, which a client
// that left no longer wants. A stop does not: StopInstance and StopAll run
// under context.WithoutCancel, so a stop the client asked for always
// completes, whoever disconnects.
func (s *server) dispatch(ctx context.Context, req rm.Envelope) rm.Envelope {
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
		// A client disconnect cancels the start's wait for health, and the
		// launcher kills the half-started child.
		in, err := s.launcher.Start(ctx, p)
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
		// Not client-scoped: the stop completes even if the client leaves.
		if err := s.launcher.Stop(context.WithoutCancel(ctx), p.Alias); err != nil {
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
			// Not client-scoped, as StopInstance.
			if err := s.launcher.Stop(context.WithoutCancel(ctx), in.Alias); err != nil {
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
		// A client disconnect cancels the download; Download removes its
		// temporary file.
		dest, err := jugglermodels.Download(ctx, entry, s.modelsDir, nil)
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
			TokenFile: p.TokenFile, ModelID: p.ModelID,
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
		res, err := rm.ResolveRemoteModelFromPath(s.remoteModelsPath, p.Name)
		if err == nil {
			return rpcResult(req.ID, res)
		}
		if !errors.Is(err, rm.ErrDaemonRequired) {
			return rpcError(req.ID, -32000, err.Error())
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
		// As StartInstance: a client disconnect cancels the start.
		in, err := s.launcher.Start(ctx, rm.StartInstanceParams{Alias: p.Name, Model: p.Name})
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
