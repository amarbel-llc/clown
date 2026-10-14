package jugglerrun

import (
	"context"
	"errors"
	"testing"

	"github.com/BurntSushi/toml"

	"code.linenisgreat.com/clown/internal/jugglerbrief"
)

// fakeResolveMoxyCLI implements only ResolveToolName (a static rendered ->
// server/tool map, or a forced failure); DeriveAgentMoxyfile never calls
// the others, so they panic if it ever did.
type fakeResolveMoxyCLI struct {
	resolved map[string][2]string
	fail     map[string]bool
}

func (fakeResolveMoxyCLI) Path() string { panic("not used by DeriveAgentMoxyfile") }
func (fakeResolveMoxyCLI) ShowEffectiveMoxyfile(context.Context, []string, string) ([]byte, error) {
	panic("not used by DeriveAgentMoxyfile")
}

func (fakeResolveMoxyCLI) Narrows(context.Context, []string, string, string) (NarrowsResult, error) {
	panic("not used by DeriveAgentMoxyfile")
}

func (f fakeResolveMoxyCLI) ResolveToolName(_ context.Context, _ []string, _ string, rendered string) (string, string, error) {
	if f.fail[rendered] {
		return "", "", errors.New("fake: unresolvable")
	}
	st, ok := f.resolved[rendered]
	if !ok {
		return "", "", errors.New("fake: no mapping configured for " + rendered)
	}
	return st[0], st[1], nil
}

func decodedMoxyfile(t *testing.T, s string) map[string]any {
	t.Helper()
	doc := map[string]any{}
	if _, err := toml.Decode(s, &doc); err != nil {
		t.Fatalf("decoding derived moxyfile: %v\n%s", err, s)
	}
	return doc
}

func stringSlice(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("not an array: %#v", v)
	}
	out := make([]string, len(raw))
	for i, e := range raw {
		out[i] = e.(string)
	}
	return out
}

// clown#261 point 3: allow-tools is always the canonical dotted form of
// brief.Tools, overriding whatever the brief's own moxyfile set, while
// every other key (pass-tools above all) is kept.
func TestDeriveAgentMoxyfile_OverridesAllowToolsKeepsEverythingElse(t *testing.T) {
	b := &jugglerbrief.Brief{
		Tools: []string{"ring_create_issue"},
		Moxyfile: `allow-tools = ["ring"]
pass-tools = ["other.tool"]
disable-moxins = ["shell"]
`,
	}
	cli := fakeResolveMoxyCLI{resolved: map[string][2]string{"ring_create_issue": {"ring", "create_issue"}}}

	out, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	if err != nil {
		t.Fatalf("DeriveAgentMoxyfile: %v", err)
	}
	doc := decodedMoxyfile(t, out)
	if got := stringSlice(t, doc["allow-tools"]); len(got) != 1 || got[0] != "ring.create_issue" {
		t.Errorf("allow-tools = %v, want [\"ring.create_issue\"] (overridden, not the brief's own [\"ring\"])", got)
	}
	if got := stringSlice(t, doc["pass-tools"]); len(got) != 1 || got[0] != "other.tool" {
		t.Errorf("pass-tools = %v, want kept as [\"other.tool\"]", got)
	}
	if got := stringSlice(t, doc["disable-moxins"]); len(got) != 1 || got[0] != "shell" {
		t.Errorf("disable-moxins = %v, want kept as [\"shell\"]", got)
	}
}

// cannot_complete is juggler's own injected pseudo-tool, never a real moxy
// tool: it must never be sent to ResolveToolName (which has no mapping for
// it here and would error) and never appears in the derived allow-tools.
func TestDeriveAgentMoxyfile_SkipsCannotComplete(t *testing.T) {
	b := &jugglerbrief.Brief{Tools: []string{"ring_create_issue", "cannot_complete"}}
	cli := fakeResolveMoxyCLI{resolved: map[string][2]string{"ring_create_issue": {"ring", "create_issue"}}}

	out, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	if err != nil {
		t.Fatalf("DeriveAgentMoxyfile: %v", err)
	}
	doc := decodedMoxyfile(t, out)
	if got := stringSlice(t, doc["allow-tools"]); len(got) != 1 || got[0] != "ring.create_issue" {
		t.Errorf("allow-tools = %v, want only [\"ring.create_issue\"], cannot_complete excluded", got)
	}
}

// A brief with no moxyfile at all still gets a derived allow-tools and
// nothing else.
func TestDeriveAgentMoxyfile_EmptyMoxyfile(t *testing.T) {
	b := &jugglerbrief.Brief{Tools: []string{"ring_create_issue"}}
	cli := fakeResolveMoxyCLI{resolved: map[string][2]string{"ring_create_issue": {"ring", "create_issue"}}}

	out, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	if err != nil {
		t.Fatalf("DeriveAgentMoxyfile: %v", err)
	}
	doc := decodedMoxyfile(t, out)
	if len(doc) != 1 {
		t.Errorf("doc = %#v, want only allow-tools", doc)
	}
	if got := stringSlice(t, doc["allow-tools"]); len(got) != 1 || got[0] != "ring.create_issue" {
		t.Errorf("allow-tools = %v", got)
	}
}

// clown#261 point 2: a tools entry moxy's render can't resolve refuses
// rather than falling back to the rendered name (which moxy would read as
// a bare, nonexistent server, silently allowing nothing) or dropping it.
func TestDeriveAgentMoxyfile_UnresolvableToolIsErrToolResolution(t *testing.T) {
	b := &jugglerbrief.Brief{Tools: []string{"ring_create_issue", "status"}}
	cli := fakeResolveMoxyCLI{
		resolved: map[string][2]string{"ring_create_issue": {"ring", "create_issue"}},
		fail:     map[string]bool{"status": true},
	}

	_, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	var unresolved *ErrToolResolution
	if !errors.As(err, &unresolved) {
		t.Fatalf("err = %v, want *ErrToolResolution", err)
	}
	if unresolved.Tool != "status" {
		t.Errorf("unresolved.Tool = %q, want %q", unresolved.Tool, "status")
	}
}
