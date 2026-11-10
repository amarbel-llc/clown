package catalogserve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
	"code.linenisgreat.com/clown/internal/alfa/catalogsnap"
)

func snapshot(principal string) catalogsnap.Snapshot {
	return catalogsnap.Snapshot{
		Principal: principal,
		Harness:   catalogsnap.HarnessClaudeCode,
		Servers: []catalogsnap.Server{
			{Plugin: "moxy", Server: "moxy", Tools: []catalogsnap.Tool{
				{Name: "folio.read", Description: "Read. [perms: dynamic]", InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`)},
				{Name: "smith.issue-list", Description: "[perms: always-allow]", InputSchema: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`)},
				{Name: "status", InputSchema: json.RawMessage(`{"type":"object"}`)},
			}},
			{Plugin: "just-us", Server: "just-us", Tools: []catalogsnap.Tool{
				{Name: "run_recipe", InputSchema: json.RawMessage(`{"type":"object","properties":{"recipe":{"type":"string"}}}`)},
			}},
		},
	}
}

// newServer is a server over a catalog dir holding a live session "live" and
// a dead one "dead".
func newServer(t *testing.T) (*server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "catalog")
	for _, p := range []string{"live", "dead"} {
		if err := catalogsnap.Write(dir, snapshot(p)); err != nil {
			t.Fatal(err)
		}
	}
	c, err := catalogcontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	return &server{cfg: Config{Dir: dir, Alive: func(s catalogsnap.Snapshot) bool { return s.Principal == "live" }, Contract: c, Version: "test"}}, dir
}

func call(t *testing.T, s *server, method string, params any) (json.RawMessage, *rpcError) {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, rerr := s.handle(method, b)
	if rerr != nil {
		return nil, rerr
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func initialized(t *testing.T) (*server, string) {
	t.Helper()
	s, dir := newServer(t)
	if _, rerr := call(t, s, "initialize", map[string]any{"protocol_versions": []string{ProtocolVersion}}); rerr != nil {
		t.Fatalf("initialize: %+v", rerr)
	}
	return s, dir
}

type node struct {
	URI    string                         `json:"uri"`
	Name   string                         `json:"name"`
	Type   string                         `json:"type"`
	Facets map[string][]map[string]string `json:"facets"`
}

func list(t *testing.T, s *server, uri string) []node {
	t.Helper()
	raw, rerr := call(t, s, "nodes.list", map[string]string{"uri": uri})
	if rerr != nil {
		t.Fatalf("nodes.list %s: %+v", uri, rerr)
	}
	var res struct {
		Nodes []node `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res.Nodes
}

func names(ns []node) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Name+":"+n.Type)
	}
	return strings.Join(out, " ")
}

func TestInitializeDeclaresTheSchemeAndEveryCatalogLevelAsAContainer(t *testing.T) {
	s, _ := newServer(t)
	raw, rerr := call(t, s, "initialize", map[string]any{"protocol_versions": []string{ProtocolVersion}})
	if rerr != nil {
		t.Fatal(rerr)
	}
	var res struct {
		Schema       string   `json:"schema"`
		Schemes      []string `json:"schemes"`
		Capabilities []string `json:"capabilities"`
		NodeTypes    []struct {
			Tag       string `json:"tag"`
			Container bool   `json:"container"`
		} `json:"node_types"`
		Bodies []struct {
			Tag string `json:"tag"`
		} `json:"bodies"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Schema != ProtocolVersion || strings.Join(res.Schemes, ",") != Scheme {
		t.Errorf("schema %q schemes %v", res.Schema, res.Schemes)
	}
	want := map[string]bool{"clown-plugin-v1": true, "mcp-server-v1": true, "mcp-tool-v1": true, "tool-input-v1": false}
	for _, nt := range res.NodeTypes {
		if c, ok := want[nt.Tag]; !ok || c != nt.Container {
			t.Errorf("node type %s container=%v, want declared with container=%v", nt.Tag, nt.Container, c)
		}
		delete(want, nt.Tag)
	}
	if len(want) != 0 {
		t.Errorf("node types not declared: %v", want)
	}
	if len(res.Bodies) != 1 || res.Bodies[0].Tag != "mcp-tool-v1" {
		t.Errorf("bodies %+v, want mcp-tool-v1's (a container with a body)", res.Bodies)
	}
	if strings.Join(res.Capabilities, ",") != "roots,leaf-read" {
		t.Errorf("capabilities %v", res.Capabilities)
	}
}

func TestInitializeRefusesAnUnknownVersionAndAnyConfig(t *testing.T) {
	s, _ := newServer(t)
	if _, rerr := call(t, s, "initialize", map[string]any{"protocol_versions": []string{"traversal-plugin/v2"}}); rerr == nil || rerr.Code != codeUnsupportedVersion {
		t.Errorf("unknown version: %+v, want %d", rerr, codeUnsupportedVersion)
	}
	if _, rerr := call(t, s, "initialize", map[string]any{"protocol_versions": []string{ProtocolVersion}, "config_toml": "x = 1\n"}); rerr == nil || rerr.Code != codeInvalidConfig {
		t.Errorf("config: %+v, want %d", rerr, codeInvalidConfig)
	}
}

func TestRequestsBeforeInitializeAndUnknownMethodsAreRefused(t *testing.T) {
	s, _ := newServer(t)
	if _, rerr := call(t, s, "roots.list", map[string]any{}); rerr == nil || rerr.Code != codeInvalidRequest {
		t.Errorf("before initialize: %+v, want %d", rerr, codeInvalidRequest)
	}
	if _, rerr := call(t, s, "node.put", map[string]any{}); rerr == nil || rerr.Code != codeMethodNotFound {
		t.Errorf("an unadvertised method before initialize: %+v, want %d", rerr, codeMethodNotFound)
	}
	s, _ = initialized(t)
	for _, m := range []string{"facets.counts", "node.patch", "node.delete"} {
		if _, rerr := call(t, s, m, map[string]any{}); rerr == nil || rerr.Code != codeMethodNotFound {
			t.Errorf("%s: %+v, want %d", m, rerr, codeMethodNotFound)
		}
	}
}

// A dead session's snapshot is hidden from roots.list and unreachable, but
// left on disk (a launch prunes it after catalogsnap.Retention).
func TestADeadSessionIsAbsentFromRootsButStillOnDisk(t *testing.T) {
	s, dir := initialized(t)
	raw, rerr := call(t, s, "roots.list", map[string]any{})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(raw) != `{"roots":["clown-catalog://live"]}` {
		t.Errorf("roots.list = %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, catalogsnap.FileName("dead"))); err != nil {
		t.Errorf("the dead session's snapshot is gone: %v", err)
	}
	if _, rerr := call(t, s, "nodes.list", map[string]string{"uri": "clown-catalog://dead"}); rerr == nil || rerr.Code != codeInvalidParams {
		t.Errorf("listing a dead session: %+v, want %d", rerr, codeInvalidParams)
	}
}

func TestTheTreeWalksPluginServerToolInput(t *testing.T) {
	s, _ := initialized(t)
	if got := names(list(t, s, "clown-catalog://live")); got != "clown-builtin-harness:clown-plugin-v1 just-us:clown-plugin-v1 moxy:clown-plugin-v1" {
		t.Errorf("plugins: %s", got)
	}
	if got := names(list(t, s, "clown-catalog://live/moxy")); got != "folio:mcp-server-v1 moxy:mcp-server-v1 smith:mcp-server-v1" {
		t.Errorf("moxy servers: %s", got)
	}
	tools := list(t, s, "clown-catalog://live/moxy/folio/")
	if names(tools) != "read:mcp-tool-v1" || tools[0].URI != "clown-catalog://live/moxy/folio/read" {
		t.Fatalf("folio tools: %+v", tools)
	}
	if p := tools[0].Facets["perm"]; len(p) != 1 || p[0]["key"] != "dynamic" {
		t.Errorf("folio read perm facet %v", tools[0].Facets)
	}
	inputs := list(t, s, "clown-catalog://live/moxy/folio/read")
	if names(inputs) != "file_path:tool-input-v1" || inputs[0].Facets["field_kind"][0]["key"] != "path" {
		t.Errorf("folio read inputs: %+v", inputs)
	}
	if got := list(t, s, "clown-catalog://live/moxy/folio/read/file_path"); len(got) != 0 {
		t.Errorf("an input has children: %+v", got)
	}
	if got := names(list(t, s, "clown-catalog://live/just-us/just-us")); got != "run_recipe:mcp-tool-v1" {
		t.Errorf("just-us tools: %s", got)
	}
}

func TestForeignSchemesAndMissingNodesAreCallerFaults(t *testing.T) {
	s, _ := initialized(t)
	for _, uri := range []string{"", "clown://live", "clown-catalog://", "clown-catalog://live/nope", "clown-catalog://live/moxy/folio/write"} {
		if _, rerr := call(t, s, "nodes.list", map[string]string{"uri": uri}); rerr == nil || rerr.Code != codeInvalidParams {
			t.Errorf("nodes.list %q: %+v, want %d", uri, rerr, codeInvalidParams)
		}
	}
}

// The tool's body is its pins, never its schema; no lock until a digest the
// enforcers share lands (clown#354), so none is reported.
func TestLeafReadOnAToolCarriesThePinsAndNoSchemaKeyOrLock(t *testing.T) {
	s, _ := initialized(t)
	raw, rerr := call(t, s, "leaf.read", map[string]string{"uri": "clown-catalog://live/moxy/folio/read"})
	if rerr != nil {
		t.Fatal(rerr)
	}
	var res struct {
		OK         bool              `json:"ok"`
		Structured map[string]string `json:"structured"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Structured["perm"] != "dynamic" {
		t.Errorf("leaf.read = %s", raw)
	}
	for _, key := range []string{"properties", "required", `"type"`, "file_path", "lock"} {
		if bytes.Contains(raw, []byte(key)) {
			t.Errorf("leaf.read on a tool carries %s: %s", key, raw)
		}
	}
	raw, _ = call(t, s, "leaf.read", map[string]string{"uri": "clown-catalog://live/moxy"})
	if string(raw) != `{"ok":false}` {
		t.Errorf("leaf.read on a plugin = %s, want no body", raw)
	}
}

// The wire: the announce line, a dialed request, and stdin EOF as shutdown.
func TestServeAnnouncesAnswersAndExitsOnStdinEOF(t *testing.T) {
	s, _ := newServer(t)
	cfg := s.cfg
	cfg.Cookie = "c00kie"
	// A short dir: t.TempDir under darwin's nix sandbox overflows sun_path.
	runtimeDir, err := os.MkdirTemp("/tmp", "cc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	cfg.RuntimeDir = runtimeDir
	stdinR, stdinW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(stdinR, outW, cfg) }()

	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Split(strings.TrimSuffix(line, "\n"), "|")
	if len(f) != 6 || f[0] != "c00kie" || f[1] != ProtocolVersion || f[2] != "unix" || f[5] != "traversal-plugin" {
		t.Fatalf("announce line %q", line)
	}
	conn, err := net.Dial("unix", f[3])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocol_versions":["traversal-plugin/v1"]}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp, `"id":7`) || !strings.Contains(resp, `"schemes":["clown-catalog"]`) {
		t.Errorf("initialize response %s", resp)
	}
	stdinW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after stdin EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not exit on stdin EOF")
	}
	if _, err := os.Stat(filepath.Dir(f[3])); !os.IsNotExist(err) {
		t.Errorf("socket dir left behind: %v", err)
	}
}

func TestServeWithoutACookieWritesNothingToStdout(t *testing.T) {
	s, _ := newServer(t)
	var out bytes.Buffer
	if err := Serve(strings.NewReader(""), &out, s.cfg); err == nil {
		t.Error("Serve without a cookie: no error")
	}
	if out.Len() != 0 {
		t.Errorf("stdout %q", out.String())
	}
}
