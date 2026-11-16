package ajourshadow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	call, unbuilt := BuildCall(node, mustContract(t), args(t, `{"file_path":"a.go","limit":5,"offset":1.5}`), home, home)
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	want := ajour.Call{
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
	}
	if !reflect.DeepEqual(call, want) {
		t.Errorf("BuildCall =\n%+v\nwant\n%+v", call, want)
	}
}

// RFC 0018 §4.3: a top-level array presents one subject per element, an
// empty array one subject whose value is [].
func TestBuildCallArrays(t *testing.T) {
	node := catalogsnap.ToolNode{Plugin: "example", Server: "example", Tool: "unlisted"} // every field is input
	call, unbuilt := BuildCall(node, mustContract(t), args(t, `{"subcommand":"status","flags":["a","b"],"none":[]}`), "/", "/nonexistent-home")
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	var got []string
	for _, v := range call.Values {
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
	call, unbuilt := BuildCall(node, mustContract(t), args(t, `{"pattern":"*.go"}`), home, home)
	if unbuilt != "" {
		t.Fatalf("unbuilt: %s", unbuilt)
	}
	if len(call.Values) != 2 || call.Values[0][0].ID != "file:." || call.Values[0][0].Fields["field"][0] != "path" {
		t.Errorf("values %+v, want the resolved path file:. first", call.Values)
	}
}

// Kinds the builder does not present yet are reported, never guessed: a
// shadow verdict over a half-built call would mislead.
func TestBuildCallReportsUnbuiltKinds(t *testing.T) {
	node := catalogsnap.ToolNode{Plugin: "cutting-garden", Server: "cutting-garden", Tool: "list_nodes"}
	if _, unbuilt := BuildCall(node, mustContract(t), args(t, `{"uri":"caldav://x/"}`), "/", "/h"); !strings.Contains(unbuilt, "reference") {
		t.Errorf("unbuilt = %q, want it to name the reference kind", unbuilt)
	}
	read := catalogsnap.ToolNode{Plugin: "clown-builtin-harness", Server: "harness", Tool: "Read"}
	if _, unbuilt := BuildCall(read, mustContract(t), args(t, `{"file_path":"madder://blobs/x"}`), "/", "/h"); unbuilt == "" {
		t.Error("a URI in a path field (§4.2) was built, want unbuilt")
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
	r := Observe([]ajour.Layer{st}, node, mustContract(t), args(t, `{"file_path":"`+secret+`"}`), home, home)
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
	admitted := Observe([]ajour.Layer{st}, node, mustContract(t), args(t, `{"file_path":"a.go"}`), home, home)
	if admitted.Outcome != "admitted" {
		t.Errorf("a.go: %+v, want admitted", admitted)
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
