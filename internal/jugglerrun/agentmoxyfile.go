package jugglerrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"code.linenisgreat.com/clown/internal/jugglerbrief"
	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// ErrToolResolution is a brief refusal (clown#261 point 3): moxy's own
// `render --resolve` could not turn an advertised juggler tool name back
// into its canonical "server.tool" pair, so the agent's own allow-tools
// cannot be derived safely. Never silently falls back to the rendered name
// (which moxy would read as a bare, nonexistent server, allowing nothing)
// or drops the entry — a broken resolution never passes, the same
// discipline checkBriefNarrows already applies to a broken narrowing check.
// This names a genuine per-tool resolution failure only (ResolveToolName
// itself ran and reported one, e.g. "not invertible"); a moxy binary that
// could not be run at all, or a cancelled ctx, is returned bare instead —
// it is not about any one tool, and blaming the first one iterated would
// mislead.
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

// ErrToolCoverageMismatch is a brief refusal (clown#261 point 3,
// LOW item 5 of the clown#261 review): the brief's own moxyfile sets
// allow-tools, but a tools entry's REAL canonical resolution is not covered
// by it. This catches a gap Brief.Validate's own check cannot: Validate's
// coverage heuristic (checkToolsCoveredByAllowTools) assumes a dotted allow
// entry's server/tool split matches juggler's own tools-entry string
// textually, but moxy's structural inverse always splits on the FIRST
// separator — for a tools entry like "foo_bar_x", Validate's heuristic can
// accept a bare allow entry "foo_bar" (reading tool "x"), while the real
// resolution is ("foo", "bar_x"). Requiring the ACTUAL resolved name to be
// covered by the brief's own allow-tools too means the override (below)
// can never grant something the brief's own allowlist did not really
// cover, regardless of that heuristic's blind spot.
type ErrToolCoverageMismatch struct {
	// Tool is the brief's tools entry; Resolved its real "server.tool".
	Tool, Resolved string
}

func (e *ErrToolCoverageMismatch) Error() string {
	return fmt.Sprintf("tools entry %q resolves to %q, which the brief's own moxyfile allow-tools does not cover", e.Tool, e.Resolved)
}

// ToolCoverageMismatchRefusalEntry is the run ledger's record of a spawn
// refused pre-mint because a brief's own allow-tools does not actually
// cover a tools entry's real resolution (clown#261).
func ToolCoverageMismatchRefusalEntry(e *ErrToolCoverageMismatch) RunLedgerEntry {
	return RunLedgerEntry{
		Tool: "tool-resolve", Kind: "refusal", OK: false, URIs: []string{},
		Reason: e.Error(),
	}
}

// moxyCoveredBy is moxy's own CoveredBy rule (internal/config/config.go in
// the moxy repo): entry is covered by set if set holds it verbatim, or, for
// a dotted "server.tool" entry, set holds the bare server.
func moxyCoveredBy(entry string, set map[string]bool) bool {
	if set[entry] {
		return true
	}
	if server, _, dotted := strings.Cut(entry, "."); dotted && set[server] {
		return true
	}
	return false
}

// DeriveAgentMoxyfile computes the moxyfile the agent's own moxy must
// actually load (clown#261 point 3): b.Moxyfile, with "allow-tools" always
// replaced by the canonical dotted form of b.Tools ("server.tool", moxy's
// own vocabulary regardless of serving template — see ResolveToolName).
// Overriding rather than intersecting the brief's own allow-tools (when it
// sets one) is the narrowest outcome: Validate already guarantees the
// brief's own allow-tools, when set, covers every tools entry — and this
// function independently re-checks the REAL resolved name against that
// same brief-declared allow-tools (ErrToolCoverageMismatch), since
// Validate's own coverage check is a textual heuristic that can diverge
// from moxy's actual first-separator split for a tools entry with more
// than one underscore. Every other key — pass-tools above all, also
// disable-moxins, [[servers]], [credentials], ... — is kept exactly as the
// brief wrote it; Validate separately refuses a brief whose pass-tools
// would collide with the derived allow-tools
// (checkPassToolsDoNotCoverTools). cannot_complete is never resolved: it is
// juggler's own injected pseudo-tool, not a tool moxy serves at all. A
// tools entry ResolveToolName genuinely can't resolve is *ErrToolResolution,
// naming it; a moxy binary that could not be run at all, or a cancelled
// ctx, propagates bare instead of blaming a tool for an infrastructure
// problem. This must run, and must be checked, before anything uses the
// result — the narrowing check's staged child moxyfile (spawn.go) and the
// agent's actual launch (agent.go) alike, each calling this independently
// from the same unmodified brief bytes, so a broken resolution never
// silently falls back to something unsafe. The two calls need not produce
// byte-identical output to be safe: ResolveToolName is a pure function of
// its input over a pinned moxy binary and a compile-time-constant
// nameTemplate, BurntSushi's encoder sorts map keys deterministically, and
// moxy's own startup/reload check re-judges whatever the unit's moxy
// actually loads against MOXY_PARENT_MOXYFILE independently either way
// (moxy's cmd/moxy/narrowing.go) — no digest comparison between the two
// call sites is needed.
func DeriveAgentMoxyfile(ctx context.Context, moxyCLI MoxyCLI, env []string, nameTemplate string, b *jugglerbrief.Brief) (string, error) {
	doc := map[string]any{}
	if strings.TrimSpace(b.Moxyfile) != "" {
		if _, err := toml.Decode(b.Moxyfile, &doc); err != nil {
			return "", fmt.Errorf("jugglerrun: decoding the brief's moxyfile: %w", err)
		}
	}
	briefAllow, briefSetAllow := briefAllowSet(doc["allow-tools"])

	allow := make([]string, 0, len(b.Tools))
	for _, t := range b.Tools {
		if t == jugglerloop.CannotCompleteTool {
			continue
		}
		server, tool, err := moxyCLI.ResolveToolName(ctx, env, nameTemplate, t)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			var ce *CommandError
			if errors.As(err, &ce) && ce.ExitCode == -1 {
				return "", err
			}
			return "", &ErrToolResolution{Tool: t, Detail: err.Error()}
		}
		resolved := server + "." + tool
		if briefSetAllow && !moxyCoveredBy(resolved, briefAllow) {
			return "", &ErrToolCoverageMismatch{Tool: t, Resolved: resolved}
		}
		allow = append(allow, resolved)
	}
	sort.Strings(allow)
	doc["allow-tools"] = allow

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return "", fmt.Errorf("jugglerrun: encoding the agent's derived moxyfile: %w", err)
	}
	return buf.String(), nil
}

// briefAllowSet reads a decoded moxyfile's own "allow-tools" value (a
// []any of strings, as toml.Decode into map[string]any renders it) into a
// set, and whether the key was present at all (nil means unset, an empty
// array is a set that covers nothing).
func briefAllowSet(v any) (map[string]bool, bool) {
	if v == nil {
		return nil, false
	}
	raw, _ := v.([]any)
	set := make(map[string]bool, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			set[s] = true
		}
	}
	return set, true
}
