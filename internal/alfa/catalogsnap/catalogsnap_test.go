package catalogsnap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
)

const folioReadSchema = `{"type":"object","properties":{"file_path":{"type":"string"},"start":{"type":"integer"}},"required":["file_path"]}`

// fixture is one launched session's servers as tools/list reported them.
func fixture() Snapshot {
	return Snapshot{
		Principal: "p-1",
		Harness:   HarnessClaudeCode,
		Servers: []Server{
			{Plugin: "moxy", Server: "moxy", Tools: []Tool{
				{Name: "folio.read", Description: "Read a file. [perms: dynamic]", InputSchema: json.RawMessage(folioReadSchema)},
				{Name: "smith.issue-list", Description: "Search issues [perms: always-allow]", InputSchema: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`)},
				{Name: "smith.pr-merge", Description: "Merge [perms: each-use]", InputSchema: json.RawMessage(`{"type":"object"}`), Meta: json.RawMessage(`{"kind":"write"}`)},
				{Name: "status", Description: "Show servers", InputSchema: json.RawMessage(`{"type":"object"}`), Meta: json.RawMessage(`{"kind":"list"}`)},
				{Name: "async", Description: "Dispatch one call", InputSchema: json.RawMessage(`{"type":"object"}`)},
			}},
			{Plugin: "just-us", Server: "just-us", Tools: []Tool{
				{Name: "run_recipe", InputSchema: json.RawMessage(`{"type":"object","properties":{"recipe":{"type":"string"},"args":{"type":"array"},"timeout":{"type":"string"}}}`)},
			}},
			{Plugin: "cutting-garden", Server: "cutting-garden", Tools: []Tool{
				{Name: "list_nodes", InputSchema: json.RawMessage(`{"type":"object","properties":{"uri":{"type":"string"}}}`)},
			}},
		},
	}
}

func contract(t *testing.T) catalogcontract.Contract {
	t.Helper()
	c, err := catalogcontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func find(ts []ToolNode, path string) (ToolNode, bool) {
	for _, n := range ts {
		if n.Plugin+"/"+n.Server+"/"+n.Tool == path {
			return n, true
		}
	}
	return ToolNode{}, false
}

// A profile matches by string, so the listed ids must be byte-equal to the
// spellings FDR 0028's sample profiles and RFC 0018's table use.
func TestPresentedIDsAreTheSpellingsProfilesMatch(t *testing.T) {
	got := Present(fixture(), contract(t))
	for _, want := range []string{
		"moxy/folio/read", "moxy/smith/issue-list", "moxy/smith/pr-merge", "moxy/moxy/status",
		"just-us/just-us/run_recipe", "cutting-garden/cutting-garden/list_nodes",
		"clown-builtin-harness/harness/Read", "clown-builtin-harness/harness/Bash",
	} {
		if _, ok := find(got, want); !ok {
			t.Errorf("%s not presented", want)
		}
	}
	for _, n := range got {
		if strings.Contains(n.Tool, "__") || strings.HasPrefix(n.Tool, "mcp_") {
			t.Errorf("%s/%s/%s: a harness-prefixed tool name", n.Plugin, n.Server, n.Tool)
		}
	}
}

// Wrappers (moxy async, batch) and the harness's resource tools present no
// tool step of their own (RFC 0018 §3, §7), so they are not catalog nodes.
func TestToolsThatPresentNoToolStepAreNotListed(t *testing.T) {
	got := Present(fixture(), contract(t))
	for _, absent := range []string{
		"moxy/moxy/async",
		"clown-builtin-harness/harness/ReadMcpResourceTool",
		"clown-builtin-harness/harness/ListMcpResourcesTool",
	} {
		if _, ok := find(got, absent); ok {
			t.Errorf("%s listed; it presents no tool step", absent)
		}
	}
}

func TestPermComesFromThePermsSuffixAndKindOnlyFromAToolKind(t *testing.T) {
	got := Present(fixture(), contract(t))
	for path, want := range map[string][2]string{
		"moxy/folio/read":                    {"", PermDynamic},
		"moxy/smith/issue-list":              {"", PermAllow},
		"moxy/smith/pr-merge":                {"write", PermAsk},
		"moxy/moxy/status":                   {"", ""}, // moxy's _meta.kind "list" is not a tool kind
		"just-us/just-us/run_recipe":         {"", ""},
		"clown-builtin-harness/harness/Read": {"read", ""},
	} {
		n, ok := find(got, path)
		if !ok {
			t.Fatalf("%s not presented", path)
		}
		if n.Kind != want[0] || n.Perm != want[1] {
			t.Errorf("%s: kind %q perm %q, want kind %q perm %q", path, n.Kind, n.Perm, want[0], want[1])
		}
	}
}

func TestInputsAreTheSchemaPropertiesWithTheirFieldKinds(t *testing.T) {
	got := Present(fixture(), contract(t))
	for path, want := range map[string]string{
		"moxy/folio/read":                          "file_path=path start=input",
		"just-us/just-us/run_recipe":               "args=recipe recipe=recipe timeout=input",
		"cutting-garden/cutting-garden/list_nodes": "uri=reference",
		"clown-builtin-harness/harness/Read":       "file_path=path",
	} {
		n, _ := find(got, path)
		var parts []string
		for _, in := range n.Inputs {
			parts = append(parts, in.Name+"="+in.FieldKind)
		}
		if s := strings.Join(parts, " "); s != want {
			t.Errorf("%s inputs %q, want %q", path, s, want)
		}
	}
}

func TestBuiltinsAreListedOnlyForTheClaudeCodeHarness(t *testing.T) {
	s := fixture()
	s.Harness = "juggler-run"
	for _, n := range Present(s, contract(t)) {
		if n.Plugin == HarnessPlugin {
			t.Fatalf("%s/%s listed for harness %s", n.Server, n.Tool, s.Harness)
		}
	}
}

func TestWriteThenReadRoundTripsPrivatelyAndListSkipsJunk(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "catalog")
	s := fixture()
	if err := Write(dir, s); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName(s.Principal)))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode %v, want 0600", fi.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	all := List(dir)
	if len(all) != 1 || all[0].Principal != "p-1" || len(all[0].Servers) != 3 {
		t.Fatalf("List = %+v, want the one snapshot", all)
	}
	if all[0].Version != Version || all[0].WrittenAt == "" {
		t.Errorf("Write did not stamp version and time: %+v", all[0])
	}
}

// The snapshot stores names, schemas and _meta only: never a server's URL,
// args, env or the tokens they carry (#292).
func TestTheSnapshotHasNoFieldForURLsEnvOrArgs(t *testing.T) {
	b, err := json.Marshal(Snapshot{Principal: "p", Servers: []Server{{Plugin: "x", Server: "y", Tools: []Tool{{Name: "t"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"url"`, `"env"`, `"args"`, `"command"`, `"address"`} {
		if strings.Contains(string(b), key) {
			t.Errorf("snapshot JSON carries %s", key)
		}
	}
}
