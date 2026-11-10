package catalogcontract

import (
	"slices"
	"testing"
)

func mustLoad(t *testing.T) Contract {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

// The node types RFC 0018 §1 declares, in its order. A type added to the
// RFC is added here and to catalog.json together.
var rfcNodeTypes = []string{
	"clown-plugin-v1",
	"mcp-server-v1",
	"mcp-tool-v1",
	"just-recipe-v1",
	"recipe-arg-v1",
	"tool-input-v1",
	"fs-path-v1",
	"mcp-resources-v1",
	"mcp-resource-v1",
	"cg-root-v1",
	"cg-field-v1",
}

func TestNodeTypesMatchTheRFC(t *testing.T) {
	c := mustLoad(t)
	var got []string
	for _, nt := range c.NodeTypes {
		got = append(got, nt.Type)
	}
	if !slices.Equal(got, rfcNodeTypes) {
		t.Fatalf("node types:\n got  %v\n want %v", got, rfcNodeTypes)
	}
}

func TestOnlyResourceStepsCarryVerbs(t *testing.T) {
	c := mustLoad(t)
	resourceTypes := []string{"mcp-resources-v1", "mcp-resource-v1", "cg-root-v1", "cg-field-v1"}
	for _, nt := range c.NodeTypes {
		hasOp := slices.Contains(nt.Fields, "op")
		if hasOp != slices.Contains(resourceTypes, nt.Type) {
			t.Errorf("%s: carries op = %v", nt.Type, hasOp)
		}
	}
}

func TestEveryFieldKindAndVerbIsDeclared(t *testing.T) {
	c := mustLoad(t)
	for _, tool := range c.Tools {
		for _, f := range tool.Fields {
			if !slices.Contains(Kinds, f.Kind) {
				t.Errorf("%s: field %s has undeclared kind %q", tool.Key(), f.Name, f.Kind)
			}
			if f.Op != "" && !slices.Contains(ResourceVerbs, f.Op) && f.Op != OpFromBody {
				t.Errorf("%s: field %s has undeclared op %q", tool.Key(), f.Name, f.Op)
			}
			if f.Op != "" && f.Kind != KindReference {
				t.Errorf("%s: field %s carries op on a %s field", tool.Key(), f.Name, f.Kind)
			}
			if f.Resolve != "" && !slices.Contains(Resolutions, f.Resolve) {
				t.Errorf("%s: field %s has undeclared resolve %q", tool.Key(), f.Name, f.Resolve)
			}
		}
	}
}

// One kind per field: a field named twice could be presented two ways.
func TestOneKindPerField(t *testing.T) {
	c := mustLoad(t)
	for _, tool := range c.Tools {
		seen := map[string]bool{}
		for _, f := range tool.Fields {
			if seen[f.Name] {
				t.Errorf("%s: field %s declared twice", tool.Key(), f.Name)
			}
			seen[f.Name] = true
		}
	}
}

func TestToolsAreUnique(t *testing.T) {
	c := mustLoad(t)
	seen := map[string]bool{}
	for _, tool := range c.Tools {
		if seen[tool.Key()] {
			t.Errorf("tool %s declared twice", tool.Key())
		}
		seen[tool.Key()] = true
	}
}

func TestToolKindsAreDeclared(t *testing.T) {
	c := mustLoad(t)
	for _, tool := range c.Tools {
		if tool.Kind != "" && !slices.Contains(ToolKinds, tool.Kind) {
			t.Errorf("%s: undeclared tool kind %q", tool.Key(), tool.Kind)
		}
		if tool.Presents != "" && !slices.Contains(Presentations, tool.Presents) {
			t.Errorf("%s: undeclared presents %q", tool.Key(), tool.Presents)
		}
	}
}

// relative_to names the repository root or a path field of the same tool.
func TestRelativeToNamesAPathField(t *testing.T) {
	c := mustLoad(t)
	for _, tool := range c.Tools {
		for _, f := range tool.Fields {
			if f.RelativeTo == "" || f.RelativeTo == RelativeToRepoRoot {
				continue
			}
			base := c.Field(tool.Plugin, tool.Server, tool.Tool, f.RelativeTo)
			if base.Kind != KindPath {
				t.Errorf("%s: %s is relative to %s, which is not a path field", tool.Key(), f.Name, f.RelativeTo)
			}
		}
	}
}

func TestFieldKindPins(t *testing.T) {
	c := mustLoad(t)
	cases := []struct {
		plugin, server, tool, field string
		kind, resolve, op           string
	}{
		{"clown-builtin-harness", "harness", "Read", "file_path", KindPath, "", ""},
		{"clown-builtin-harness", "harness", "Grep", "path", KindPath, ResolveCwd, ""},
		{"clown-builtin-harness", "harness", "Bash", "command", KindInput, "", ""},
		{"clown-builtin-harness", "harness", "WebFetch", "url", KindInput, "", ""},
		{"clown-builtin-harness", "harness", "ReadMcpResourceTool", "uri", KindReference, "", "read"},
		{"clown-builtin-harness", "harness", "ReadMcpResourceTool", "server", KindRoute, "", ""},
		{"moxy", "folio", "cp", "source", KindPath, "", ""},
		{"moxy", "folio", "cp", "destination", KindPath, "", ""},
		{"moxy", "folio", "read", "limit", KindInput, "", ""},
		{"moxy", "jq", "jq", "stdin", KindContent, "", ""},
		{"moxy", "smith", "issue-list", "repo", KindInput, ResolveCwdRemote, ""},
		{"cutting-garden", "cutting-garden", "mutate_nodes", "groups", KindReference, "", OpFromBody},
		{"cutting-garden", "cutting-garden", "list_nodes", "uri", KindReference, "", "read"},
		{"just-us", "just-us", "run_recipe", "recipe", KindRecipe, "", ""},
		{"spinclass", "spinclass", "grit_branch", "repo_path", KindPath, ResolveSessionWorktree, ""},
		{"nobody", "nothing", "never", "x", KindInput, "", ""},
	}
	for _, tc := range cases {
		f := c.Field(tc.plugin, tc.server, tc.tool, tc.field)
		if f.Kind != tc.kind || f.Resolve != tc.resolve || f.Op != tc.op {
			t.Errorf("%s/%s/%s.%s: got kind=%q resolve=%q op=%q, want kind=%q resolve=%q op=%q",
				tc.plugin, tc.server, tc.tool, tc.field,
				f.Kind, f.Resolve, f.Op, tc.kind, tc.resolve, tc.op)
		}
	}
}

func TestHarnessResourceToolsPresentNoToolStep(t *testing.T) {
	c := mustLoad(t)
	for _, name := range []string{"ReadMcpResourceTool", "ListMcpResourcesTool"} {
		tool, ok := c.Tool("clown-builtin-harness", "harness", name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if tool.Presents != PresentsOwnerResourceRead {
			t.Errorf("%s presents %q", name, tool.Presents)
		}
	}
}

func TestSchemeOwners(t *testing.T) {
	c := mustLoad(t)
	for scheme, want := range map[string][2]string{
		"madder":   {"moxy", "moxy"},
		"chrest":   {"moxy", "chrest"},
		"nebulous": {"moxy", "nebulous"},
		"clown":    {"clown-builtin-diagnostics", "disconnects"},
		"caldav":   {"cutting-garden", "cutting-garden"},
	} {
		s, ok := c.SchemeOwner(scheme)
		if !ok || s.Plugin != want[0] || s.Server != want[1] {
			t.Errorf("SchemeOwner(%q) = %+v, %v; want %v", scheme, s, ok, want)
		}
	}
	if _, ok := c.SchemeOwner("https"); ok {
		t.Errorf("https must have no owner (RFC 0018 §4.2, §7)")
	}
}

func TestRawIsTheEmbeddedTable(t *testing.T) {
	if len(Raw()) == 0 {
		t.Fatal("Raw is empty")
	}
}
