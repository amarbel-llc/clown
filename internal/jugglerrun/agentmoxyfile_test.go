package jugglerrun

import (
	"context"
	"errors"
	"fmt"
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
	// infraErr, when set, is returned from every ResolveToolName call
	// regardless of the name asked for — an infrastructure failure (moxy
	// itself not runnable), not a per-tool one.
	infraErr error
}

func (fakeResolveMoxyCLI) Path() string { panic("not used by DeriveAgentMoxyfile") }
func (fakeResolveMoxyCLI) ShowEffectiveMoxyfile(context.Context, []string, string) ([]byte, error) {
	panic("not used by DeriveAgentMoxyfile")
}

func (fakeResolveMoxyCLI) Narrows(context.Context, []string, string, string) (NarrowsResult, error) {
	panic("not used by DeriveAgentMoxyfile")
}

func (f fakeResolveMoxyCLI) ResolveToolName(_ context.Context, _ []string, _ string, rendered string) (string, string, error) {
	if f.infraErr != nil {
		return "", "", f.infraErr
	}
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

// clown#261 review LOW 6: a brief whose only tools entry is
// cannot_complete still gets a derived allow-tools, explicitly empty —
// never omitted, which moxy would read as no allowlist at all (everything
// held at use).
func TestDeriveAgentMoxyfile_CannotCompleteOnlyStillEmitsEmptyAllowTools(t *testing.T) {
	b := &jugglerbrief.Brief{Tools: []string{"cannot_complete"}}
	cli := fakeResolveMoxyCLI{}

	out, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	if err != nil {
		t.Fatalf("DeriveAgentMoxyfile: %v", err)
	}
	doc := decodedMoxyfile(t, out)
	if got := stringSlice(t, doc["allow-tools"]); len(got) != 0 {
		t.Errorf("allow-tools = %v, want an explicit empty array", got)
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

// clown#261 review LOW 5: when the brief's own moxyfile sets allow-tools,
// the REAL resolved name must be covered by it too — catching a gap
// Validate's own textual heuristic can miss for a tools entry with more
// than one underscore (moxy's structural inverse always splits on the
// FIRST separator; a bare allow entry like "foo_bar" reads, under
// Validate's heuristic, as covering tool "x" of server "foo_bar", while
// the real resolved identity is server "foo", tool "bar_x").
func TestDeriveAgentMoxyfile_RequiresBriefAllowToolsToCoverTheRealResolution(t *testing.T) {
	b := &jugglerbrief.Brief{
		Tools:    []string{"foo_bar_x"},
		Moxyfile: `allow-tools = ["foo_bar"]`,
	}
	cli := fakeResolveMoxyCLI{resolved: map[string][2]string{"foo_bar_x": {"foo", "bar_x"}}}

	_, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	var mismatch *ErrToolCoverageMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *ErrToolCoverageMismatch", err)
	}
	if mismatch.Tool != "foo_bar_x" || mismatch.Resolved != "foo.bar_x" {
		t.Errorf("mismatch = %+v", mismatch)
	}
}

// The same brief-set allow-tools, when it DOES cover the real resolved
// name (moxy's own bare-server-covers-dotted-entry rule), succeeds.
func TestDeriveAgentMoxyfile_BriefAllowToolsCoveringTheRealResolutionSucceeds(t *testing.T) {
	b := &jugglerbrief.Brief{
		Tools:    []string{"ring_create_issue"},
		Moxyfile: `allow-tools = ["ring"]`,
	}
	cli := fakeResolveMoxyCLI{resolved: map[string][2]string{"ring_create_issue": {"ring", "create_issue"}}}

	out, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	if err != nil {
		t.Fatalf("DeriveAgentMoxyfile: %v", err)
	}
	doc := decodedMoxyfile(t, out)
	if got := stringSlice(t, doc["allow-tools"]); len(got) != 1 || got[0] != "ring.create_issue" {
		t.Errorf("allow-tools = %v", got)
	}
}

// clown#261 point 3: a tools entry moxy's render can't resolve refuses
// rather than falling back to the rendered name (which moxy would read as
// a bare, nonexistent server, silently allowing nothing) or dropping it.
func TestDeriveAgentMoxyfile_UnresolvableToolIsErrToolResolution(t *testing.T) {
	b := &jugglerbrief.Brief{Tools: []string{"ring_create_issue", "ring_archive"}}
	cli := fakeResolveMoxyCLI{
		resolved: map[string][2]string{"ring_create_issue": {"ring", "create_issue"}},
		fail:     map[string]bool{"ring_archive": true},
	}

	_, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	var unresolved *ErrToolResolution
	if !errors.As(err, &unresolved) {
		t.Fatalf("err = %v, want *ErrToolResolution", err)
	}
	if unresolved.Tool != "ring_archive" {
		t.Errorf("unresolved.Tool = %q, want %q", unresolved.Tool, "ring_archive")
	}
}

// An infrastructure failure (moxy not runnable, a cancelled ctx) is
// returned bare, never blamed on the first tool iterated as an
// *ErrToolResolution.
func TestDeriveAgentMoxyfile_InfrastructureFailureIsNotErrToolResolution(t *testing.T) {
	b := &jugglerbrief.Brief{Tools: []string{"ring_create_issue"}}
	underlying := &CommandError{Argv: []string{"moxy", "render"}, ExitCode: -1, Err: errors.New("fork/exec /nonexistent/moxy: no such file or directory")}
	cli := fakeResolveMoxyCLI{infraErr: fmt.Errorf("resolving the agent's tools needs moxy, and %q could not be run: %w", "/nonexistent/moxy", underlying)}

	_, err := DeriveAgentMoxyfile(context.Background(), cli, nil, "{server}_{tool}", b)
	var unresolved *ErrToolResolution
	if errors.As(err, &unresolved) {
		t.Fatalf("err = %v, want a bare infrastructure error, not *ErrToolResolution", err)
	}
	var ce *CommandError
	if !errors.As(err, &ce) || ce.ExitCode != -1 {
		t.Fatalf("err = %v, want the underlying CommandError preserved", err)
	}
}

func TestDeriveAgentMoxyfile_CancelledContextIsNotErrToolResolution(t *testing.T) {
	b := &jugglerbrief.Brief{Tools: []string{"ring_create_issue"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cli := fakeResolveMoxyCLI{infraErr: errors.New("fake: would not normally reach here")}

	_, err := DeriveAgentMoxyfile(ctx, cli, nil, "{server}_{tool}", b)
	var unresolved *ErrToolResolution
	if errors.As(err, &unresolved) {
		t.Fatalf("err = %v, want a bare context error, not *ErrToolResolution", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
