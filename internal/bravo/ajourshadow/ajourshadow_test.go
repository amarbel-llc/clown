package ajourshadow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
	"code.linenisgreat.com/clown/internal/alfa/catalogsnap"
	"code.linenisgreat.com/cutting-garden/pkgs/ajour"
)

func mustContract(t *testing.T) catalogcontract.Contract {
	t.Helper()
	c, err := catalogcontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func args(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The hook runs in shadow until the operator rules enforcement: shadow never
// denies whatever ajour answers; enforce denies every answer but Admitted.
func TestDecideShadowNeverDeniesEnforceDoes(t *testing.T) {
	if CurrentMode != ModeShadow {
		t.Fatalf("CurrentMode = %v, want shadow (RFC 0018 §11: enforcement is an operator ruling)", CurrentMode)
	}
	for _, o := range []ajour.Outcome{ajour.OutcomeRefused, ajour.OutcomeAsk, ajour.OutcomeAdmitted} {
		v := ajour.Verdict{Outcome: o, Admitted: o == ajour.OutcomeAdmitted}
		if d := Decide(ModeShadow, v); d.Deny {
			t.Errorf("shadow denied %v: %+v", o, d)
		}
		if d := Decide(ModeEnforce, v); d.Deny != (o != ajour.OutcomeAdmitted) {
			t.Errorf("enforce on %v: deny=%v", o, d.Deny)
		}
	}
}

func TestFileID(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	outside := t.TempDir()
	outside, _ = filepath.EvalSymlinks(outside)
	wt := filepath.Join(home, "eng", "wt")
	if err := os.MkdirAll(filepath.Join(wt, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(wt, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, cwd, want string }{
		{"src/a.go", wt, "file:eng/wt/src/a.go"},
		{filepath.Join(wt, "src", "..", "src", "a.go"), "/", "file:eng/wt/src/a.go"},
		{filepath.Join(home, "link", "src", "new.go"), "/", "file:eng/wt/src/new.go"}, // symlink resolved, leaf need not exist
		{home, "/", "file:."},
		{filepath.Join(outside, "x"), "/", "file:" + filepath.Join(outside, "x")},
		{wt + "/", "/", "file:eng/wt"},
	} {
		got, err := FileID(tc.path, tc.cwd, home)
		if err != nil || got != tc.want {
			t.Errorf("FileID(%q, cwd %q) = %q, %v; want %q", tc.path, tc.cwd, got, err, tc.want)
		}
	}
}

// A built-in's path field becomes an fs-path-v1 value, an input field a
// tool-input-v1 value with its canonical text, and the tool step carries its
// catalog kind (the cutting-garden fixture call-read-src.json's shape).
func TestBuildCallPathAndInput(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	node := catalogsnap.ToolNode{Plugin: "clown-builtin-harness", Server: "harness", Tool: "Read", Kind: "read"}
	calls, unbuilt := BuildCall(node, mustContract(t), args(t, `{"file_path":"a.go","limit":5,"offset":1.5}`), home, home)
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	want := []ajour.Call{{
		Path: []ajour.Subject{
			{Type: "clown-plugin-v1", ID: "clown-builtin-harness"},
			{Type: "mcp-server-v1", ID: "harness"},
			{Type: "mcp-tool-v1", ID: "Read", Fields: map[string][]string{"kind": {"read"}}},
		},
		Values: [][]ajour.Subject{
			{{Type: "fs-path-v1", ID: "file:a.go", Fields: map[string][]string{"field": {"file_path"}}}},
			{{Type: "tool-input-v1", ID: "limit", Fields: map[string][]string{"field": {"limit"}, "value": {"5"}}}},
			{{Type: "tool-input-v1", ID: "offset", Fields: map[string][]string{"field": {"offset"}, "value": {"1.5"}}}},
		},
		Anchor: 1,
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("BuildCall =\n%+v\nwant\n%+v", calls, want)
	}
}

// RFC 0018 §4.3: a top-level array presents one subject per element, an
// empty array one subject whose value is [].
func TestBuildCallArrays(t *testing.T) {
	node := catalogsnap.ToolNode{Plugin: "example", Server: "example", Tool: "unlisted"} // every field is input
	calls, unbuilt := BuildCall(node, mustContract(t), args(t, `{"subcommand":"status","flags":["a","b"],"none":[]}`), "/", "/nonexistent-home")
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	var got []string
	for _, v := range calls[0].Values {
		got = append(got, v[0].ID+"="+strings.Join(v[0].Fields["value"], ","))
	}
	want := []string{"flags=a", "flags=b", "none=[]", "subcommand=status"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values %v, want %v", got, want)
	}
}

// An omitted field the contract resolves from the working directory is
// presented with the resolved value, never left out (RFC 0018 §4.3).
func TestBuildCallResolvesCwdField(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	node := catalogsnap.ToolNode{Plugin: "clown-builtin-harness", Server: "harness", Tool: "Glob", Kind: "read"}
	calls, unbuilt := BuildCall(node, mustContract(t), args(t, `{"pattern":"*.go"}`), home, home)
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	if v := calls[0].Values; len(v) != 2 || v[0][0].ID != "file:." || v[0][0].Fields["field"][0] != "path" {
		t.Errorf("values %+v, want the resolved path file:. first", v)
	}
}

// Kinds the builder does not present yet are reported, never guessed: a
// shadow verdict over a half-built call would mislead. A cutting-garden
// reference needs its ancestry from cutting-garden (RFC 0018 §5.2), and a
// recipe needs just-us's catalog presentation (§10).
func TestBuildCallReportsUnbuiltKinds(t *testing.T) {
	cg := catalogsnap.ToolNode{Plugin: "cutting-garden", Server: "cutting-garden", Tool: "list_nodes"}
	if _, unbuilt := BuildCall(cg, mustContract(t), args(t, `{"uri":"caldav://x/"}`), "/", "/h"); !strings.Contains(unbuilt, "cutting-garden") {
		t.Errorf("unbuilt = %q, want it to name cutting-garden's ancestry", unbuilt)
	}
	recipe := catalogsnap.ToolNode{Plugin: "just-us", Server: "just-us", Tool: "run_recipe"}
	if _, unbuilt := BuildCall(recipe, mustContract(t), args(t, `{"recipe":"test"}`), "/", "/h"); !strings.Contains(unbuilt, "recipe") {
		t.Errorf("unbuilt = %q, want it to name the recipe kind", unbuilt)
	}
	mutate := catalogsnap.ToolNode{Plugin: "cutting-garden", Server: "cutting-garden", Tool: "mutate_nodes"}
	if _, unbuilt := BuildCall(mutate, mustContract(t), args(t, `{"groups":[]}`), "/", "/h"); unbuilt == "" {
		t.Error("mutate_nodes (op from the body) was built, want unbuilt")
	}
	unknown := catalogsnap.ToolNode{Plugin: "moxy", Server: "chrest", Tool: "read-resource"}
	if _, unbuilt := BuildCall(unknown, mustContract(t), args(t, `{"uri":"nowhere://x"}`), "/", "/h"); unbuilt == "" {
		t.Error("a reference whose scheme no server owns was built, want unbuilt")
	}
}

func surface(verb string, uri string) []ajour.Subject {
	return []ajour.Subject{
		{Type: "mcp-resources-v1", ID: "resources", Fields: map[string][]string{"op": {verb}}},
		{Type: "mcp-resource-v1", ID: uri, Fields: map[string][]string{"op": {verb}}},
	}
}

// A reference to the called server's own scheme hangs under its own
// surface, in the tool's Call, with the field's verb on every step; the
// scheme is lowercased (RFC 0018 §5.2).
func TestBuildCallReferenceUnderItsOwnSurface(t *testing.T) {
	node := catalogsnap.ToolNode{Plugin: "moxy", Server: "chrest", Tool: "read-resource"}
	calls, unbuilt := BuildCall(node, mustContract(t), args(t, `{"uri":"Chrest://items/1"}`), "/", "/h")
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].Resources, [][]ajour.Subject{surface("read", "chrest://items/1")}) || calls[0].Values != nil {
		t.Errorf("calls %+v, want one call reading chrest://items/1 under its surface", calls)
	}
}

// One door (RFC 0018 §5.1): a reference whose scheme another server owns is
// a read under that owner's surface, presented as a second Call whose Path
// ends at the owner's server.
func TestBuildCallForeignOwnerIsASecondCall(t *testing.T) {
	c := mustContract(t)
	harness := catalogsnap.ToolNode{Plugin: "clown-builtin-harness", Server: "harness", Tool: "ReadMcpResourceTool"}
	calls, unbuilt := BuildCall(harness, c, args(t, `{"server":"plugin:moxy:moxy","uri":"chrest://items/1"}`), "/", "/h")
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	owner := ajour.Call{
		Path:      []ajour.Subject{{Type: "clown-plugin-v1", ID: "moxy"}, {Type: "mcp-server-v1", ID: "chrest"}},
		Resources: [][]ajour.Subject{surface("read", "chrest://items/1")},
		Anchor:    1,
	}
	if len(calls) != 2 || calls[0].Resources != nil || calls[0].Values != nil || !reflect.DeepEqual(calls[1], owner) {
		t.Errorf("calls %+v, want the tool's Call (the route field presents nothing) and the owner's read", calls)
	}

	// A madder blob in a content field reads under moxy/moxy; any other
	// content value is an input value.
	jq := catalogsnap.ToolNode{Plugin: "moxy", Server: "jq", Tool: "jq"}
	calls, unbuilt = BuildCall(jq, c, args(t, `{"filter":".","stdin":"  madder://blobs/blake2b256-abc  "}`), "/", "/h")
	if unbuilt != "" || len(calls) != 2 || !reflect.DeepEqual(calls[1].Resources, [][]ajour.Subject{surface("read", "madder://blobs/blake2b256-abc")}) {
		t.Errorf("jq on a blob: %+v, %q", calls, unbuilt)
	}
	calls, _ = BuildCall(jq, c, args(t, `{"filter":".","stdin":"{\"a\":1}"}`), "/", "/h")
	stdinIsInput := len(calls) == 1 && slices.ContainsFunc(calls[0].Values, func(v []ajour.Subject) bool {
		return v[0].Type == "tool-input-v1" && v[0].ID == "stdin" && v[0].Fields["value"][0] == `{"a":1}`
	})
	if !stdinIsInput || calls[0].Resources != nil {
		t.Errorf("jq on text: %+v, want stdin as an input value and no resource", calls)
	}

	// §4.2: a URI in a path field is a read under its owner's surface, and
	// a scheme no server owns is a resource of the called server.
	read := catalogsnap.ToolNode{Plugin: "clown-builtin-harness", Server: "harness", Tool: "Read"}
	calls, unbuilt = BuildCall(read, c, args(t, `{"file_path":"madder://blobs/x"}`), "/", "/h")
	if unbuilt != "" || len(calls) != 2 || calls[1].Path[0].ID != "moxy" || calls[1].Path[1].ID != "moxy" {
		t.Errorf("Read of a blob: %+v, %q", calls, unbuilt)
	}
	calls, unbuilt = BuildCall(read, c, args(t, `{"file_path":"https://example.org/r.git"}`), "/", "/h")
	if unbuilt != "" || len(calls) != 1 || !reflect.DeepEqual(calls[0].Resources, [][]ajour.Subject{surface("read", "https://example.org/r.git")}) {
		t.Errorf("Read of an unowned scheme: %+v, %q; want a read under the called server's own surface", calls, unbuilt)
	}
}

// Evaluate over a tool-less owner Path (an RFC 0018 §5.1 owner Call): a
// stencil that opens the owner's surface for reads admits the pair, one that
// does not refuses it, and the verdict is the pair's.
func TestObserveFoldsTheOwnerCall(t *testing.T) {
	home := t.TempDir()
	harness := catalogsnap.ToolNode{Plugin: "clown-builtin-harness", Server: "harness", Tool: "ReadMcpResourceTool", HarnessName: "ReadMcpResourceTool"}
	input := args(t, `{"server":"plugin:moxy:moxy","uri":"chrest://items/1"}`)
	tool := "[clown-builtin-harness !clown-plugin-v1 [-> harness !mcp-server-v1 [-> ReadMcpResourceTool !mcp-tool-v1]]]"
	open := tool + "\n[moxy !clown-plugin-v1 [-> chrest !mcp-server-v1 [-> resources !mcp-resources-v1 op=[read] [-> chrest://items !mcp-resource-v1 subtree op=[read]]]]]"
	for _, tc := range []struct {
		stencil, want string
	}{{open, "admitted"}, {tool, "refused"}} {
		st, err := ajour.ParseStencil(tc.stencil)
		if err != nil {
			t.Fatal(err)
		}
		r, v := Observe([]ajour.Layer{st}, harness, mustContract(t), input, home, home)
		if r.Outcome != tc.want || v.Admitted != (tc.want == "admitted") {
			t.Errorf("stencil %q: record %+v, want %s", tc.stencil, r, tc.want)
		}
		if b, _ := json.Marshal(r); strings.Contains(string(b), "items/1") {
			t.Errorf("the record carries the reference: %s", b)
		}
	}
	// Neither a content value nor a blob reference reaches the record.
	jq := catalogsnap.ToolNode{Plugin: "moxy", Server: "jq", Tool: "jq", HarnessName: "mcp__plugin_moxy_moxy__jq_jq"}
	st, _ := ajour.ParseStencil(tool)
	for _, stdin := range []string{"secret-content-text", "madder://blobs/blake2b256-secretdigest"} {
		r, _ := Observe([]ajour.Layer{st}, jq, mustContract(t), args(t, `{"filter":".","stdin":"`+stdin+`"}`), home, home)
		if b, _ := json.Marshal(r); strings.Contains(string(b), "secret") {
			t.Errorf("the record carries a content argument: %s", b)
		}
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const engineerStencil = "[clown-builtin-harness !clown-plugin-v1 [-> harness !mcp-server-v1 [-> Read !mcp-tool-v1 [-> file:a.go !fs-path-v1] [-> _ !tool-input-v1 open]]]]"

func TestLoadLayers(t *testing.T) {
	dir := t.TempDir()
	if layers, err := LoadLayers(filepath.Join(dir, "absent.json")); err != nil || layers != nil {
		t.Fatalf("absent file: %v, %v; want no layers, no error", layers, err)
	}
	st, err := ajour.ParseStencil(engineerStencil)
	if err != nil {
		t.Fatal(err)
	}
	stencil := writeFile(t, dir, "s.espalier", "---\n- role = engineer-x\n! espalier-profile-v1\n---\n\n"+engineerStencil)
	patch := writeFile(t, dir, "p.espalier", "[clown-builtin-harness [-> harness [-> Read [-> file:secret !fs-path-v1 deny]]]]")
	entries := func(digest string) string {
		b, _ := json.Marshal([]LayerEntry{
			{Path: stencil, Role: RoleStencil, Digest: digest},
			{Path: patch, Role: RolePatch},
		})
		return string(b)
	}
	good := writeFile(t, dir, "good.json", entries(st.Digest()))
	layers, err := LoadLayers(good)
	if err != nil || len(layers) != 2 {
		t.Fatalf("LoadLayers = %d layers, %v; want 2, nil", len(layers), err)
	}
	purposeful := writeFile(t, dir, "purpose.json", entries(ajour.PurposeStencilDigest+"@"+st.Digest()))
	if _, err := LoadLayers(purposeful); err != nil {
		t.Errorf("a purpose-full digest: %v", err)
	}
	bad := writeFile(t, dir, "bad.json", entries("blake2b256-93wmjxtet0p42hxueuem6a7f3x3g6km5gnrd5yjcc27rugdy7v2qneuhc4"))
	if _, err := LoadLayers(bad); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("a digest mismatch: %v, want a digest error", err)
	}
	role := writeFile(t, dir, "role.json", `[{"path":"`+stencil+`","role":"base","digest":"x"}]`)
	if _, err := LoadLayers(role); err == nil {
		t.Error("an unknown role loaded")
	}
}

// End to end over the recorder: a refused read is recorded with its verdict
// and deciding layer, never with the call's arguments.
func TestObserveRecordsVerdictWithoutArguments(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	st, err := ajour.ParseStencil(engineerStencil)
	if err != nil {
		t.Fatal(err)
	}
	rec := Recorder{Path: filepath.Join(t.TempDir(), "shadow.jsonl")}
	node := catalogsnap.ToolNode{Plugin: "clown-builtin-harness", Server: "harness", Tool: "Read", Kind: "read", HarnessName: "Read"}
	secret := "very-private-name.txt"
	r, _ := Observe([]ajour.Layer{st}, node, mustContract(t), args(t, `{"file_path":"`+secret+`"}`), home, home)
	if r.Outcome != "refused" || r.Deciding == "" {
		t.Fatalf("record %+v, want refused with a deciding layer", r)
	}
	if err := rec.Append(r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Errorf("the record carries an argument: %s", b)
	}
	admitted, v := Observe([]ajour.Layer{st}, node, mustContract(t), args(t, `{"file_path":"a.go"}`), home, home)
	if admitted.Outcome != "admitted" || !v.Admitted {
		t.Errorf("a.go: %+v, want admitted", admitted)
	}
}

// hookSession lays out one session's state under a temporary
// XDG_STATE_HOME: a claude-code snapshot (so the harness built-ins resolve)
// and, when withLayers, the engineer stencil as its one layer.
func hookSession(t *testing.T, principal string, withLayers bool) (home, dir string) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	home = t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	snapDir, err := catalogsnap.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalogsnap.Write(snapDir, catalogsnap.Snapshot{Principal: principal, Harness: catalogsnap.HarnessClaudeCode}); err != nil {
		t.Fatal(err)
	}
	dir, err = Dir(principal)
	if err != nil {
		t.Fatal(err)
	}
	if !withLayers {
		return home, dir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := ajour.ParseStencil(engineerStencil)
	if err != nil {
		t.Fatal(err)
	}
	stencil := writeFile(t, dir, "engineer.espalier", engineerStencil)
	b, _ := json.Marshal([]LayerEntry{{Path: stencil, Role: RoleStencil, Digest: st.Digest()}})
	writeFile(t, dir, LayersFile, string(b))
	return home, dir
}

func shadowLines(t *testing.T, dir string) []Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ShadowFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// The hook end to end in shadow: it records an admitted and a refused call
// and denies neither.
func TestHookRecordsAndNeverDeniesInShadow(t *testing.T) {
	home, dir := hookSession(t, "p-1", true)
	for _, file := range []string{"a.go", "secret.txt"} {
		if d := Hook("p-1", "Read", args(t, `{"file_path":"`+file+`"}`), home, home); d.Deny {
			t.Errorf("%s: shadow denied: %+v", file, d)
		}
	}
	rs := shadowLines(t, dir)
	if len(rs) != 2 || rs[0].Outcome != "admitted" || rs[1].Outcome != "refused" || rs[1].Mode != "shadow" {
		t.Errorf("records %+v, want admitted then refused, in shadow", rs)
	}
}

// No layers file is no profile: the hook records nothing and writes nothing.
func TestHookWithoutLayersWritesNothing(t *testing.T) {
	home, dir := hookSession(t, "p-2", false)
	if d := Hook("p-2", "Read", args(t, `{"file_path":"a.go"}`), home, home); d.Deny {
		t.Errorf("denied without a profile: %+v", d)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("%s exists; nothing may be written without a layers file", dir)
	}
}

// A tool the session's catalog does not name is recorded as unresolved.
func TestHookRecordsAnUnresolvedTool(t *testing.T) {
	home, dir := hookSession(t, "p-3", true)
	Hook("p-3", "mcp__plugin_nowhere_x__y", args(t, `{}`), home, home)
	if rs := shadowLines(t, dir); len(rs) != 1 || rs[0].Outcome != OutcomeUnresolved || rs[0].HarnessName != "mcp__plugin_nowhere_x__y" {
		t.Errorf("records %+v, want one unresolved", rs)
	}
}

// The record is capped: past MaxBytes it rotates to one older generation, so
// a session's shadow log never holds more than twice the cap.
func TestRecorderRotatesAtTheCap(t *testing.T) {
	dir := t.TempDir()
	rec := Recorder{Path: filepath.Join(dir, "shadow.jsonl"), MaxBytes: 200}
	for range 20 {
		if err := rec.Append(Record{Outcome: "admitted", HarnessName: strings.Repeat("x", 40)}); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := os.Stat(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(rec.Path + ".1")
	if err != nil {
		t.Fatalf("no rotated generation: %v", err)
	}
	if cur.Size() > 200 || old.Size() > 200 {
		t.Errorf("sizes %d and %d, want each at most the 200-byte cap", cur.Size(), old.Size())
	}
	if _, err := os.Stat(rec.Path + ".2"); err == nil {
		t.Error("a second rotated generation exists; the cap allows one")
	}
}
