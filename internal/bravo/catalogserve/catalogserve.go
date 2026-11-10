// Package catalogserve is `clown catalog traversal-serve`: a read-only
// cutting-garden traversal plugin (cutting-garden RFC 0013,
// traversal-plugin/v1) serving the live capability catalog (clown RFC 0018
// §10, clown#350) under the clown-catalog scheme:
//
//	clown-catalog://<principal>/<plugin>/<server>/<tool>/<input>
//
// One root per live session. Plugins, servers and tools are containers; a
// tool also has a body (its kind and perm), and its inputs are leaves.
// The protocol is hand-rolled, as smith's plugin is, so clown links no
// cutting-garden module.
package catalogserve

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
	"code.linenisgreat.com/clown/internal/alfa/catalogsnap"
	"code.linenisgreat.com/clown/internal/alfa/sessionstatus"
)

// ProtocolVersion is the one traversal protocol spoken.
const ProtocolVersion = "traversal-plugin/v1"

// Scheme is the URI scheme served.
const Scheme = "clown-catalog"

const schemePrefix = Scheme + "://"

// JSON-RPC error codes (RFC 0013 "Errors").
const (
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeUnsupportedVersion = -32000
	codeInvalidConfig      = -32002
)

// Node types served.
const (
	typePlugin = "clown-plugin-v1"
	typeServer = "mcp-server-v1"
	typeTool   = "mcp-tool-v1"
	typeInput  = "tool-input-v1"
)

// Config is what one serve needs.
type Config struct {
	// Cookie is TRAVERSAL_PLUGIN_COOKIE; Serve refuses to start without it.
	Cookie string
	// Dir holds the sessions' snapshots (catalogsnap.Dir).
	Dir string
	// Alive reports whether a snapshot's session is running (FrontendAlive
	// in production); a dead session's snapshot is neither listed nor
	// reachable.
	Alive    func(catalogsnap.Snapshot) bool
	Contract catalogcontract.Contract
	// RuntimeDir is where the socket's directory is made ($XDG_RUNTIME_DIR,
	// else the system temp dir).
	RuntimeDir string
	Version    string
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs one traversal session: it binds a socket, announces it on stdout,
// answers one connection, and returns when the connection ends, a shutdown
// notification arrives or stdin reaches EOF.
func Serve(stdin io.Reader, stdout io.Writer, cfg Config) error {
	if cfg.Cookie == "" {
		return errors.New("TRAVERSAL_PLUGIN_COOKIE is not set: this verb is started by cutting-garden")
	}
	dir, err := os.MkdirTemp(cfg.RuntimeDir, "clown-catalog-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	var (
		mu   sync.Mutex
		conn net.Conn
		eof  bool
	)
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		mu.Lock()
		eof = true
		if conn != nil {
			_ = conn.Close()
		}
		mu.Unlock()
		_ = ln.Close()
	}()
	if _, err := fmt.Fprintf(stdout, "%s|%s|unix|%s||traversal-plugin\n", cfg.Cookie, ProtocolVersion, sock); err != nil {
		_ = ln.Close()
		return err
	}
	c, err := ln.Accept()
	_ = ln.Close()
	if err != nil {
		mu.Lock()
		closed := eof
		mu.Unlock()
		if closed {
			return nil // stdin EOF before the host dialed: a clean exit
		}
		return err
	}
	mu.Lock()
	conn = c
	if eof {
		_ = c.Close()
	}
	mu.Unlock()
	defer c.Close()
	s := &server{cfg: cfg}
	s.answer(c, c)
	return nil
}

type server struct {
	cfg         Config
	initialized bool
	done        bool // answer stops after the current reply
}

// answer handles newline-delimited JSON-RPC from in until it ends or a
// shutdown notification arrives.
func (s *server) answer(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			if req.Method == "shutdown" {
				return
			}
			continue // other notifications: nothing to answer
		}
		res, rerr := s.handle(req.Method, req.Params)
		msg := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			msg["error"] = rerr
		} else {
			msg["result"] = res
		}
		if enc.Encode(msg) != nil || s.done {
			return
		}
	}
}

func (s *server) handle(method string, params json.RawMessage) (any, *rpcError) {
	if method == "initialize" {
		return s.initialize(params)
	}
	if !slices.Contains([]string{"roots.list", "nodes.list", "leaf.read"}, method) {
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
	}
	if !s.initialized {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "initialize first"}
	}
	switch method {
	case "roots.list":
		var roots []string
		for _, snap := range s.live() {
			roots = append(roots, uriOf(snap.Principal))
		}
		if roots == nil {
			roots = []string{}
		}
		return map[string]any{"roots": roots}, nil
	case "nodes.list":
		t, rerr := s.resolve(params)
		if rerr != nil {
			return nil, rerr
		}
		return map[string]any{"nodes": t.children()}, nil
	case "leaf.read":
		t, rerr := s.resolve(params)
		if rerr != nil {
			return nil, rerr
		}
		return t.body(), nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
}

func (s *server) initialize(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersions []string `json:"protocol_versions"`
		ConfigTOML       string   `json:"config_toml"`
	}
	if json.Unmarshal(params, &p) != nil || !slices.Contains(p.ProtocolVersions, ProtocolVersion) {
		s.done = true // RFC 0013: SHOULD exit after refusing the version
		return nil, &rpcError{Code: codeUnsupportedVersion, Message: "clown-catalog speaks " + ProtocolVersion + " only"}
	}
	if strings.TrimSpace(p.ConfigTOML) != "" {
		return nil, &rpcError{Code: codeInvalidConfig, Message: "clown-catalog takes no config section"}
	}
	s.initialized = true
	closed := func(vals []string) []map[string]string {
		out := make([]map[string]string, len(vals))
		for i, v := range vals {
			out[i] = map[string]string{"key": v}
		}
		return out
	}
	return map[string]any{
		"schema":       ProtocolVersion,
		"plugin":       map[string]string{"name": "clown-catalog", "version": s.cfg.Version},
		"schemes":      []string{Scheme},
		"type_tag":     "cutting_garden-capture_receipt-clown_catalog-v1",
		"capabilities": []string{"roots", "leaf-read"},
		"node_types": []map[string]any{
			{"tag": typePlugin, "container": true},
			{"tag": typeServer, "container": true},
			{"tag": typeTool, "container": true},
			{"tag": typeInput, "container": false, "mime_type": "application/json"},
		},
		"facets": []map[string]any{
			{"tag": typeTool, "dimensions": []map[string]any{
				{"key": "kind", "label": "Kind", "kind": "categorical", "values": closed(catalogcontract.ToolKinds)},
				{"key": "perm", "label": "Perm", "kind": "categorical", "values": closed([]string{catalogsnap.PermAllow, catalogsnap.PermAsk, catalogsnap.PermDynamic})},
			}},
			{"tag": typeInput, "dimensions": []map[string]any{
				{"key": "field_kind", "label": "Field kind", "kind": "categorical", "values": closed(catalogcontract.Kinds)},
			}},
		},
		"bodies": []map[string]any{
			{"tag": typeTool, "accepts": []string{"application/json: the tool's kind and perm (read-only)"}},
		},
	}, nil
}

// FrontendAlive reports whether the frontend that wrote snap still runs: its
// pid exists and, where /proc gives start ticks, they match the recorded ones
// (a reused pid differs). Without ticks on either side, an existing pid is
// taken as alive.
func FrontendAlive(snap catalogsnap.Snapshot) bool {
	pid := snap.FrontendPID
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	ticks := sessionstatus.StartTicks(pid)
	if ticks == 0 || snap.FrontendStartTicks == 0 {
		return true
	}
	return ticks == snap.FrontendStartTicks
}

// live is the snapshots of running sessions.
func (s *server) live() []catalogsnap.Snapshot {
	var out []catalogsnap.Snapshot
	for _, snap := range catalogsnap.List(s.cfg.Dir) {
		if s.cfg.Alive != nil && s.cfg.Alive(snap) {
			out = append(out, snap)
		}
	}
	return out
}

// target is one resolved node: segs are its path below the scheme.
type target struct {
	segs  []string
	tools []catalogsnap.ToolNode // the session's presented tools
}

// resolve parses a {"uri"} param and finds the node it names.
func (s *server) resolve(params json.RawMessage) (target, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if json.Unmarshal(params, &p) != nil || !strings.HasPrefix(p.URI, schemePrefix) {
		return target{}, &rpcError{Code: codeInvalidParams, Message: "not a " + Scheme + " URI"}
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(p.URI, schemePrefix), "/")
	if rest == "" {
		return target{}, &rpcError{Code: codeInvalidParams, Message: "a " + Scheme + " URI names a session"}
	}
	var segs []string
	for _, raw := range strings.Split(rest, "/") {
		seg, err := url.PathUnescape(raw)
		if err != nil || seg == "" {
			return target{}, &rpcError{Code: codeInvalidParams, Message: "malformed URI: " + p.URI}
		}
		segs = append(segs, seg)
	}
	notFound := &rpcError{Code: codeInvalidParams, Message: "no such node: " + p.URI}
	for _, snap := range s.live() {
		if snap.Principal != segs[0] {
			continue
		}
		t := target{segs: segs, tools: catalogsnap.Present(snap, s.cfg.Contract)}
		if !t.exists() {
			return target{}, notFound
		}
		return t, nil
	}
	return target{}, notFound
}

// match is the tools at or below the target's plugin/server/tool prefix.
func (t target) match() []catalogsnap.ToolNode {
	var out []catalogsnap.ToolNode
	for _, n := range t.tools {
		path := []string{n.Plugin, n.Server, n.Tool}
		if len(t.segs)-1 <= 3 && slices.Equal(path[:len(t.segs)-1], t.segs[1:]) {
			out = append(out, n)
		} else if len(t.segs) == 5 && slices.Equal(path, t.segs[1:4]) {
			out = append(out, n)
		}
	}
	return out
}

func (t target) exists() bool {
	m := t.match()
	switch {
	case len(t.segs) > 5:
		return false
	case len(t.segs) == 5:
		return len(m) > 0 && slices.ContainsFunc(m[0].Inputs, func(in catalogsnap.Input) bool { return in.Name == t.segs[4] })
	case len(t.segs) == 1:
		return true
	}
	return len(m) > 0
}

func uriOf(segs ...string) string {
	esc := make([]string, len(segs))
	for i, s := range segs {
		esc[i] = url.PathEscape(s)
	}
	return schemePrefix + strings.Join(esc, "/")
}

func facet(key, value string, into map[string]any) {
	if value != "" {
		into[key] = []map[string]string{{"key": value}}
	}
}

func (t target) children() []map[string]any {
	out := []map[string]any{}
	seen := map[string]bool{}
	add := func(name, typ string, facets map[string]any) {
		if seen[name] {
			return
		}
		seen[name] = true
		n := map[string]any{"uri": uriOf(append(slices.Clone(t.segs), name)...), "name": name, "type": typ}
		if len(facets) > 0 {
			n["facets"] = facets
		}
		out = append(out, n)
	}
	for _, n := range t.match() {
		switch len(t.segs) {
		case 1:
			add(n.Plugin, typePlugin, nil)
		case 2:
			add(n.Server, typeServer, nil)
		case 3:
			f := map[string]any{}
			facet("kind", n.Kind, f)
			facet("perm", n.Perm, f)
			add(n.Tool, typeTool, f)
		case 4:
			for _, in := range n.Inputs {
				f := map[string]any{}
				facet("field_kind", in.FieldKind, f)
				add(in.Name, typeInput, f)
			}
		}
	}
	return out
}

// body is the node's own body: a tool's pins, an input's field kind;
// the other levels have none.
func (t target) body() map[string]any {
	m := t.match()
	if len(m) == 0 || len(t.segs) < 4 {
		return map[string]any{"ok": false}
	}
	n := m[0]
	structured := map[string]string{}
	if len(t.segs) == 4 {
		for k, v := range map[string]string{"kind": n.Kind, "perm": n.Perm} {
			if v != "" {
				structured[k] = v
			}
		}
	} else {
		for _, in := range n.Inputs {
			if in.Name == t.segs[4] {
				structured["field_kind"] = in.FieldKind
			}
		}
	}
	return map[string]any{"ok": true, "structured": structured}
}
