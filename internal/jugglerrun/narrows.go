package jugglerrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// NarrowsViolation is one entry of `moxy narrows --json`'s violations.
type NarrowsViolation struct {
	Key     string `json:"key"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

// NarrowsResult is `moxy narrows`' verdict. Widens is exit 1: Lines are its
// stdout lines ("<key>: <reason>", sorted, byte-stable) and Violations the
// --json form of the same.
type NarrowsResult struct {
	Widens     bool
	Lines      []string
	Violations []NarrowsViolation
	// ViolationsParsed: the --json rerun produced a parseable document; when
	// false, Violations is not the structured form of Lines.
	ViolationsParsed bool
	// MoxyBin and ParentMoxyfileDigest are the check's provenance, set by
	// the caller for the refusal entry.
	MoxyBin, ParentMoxyfileDigest string
}

// MoxyCLI is the moxy surface `juggler spawn` checks a brief's moxyfile with
// (FDR 0019 §5). Every call runs with env as the complete environment.
type MoxyCLI interface {
	// Path is the moxy binary used, for the child record.
	Path() string
	// ShowEffectiveMoxyfile renders the effective moxyfile of dir
	// (`moxy show-effective-moxyfile --dir`): canonical, include-free TOML
	// with the resolved moxins.
	ShowEffectiveMoxyfile(ctx context.Context, env []string, dir string) ([]byte, error)
	// Narrows runs `moxy narrows --parent parent --child child`. A returned
	// error is exit 2 (or no exit at all): the check could not be made.
	Narrows(ctx context.Context, env []string, parent, child string) (NarrowsResult, error)
}

// ExecMoxyCLI shells the moxy binary. Argv shapes:
//
//	show-effective-moxyfile --dir D                -> TOML on stdout
//	narrows --parent P --child C [--json]          -> exit 0 / 1 / 2
type ExecMoxyCLI struct {
	Bin string
}

func (m ExecMoxyCLI) Path() string { return m.Bin }

func (m ExecMoxyCLI) ShowEffectiveMoxyfile(ctx context.Context, env []string, dir string) ([]byte, error) {
	out, err := runCommand(ctx, Command{Argv: []string{m.Bin, "show-effective-moxyfile", "--dir", dir}, Env: env})
	if err != nil {
		var ce *CommandError
		if errors.As(err, &ce) && ce.ExitCode == -1 {
			return nil, fmt.Errorf("the moxyfile narrowing check needs moxy, and %q could not be run (set --moxy or $%s): %w", m.Bin, MoxyBinEnv, err)
		}
		return nil, fmt.Errorf("rendering the spawner's effective moxyfile: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("rendering the spawner's effective moxyfile: %s printed nothing", m.Bin)
	}
	return out, nil
}

func (m ExecMoxyCLI) Narrows(ctx context.Context, env []string, parent, child string) (NarrowsResult, error) {
	argv := []string{m.Bin, "narrows", "--parent", parent, "--child", child}
	out, err := runCommand(ctx, Command{Argv: argv, Env: env})
	if err == nil {
		return NarrowsResult{}, nil
	}
	var ce *CommandError
	if !errors.As(err, &ce) || ce.ExitCode != 1 {
		return NarrowsResult{}, fmt.Errorf("checking the brief's moxyfile: %w", err)
	}
	res := NarrowsResult{Widens: true}
	for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if l != "" {
			res.Lines = append(res.Lines, l)
		}
	}
	// The structured form, for the run ledger; the verdict above stands
	// whatever this second call does.
	// It exits 1 too; its stdout is read either way.
	jsonOut, _ := runCommand(ctx, Command{Argv: append(argv, "--json"), Env: env})
	var doc struct {
		Violations []NarrowsViolation `json:"violations"`
	}
	if json.Unmarshal(bytes.TrimSpace(jsonOut), &doc) == nil {
		res.Violations, res.ViolationsParsed = doc.Violations, true
	}
	return res, nil
}

// missingMoxyCLI stands in when LifecycleDeps names no MoxyCLI: the check
// never silently passes.
type missingMoxyCLI struct{}

var errNoMoxyCLI = errors.New("no moxy binary is configured for the moxyfile narrowing check")

func (missingMoxyCLI) Path() string { return "" }

func (missingMoxyCLI) ShowEffectiveMoxyfile(context.Context, []string, string) ([]byte, error) {
	return nil, errNoMoxyCLI
}

func (missingMoxyCLI) Narrows(context.Context, []string, string, string) (NarrowsResult, error) {
	return NarrowsResult{}, errNoMoxyCLI
}

// ErrBriefWidens is a `spawn --brief` refusal: the brief's moxyfile widens
// the spawner's effective moxyfile.
type ErrBriefWidens struct {
	// Lines are `moxy narrows`' violation lines, verbatim.
	Lines  []string
	result NarrowsResult
}

func (e *ErrBriefWidens) Error() string {
	return "brief moxyfile widens the spawner's:\n" + strings.Join(e.Lines, "\n")
}

// NarrowsRefusalEntry is the run ledger's record of a refused spawn,
// auditable without a child record: the violations (or violations_missing
// when moxy's --json form was unavailable, the text lines then being the
// error's only record), the moxy binary and the parent reference's digest.
func NarrowsRefusalEntry(res NarrowsResult) RunLedgerEntry {
	return RunLedgerEntry{
		Tool: "narrows", Kind: "refusal", OK: false, URIs: []string{},
		Reason: "brief moxyfile widens the spawner's", Violations: res.Violations,
		ViolationsMissing: !res.ViolationsParsed,
		MoxyBin:           res.MoxyBin, ParentMoxyfileDigest: res.ParentMoxyfileDigest,
	}
}

// moxyEnv is the spawner's environment for its moxy calls, minus its
// principal (StripPrincipalEnv: CLOWN_SESSION_ID, TROUPE_XMPP_*,
// TROUPE_MINT_*), with MOXIN_PATH replaced by the pinned value (removed when
// it is "") and no MOXY_PARENT_MOXYFILE of the spawner's own leaking into the
// render. PATH, HOME and the rest are kept.
func moxyEnv(moxinPath string) []string {
	base := StripPrincipalEnv(os.Environ())
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k == "MOXIN_PATH" || k == "MOXY_PARENT_MOXYFILE" {
			continue
		}
		out = append(out, kv)
	}
	if moxinPath != "" {
		out = append(out, "MOXIN_PATH="+moxinPath)
	}
	return out
}

func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
