package jugglerrun

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"code.linenisgreat.com/clown/internal/jugglerbrief"
	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// ErrToolResolution is a brief refusal (clown#261 point 2): moxy's own
// `render --resolve` could not turn an advertised juggler tool name back
// into its canonical "server.tool" pair, so the agent's own allow-tools
// cannot be derived safely. Never silently falls back to the rendered name
// (which moxy would read as a bare, nonexistent server, allowing nothing)
// or drops the entry — a broken resolution never passes, the same
// discipline checkBriefNarrows already applies to a broken narrowing check.
type ErrToolResolution struct {
	// Tool is the brief's tools entry that could not be resolved.
	Tool string
	// Detail is the underlying reason: a non-zero exit, or unparsable
	// output (no tab-separated server/tool half).
	Detail string
}

func (e *ErrToolResolution) Error() string {
	return fmt.Sprintf("resolving tools entry %q to its canonical server.tool name: %s", e.Tool, e.Detail)
}

// ToolResolutionRefusalEntry is the run ledger's record of a spawn refused
// pre-mint because a brief's tools entry could not be canonicalized
// (clown#261), auditable without a child record — the same shape
// NarrowsRefusalEntry and SystemdRefusalEntry use for their own refusals.
func ToolResolutionRefusalEntry(e *ErrToolResolution) RunLedgerEntry {
	return RunLedgerEntry{
		Tool: "tool-resolve", Kind: "refusal", OK: false, URIs: []string{},
		Reason: e.Error(),
	}
}

// DeriveAgentMoxyfile computes the moxyfile the agent's own moxy must
// actually load (clown#261 point 3): b.Moxyfile, with "allow-tools" always
// replaced by the canonical dotted form of b.Tools ("server.tool", moxy's
// own vocabulary regardless of serving template — see ResolveToolName).
// Overriding rather than intersecting the brief's own allow-tools (when it
// sets one) is the narrowest outcome: Validate already guarantees the
// brief's own allow-tools, when set, covers every tools entry, so the
// derived set is never wider than what the brief itself already claimed.
// Every other key — pass-tools above all, also disable-moxins, [[servers]],
// [credentials], ... — is kept exactly as the brief wrote it; Validate
// separately refuses a brief whose pass-tools would collide with the
// derived allow-tools (checkPassToolsDoNotCoverTools). cannot_complete is
// never resolved: it is juggler's own injected pseudo-tool, not a tool
// moxy serves at all. A tools entry ResolveToolName can't resolve is
// *ErrToolResolution, naming it: this must run, and must be checked,
// before anything uses the result — the narrowing check's staged child
// moxyfile and the agent's actual launch alike — so a broken resolution
// never silently falls back to something unsafe.
func DeriveAgentMoxyfile(ctx context.Context, moxyCLI MoxyCLI, env []string, nameTemplate string, b *jugglerbrief.Brief) (string, error) {
	doc := map[string]any{}
	if strings.TrimSpace(b.Moxyfile) != "" {
		if _, err := toml.Decode(b.Moxyfile, &doc); err != nil {
			return "", fmt.Errorf("jugglerrun: decoding the brief's moxyfile: %w", err)
		}
	}

	allow := make([]string, 0, len(b.Tools))
	for _, t := range b.Tools {
		if t == jugglerloop.CannotCompleteTool {
			continue
		}
		server, tool, err := moxyCLI.ResolveToolName(ctx, env, nameTemplate, t)
		if err != nil {
			return "", &ErrToolResolution{Tool: t, Detail: err.Error()}
		}
		allow = append(allow, server+"."+tool)
	}
	sort.Strings(allow)
	doc["allow-tools"] = allow

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return "", fmt.Errorf("jugglerrun: encoding the agent's derived moxyfile: %w", err)
	}
	return buf.String(), nil
}
