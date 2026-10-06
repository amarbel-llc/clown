package jugglerrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/jugglerbrief"
	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// cleanupTimeout bounds the undo of a failed spawn.
const cleanupTimeout = 30 * time.Second

// DefaultStopGrace is added to the brief's wall clock for RuntimeMaxSec, so
// the loop's own budget (primary) expires before systemd's backstop.
const DefaultStopGrace = 30 * time.Second

// undoStack collects the compensations of a multi-step spawn. On failure
// they run newest first, so a spawn never leaves an orphan (FDR 0019 §1).
type undoStack []func(ctx context.Context, cause error) error

func (u *undoStack) push(f func(ctx context.Context, cause error) error) { *u = append(*u, f) }

func (u undoStack) unwind(cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	var errs []error
	for i := len(u) - 1; i >= 0; i-- {
		if err := u[i](ctx, cause); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w (cleanup also failed: %v)", cause, errors.Join(errs...))
	}
	return cause
}

// withdrawalStanza marks a stanza a failed spawn posted as void, so the room
// never holds a brief or run input with no agent behind it.
func withdrawalStanza(stanzaID string, cause error) []byte {
	b, _ := json.Marshal(map[string]string{"type": "withdrawn", "stanza": stanzaID, "reason": cause.Error()})
	return b
}

// NewRunRequest is `juggler spawn --new-run`.
type NewRunRequest struct {
	// RunKey is the caller's idempotency key; empty mints a fresh one.
	RunKey string
	// Issuer is the FDR 0032 D2 issuer: the run job lives on its channel and
	// its exit wake goes to it.
	Issuer string
	// Input is the run's input (the recording), posted as the root stanza.
	Input []byte
	// Room names an existing, already-configured run MUC. Exactly one of Room
	// and RoomDomain is set.
	Room string
	// RoomDomain is the MUC component a new room would be created on; it
	// needs a RoomProvisioner, which troupe does not yet back.
	RoomDomain string
}

// NewRunResult is `juggler spawn --new-run`'s stdout object.
type NewRunResult struct {
	RunKey            string `json:"run_key"`
	RootPrincipal     string `json:"root_principal"`
	RootCredentialRef string `json:"root_credential_ref"`
	RootJID           string `json:"root_jid"`
	Room              string `json:"room"`
	// RootStanza is the id of the run-input stanza posted to the room: the
	// provenance parent `juggler decide --parent` takes.
	RootStanza string `json:"root_stanza"`
	RunJob     string `json:"run_job"`
	Existing   bool   `json:"existing"`
	// Resolved and TornDown tell a caller that finds an existing run how far
	// it got: a crashed first delivery leaves it unresolved.
	Resolved bool `json:"resolved"`
	TornDown bool `json:"torn_down"`
}

func newRunResult(rec *RunRecord, existing bool) NewRunResult {
	return NewRunResult{
		RunKey: rec.RunKey, RootPrincipal: rec.RootPrincipal, RootCredentialRef: rec.RootCredentialRef,
		RootJID: rec.RootJID, Room: rec.Room, RootStanza: rec.RootStanza, RunJob: rec.RunJob, Existing: existing,
		Resolved: rec.Resolved != nil, TornDown: rec.TornDown,
	}
}

// runInputStanza is the room's root stanza.
type runInputStanza struct {
	Type   string          `json:"type"`
	RunKey string          `json:"run_key"`
	Issuer string          `json:"issuer"`
	Root   string          `json:"root"`
	Input  json.RawMessage `json:"input"`
}

// NewRun mints a run root (FDR 0019 §1, §2): the root principal and its JID,
// the run's room (or an existing one), the input as the root stanza, a
// run-level job on the issuer's channel with the issuer as first holder, and
// the run record. An existing RunKey returns the stored run unchanged with
// Existing set and creates nothing.
func NewRun(ctx context.Context, deps LifecycleDeps, req NewRunRequest) (NewRunResult, error) {
	deps = deps.withDefaults()
	if req.Issuer == "" {
		return NewRunResult{}, errors.New("an issuer principal is required")
	}
	if (req.Room == "") == (req.RoomDomain == "") {
		return NewRunResult{}, errors.New("exactly one of an existing room or a room domain is required")
	}
	if req.RunKey == "" {
		req.RunKey = "run-" + deps.NewPrincipal()
	}
	if err := ValidRunKey(req.RunKey); err != nil {
		return NewRunResult{}, err
	}
	unlock, err := lock(deps.Store.runPath(req.RunKey) + ".lock")
	if err != nil {
		return NewRunResult{}, err
	}
	defer unlock()
	if rec, err := deps.Store.LoadRun(req.RunKey); err != nil {
		return NewRunResult{}, err
	} else if rec != nil {
		return newRunResult(rec, true), nil
	}

	var undo undoStack
	rec := &RunRecord{Schema: RecordSchema, RunKey: req.RunKey, Issuer: req.Issuer, CreatedAt: deps.Now().UTC()}
	rec.RootPrincipal = deps.NewPrincipal()

	rootPW, err := deps.Store.CredentialPath(req.RunKey, rec.RootPrincipal)
	if err != nil {
		return NewRunResult{}, fmt.Errorf("preparing the run root's credential file: %w", err)
	}
	cred, err := deps.Troupe.Mint(ctx, rec.RootPrincipal, rootPW)
	if err != nil {
		return NewRunResult{}, fmt.Errorf("minting the run root: %w", err)
	}
	undo.push(func(ctx context.Context, _ error) error {
		return deps.Troupe.RevokeMint(ctx, rec.RootPrincipal, rootPW)
	})
	rec.RootJID, rec.RootCredentialRef = cred.JID, cred.PasswordFile
	root := IdentityFor(rec.RootPrincipal, cred)

	rec.Room = req.Room
	if rec.Room == "" {
		rec.Room = "juggler-" + roomLocalpart(req.RunKey) + "@" + req.RoomDomain
		if err := deps.Rooms.CreateRoom(ctx, rec.Room); err != nil {
			return NewRunResult{}, undo.unwind(fmt.Errorf("creating room %s: %w", rec.Room, err))
		}
		rec.RoomCreated = true
		undo.push(func(ctx context.Context, _ error) error { return deps.Rooms.DestroyRoom(ctx, rec.Room) })
	}

	stanza, err := json.Marshal(runInputStanza{Type: "run_input", RunKey: rec.RunKey, Issuer: rec.Issuer, Root: rec.RootPrincipal, Input: inputAsJSON(req.Input)})
	if err != nil {
		return NewRunResult{}, undo.unwind(err)
	}
	if rec.RootStanza, err = deps.Troupe.PostStanza(ctx, root, rec.Room, SpawnSource, stanza); err != nil {
		return NewRunResult{}, undo.unwind(fmt.Errorf("posting the run input to %s: %w", rec.Room, err))
	}
	if !rec.RoomCreated {
		undo.push(func(ctx context.Context, cause error) error {
			_, err := deps.Troupe.PostStanza(ctx, root, rec.Room, SpawnSource, withdrawalStanza(rec.RootStanza, cause))
			return err
		})
	}

	if rec.RunJob, err = deps.Ringmaster.Start(ctx, rec.Issuer, RunJobLabel, SpawnSource); err != nil {
		return NewRunResult{}, undo.unwind(fmt.Errorf("starting the run job on %s: %w", rec.Issuer, err))
	}
	undo.push(failJobUndo(deps.Ringmaster, rec.Issuer, rec.RunJob))

	holder := FirstHolder(rec.Issuer, deps.Now())
	if err := mirrorHolder(ctx, deps.Ringmaster, rec.Issuer, rec.RunJob, holder); err != nil {
		return NewRunResult{}, undo.unwind(err)
	}
	rec.Holders = []Holder{holder}
	if err := deps.Store.SaveRun(rec); err != nil {
		return NewRunResult{}, undo.unwind(fmt.Errorf("saving the run record: %w", err))
	}
	return newRunResult(rec, false), nil
}

func failJobUndo(rmc Ringmaster, target, job string) func(ctx context.Context, cause error) error {
	return func(ctx context.Context, cause error) error {
		return rmc.Done(ctx, target, job, DoneRecord{State: StateFailed, Message: "juggler spawn failed: " + cause.Error()})
	}
}

// inputAsJSON embeds valid JSON input verbatim and anything else as a string.
func inputAsJSON(input []byte) json.RawMessage {
	if trimmed := bytes.TrimSpace(input); json.Valid(trimmed) {
		return json.RawMessage(trimmed)
	}
	quoted, _ := json.Marshal(string(input))
	return quoted
}

// roomLocalpart lowercases key and maps anything outside [a-z0-9-] to '-'.
func roomLocalpart(key string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(key) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// SpawnRequest is `juggler spawn --brief`.
type SpawnRequest struct {
	// Brief is the brief template as the spawner shipped it (see FillTemplate).
	Brief []byte
	// Task is the brief's task; empty keeps the template's own.
	Task []byte
	// RunKey names the run; empty finds it by the template's room, else by
	// EnvPrincipal as the run root (the glue runs as the root).
	RunKey       string
	EnvPrincipal string
	// MoxyURL is handed to the agent (JUGGLER_MOXY_URL in the unit).
	MoxyURL string
	// JugglerBin is the juggler binary the unit execs (run and exit-wake).
	JugglerBin string
	// StopGrace is added to the wall clock for RuntimeMaxSec;
	// DefaultStopGrace when zero.
	StopGrace time.Duration
	// UnitEnv is passed to the unit as-is (see PassthroughUnitEnv); identity
	// variables in it are ignored.
	UnitEnv map[string]string
}

// LaunchResult is `juggler spawn --brief`'s stdout object without --wait.
type LaunchResult struct {
	JID  string `json:"jid"`
	Job  string `json:"job"`
	Room string `json:"room"`
}

// Launch is the launch JSON for rec.
func (rec *ChildRecord) Launch() LaunchResult {
	return LaunchResult{JID: rec.JID, Job: rec.Job, Room: rec.Room}
}

// BriefDigest is the subagent idempotency key within a run: the SHA-256 of
// the template exactly as the spawner handed it, a NUL, and the task.
func BriefDigest(template, task []byte) string {
	h := sha256.New()
	h.Write(template)
	h.Write([]byte{0})
	h.Write(task)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// TemplateFill is what the spawner fills into a brief template.
type TemplateFill struct {
	Principal, Parent, Room, Task string
}

// FillTemplate parses a brief template (jugglerbrief.ParseTemplate: model,
// system, tools, evaluator and limits must be present; principal, parent,
// room and task may be empty), fills principal, parent and room, and task
// when fill.Task is non-empty, then validates the filled brief strictly and
// returns it with its canonical bytes. A template may not pin a principal,
// and one that names a parent or room must name the run's.
func FillTemplate(template []byte, fill TemplateFill) (*jugglerbrief.Brief, []byte, error) {
	b, err := jugglerbrief.ParseTemplate(template)
	if err != nil {
		return nil, nil, err
	}
	if b.Principal != "" {
		return nil, nil, errors.New("brief template sets principal; the spawner mints it")
	}
	if b.Parent != "" && b.Parent != fill.Parent {
		return nil, nil, fmt.Errorf("brief template parent %q is not the run root %q", b.Parent, fill.Parent)
	}
	if b.Room != "" && b.Room != fill.Room {
		return nil, nil, fmt.Errorf("brief template room %q is not the run's room %q", b.Room, fill.Room)
	}
	b.Principal, b.Parent, b.Room = fill.Principal, fill.Parent, fill.Room
	if fill.Task != "" {
		b.Task = fill.Task
	}
	canonical, err := b.Marshal()
	if err != nil {
		return nil, nil, err
	}
	return b, canonical, nil
}

// briefStanza is the brief as it is dropped into the run's room.
type briefStanza struct {
	Type      string `json:"type"`
	Principal string `json:"principal"`
	Parent    string `json:"parent"`
	Job       string `json:"job"`
	Digest    string `json:"digest"`
	Brief     string `json:"brief"`
}

// SpawnChild is `juggler spawn --brief` (FDR 0019 §1): it mints the child
// principal and JID, fills brief.principal, stages the brief, starts the
// child's job on the parent's channel with the parent as first holder, drops
// the brief into the room as the run root (the ambient TROUPE_XMPP_*
// identity), saves the child record and starts the transient unit. A brief
// with the same digest within the run returns the existing child
// (existing=true) and creates nothing. Any failure after the mint unwinds
// everything created, newest first.
func SpawnChild(ctx context.Context, deps LifecycleDeps, req SpawnRequest) (rec *ChildRecord, existing bool, err error) {
	deps = deps.withDefaults()
	if req.JugglerBin == "" {
		return nil, false, errors.New("the juggler binary path is required")
	}
	digest := BriefDigest(req.Brief, req.Task)
	tmpl, err := jugglerbrief.ParseTemplate(req.Brief)
	if err != nil {
		return nil, false, err
	}
	run, err := findSpawnRun(deps.Store, req.RunKey, tmpl.Room, req.EnvPrincipal)
	if err != nil {
		return nil, false, err
	}
	fill := TemplateFill{Principal: "unminted", Parent: run.RootPrincipal, Room: run.Room, Task: string(req.Task)}
	probe, _, err := FillTemplate(req.Brief, fill)
	if err != nil {
		return nil, false, err
	}
	if run.Resolved != nil {
		return nil, false, fmt.Errorf("run %s is already resolved (%s)", run.RunKey, run.Resolved.State)
	}

	unlock, err := lock(deps.Store.childPath(run.RunKey, digest) + ".lock")
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	if prior, err := deps.Store.LoadChild(run.RunKey, digest); err != nil {
		return nil, false, err
	} else if prior != nil {
		return prior, true, nil
	}

	rec = &ChildRecord{Schema: RecordSchema, RunKey: run.RunKey, BriefDigest: digest, Parent: probe.Parent, Room: probe.Room, CreatedAt: deps.Now().UTC()}
	rec.Principal = deps.NewPrincipal()
	fill.Principal = rec.Principal
	brief, canonical, err := FillTemplate(req.Brief, fill)
	if err != nil {
		return nil, false, err
	}
	rec.WallClock = brief.Limits.WallClock
	wallClock, _ := brief.Limits.WallClockDuration()

	var undo undoStack
	childPW, err := deps.Store.CredentialPath(run.RunKey, rec.Principal)
	if err != nil {
		return nil, false, fmt.Errorf("preparing the agent's credential file: %w", err)
	}
	cred, err := deps.Troupe.Mint(ctx, rec.Principal, childPW)
	if err != nil {
		return nil, false, fmt.Errorf("minting the agent principal: %w", err)
	}
	undo.push(func(ctx context.Context, _ error) error { return deps.Troupe.RevokeMint(ctx, rec.Principal, childPW) })
	rec.JID, rec.CredentialRef = cred.JID, cred.PasswordFile

	rec.BriefPath = deps.Store.stagedBriefPath(run.RunKey, digest)
	if err := writeFileAtomic(rec.BriefPath, canonical); err != nil {
		return nil, false, undo.unwind(fmt.Errorf("staging the brief: %w", err))
	}
	undo.push(func(context.Context, error) error { return removeIfExists(rec.BriefPath) })

	if rec.Job, err = deps.Ringmaster.Start(ctx, rec.Parent, AgentJobLabel, SpawnSource); err != nil {
		return nil, false, undo.unwind(fmt.Errorf("starting the agent job on %s: %w", rec.Parent, err))
	}
	undo.push(failJobUndo(deps.Ringmaster, rec.Parent, rec.Job))
	holder := FirstHolder(rec.Parent, deps.Now())
	if err := mirrorHolder(ctx, deps.Ringmaster, rec.Parent, rec.Job, holder); err != nil {
		return nil, false, undo.unwind(err)
	}
	rec.Holders = []Holder{holder}

	stanza, err := json.Marshal(briefStanza{Type: "brief", Principal: rec.Principal, Parent: rec.Parent, Job: rec.Job, Digest: digest, Brief: string(canonical)})
	if err != nil {
		return nil, false, undo.unwind(err)
	}
	if rec.BriefStanza, err = deps.Troupe.PostStanza(ctx, Identity{}, rec.Room, SpawnSource, stanza); err != nil {
		return nil, false, undo.unwind(fmt.Errorf("posting the brief to %s: %w", rec.Room, err))
	}
	undo.push(func(ctx context.Context, cause error) error {
		_, err := deps.Troupe.PostStanza(ctx, Identity{}, rec.Room, SpawnSource, withdrawalStanza(rec.BriefStanza, cause))
		return err
	})

	rec.Unit = "juggler-agent-" + rec.Principal
	if err := deps.Store.SaveChild(rec); err != nil {
		return nil, false, undo.unwind(fmt.Errorf("saving the child record: %w", err))
	}
	undo.push(func(context.Context, error) error {
		return removeIfExists(deps.Store.childPath(rec.RunKey, rec.BriefDigest))
	})

	grace := req.StopGrace
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	if err := deps.Units.Launch(ctx, agentUnit(rec, cred, req, brief.Env, wallClock+grace)); err != nil {
		return nil, false, undo.unwind(fmt.Errorf("starting transient unit %s: %w", rec.Unit, err))
	}
	return rec, false, nil
}

// agentUnit is the child's transient unit. Its environment is exactly the
// child's identity (CLOWN_SESSION_ID and the minted TROUPE_XMPP_USER /
// PASSWORD_FILE / DOMAIN), the moxy upstream, and the passthrough variables;
// the brief travels as a staged file, so the child key is in nothing else.
// The brief's own [env] (validated by jugglerbrief: no reserved keys) is
// layered over the passthrough so moxy and its moxins inherit it.
func agentUnit(rec *ChildRecord, cred Credential, req SpawnRequest, briefEnv map[string]string, runtimeMax time.Duration) TransientUnit {
	// The passthrough keeps TROUPE_XMPP_HOST/PORT/INSECURE (connection
	// settings, not identity); any identity key in it is overwritten below.
	env := maps.Clone(req.UnitEnv)
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range briefEnv {
		if !jugglerbrief.ReservedEnvKey(k) {
			env[k] = v
		}
	}
	env[SessionIDEnv] = rec.Principal
	env["TROUPE_XMPP_USER"] = cred.Localpart()
	env["TROUPE_XMPP_PASSWORD_FILE"] = cred.PasswordFile
	env["TROUPE_XMPP_DOMAIN"] = cred.Domain()
	if req.MoxyURL != "" {
		env[MoxyURLEnv] = req.MoxyURL
	}
	return TransientUnit{
		Name:         rec.Unit,
		RuntimeMax:   runtimeMax,
		ExecStopPost: []string{req.JugglerBin, "exit-wake", "--job", rec.Job, "--target", rec.Parent},
		Env:          env,
		Exec:         []string{req.JugglerBin, "run", "--brief", rec.BriefPath, "--job", rec.Job, "--brief-stanza", rec.BriefStanza},
	}
}

func findSpawnRun(store Store, runKey, room, rootPrincipal string) (*RunRecord, error) {
	if runKey == "" && room == "" {
		if rootPrincipal == "" {
			return nil, errors.New("cannot find the run: pass --run-key (the template names no room and CLOWN_SESSION_ID is unset)")
		}
		rec, err := store.findRun(func(r *RunRecord) bool { return r.RootPrincipal == rootPrincipal })
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, fmt.Errorf("no run has root principal %q (create it with juggler spawn --new-run)", rootPrincipal)
		}
		return rec, nil
	}
	if runKey != "" {
		if err := ValidRunKey(runKey); err != nil {
			return nil, err
		}
		rec, err := store.LoadRun(runKey)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, fmt.Errorf("no run with key %q (create it with juggler spawn --new-run)", runKey)
		}
		return rec, nil
	}
	rec, err := store.FindRunByRoom(room)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("no run owns room %q (create it with juggler spawn --new-run)", room)
	}
	return rec, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// WaitRecord is `juggler spawn --wait`'s stdout object (FDR 0019 §1).
type WaitRecord struct {
	JID            string                      `json:"jid"`
	Job            string                      `json:"job"`
	Room           string                      `json:"room"`
	State          string                      `json:"state"`
	Reason         ExitReason                  `json:"reason"`
	Message        string                      `json:"message"`
	Ledger         string                      `json:"ledger"`
	Artifacts      []Artifact                  `json:"artifacts"`
	CannotComplete *jugglerloop.CannotComplete `json:"cannot_complete"`
}

// ExitCode mirrors the state; a job still running after the wait is 5.
func (w WaitRecord) ExitCode() int {
	if !IsTerminalState(w.State) {
		return ExitWaitTimeout
	}
	return ExitCodeForState(w.State)
}

// DefaultWaitTimeout bounds `--wait` when no --timeout is given: past the
// unit's RuntimeMaxSec the post-stop hook has written the terminal.
func DefaultWaitTimeout(rec *ChildRecord, grace time.Duration) time.Duration {
	wall, err := time.ParseDuration(rec.WallClock)
	if err != nil {
		wall = jugglerloop.DefaultWallClock
	}
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	return wall + 2*grace
}

// WaitChild blocks until the child's job is terminal or timeout elapses and
// reads the verdict, reason and ledger artifacts back from the journal and
// the result spool (`ringmaster wait` polls; it has no wake subscription).
// When timeout elapses first, the job is asked to cancel and given grace
// (DefaultStopGrace when zero) to terminalize, so a caller that falls back
// never races a late agent; a job still not terminal after that is running.
func WaitChild(ctx context.Context, deps LifecycleDeps, rec *ChildRecord, timeout, grace time.Duration) (WaitRecord, error) {
	out := WaitRecord{JID: rec.JID, Job: rec.Job, Room: rec.Room, Artifacts: []Artifact{}}
	_ = deps.Ringmaster.WaitTerminal(ctx, rec.Parent, rec.Job, timeout)
	recs, err := deps.Ringmaster.Records(ctx, rec.Parent, rec.Job)
	if err != nil {
		return out, fmt.Errorf("reading %s: %w", rec.Job, err)
	}
	if len(recs) == 0 {
		return out, fmt.Errorf("job %s has no journal on %s's channel", rec.Job, rec.Parent)
	}
	term, terminal := TerminalRecord(recs)
	if !terminal {
		if recs, terminal, err = requestStop(ctx, deps.Ringmaster, rec.Parent, rec.Job, "juggler spawn --wait timed out", grace); err != nil {
			return out, err
		}
		term, _ = TerminalRecord(recs)
	}
	if !terminal {
		out.State = StateRunning
		return out, nil
	}
	out.State, out.Message, out.Ledger = term.Type, term.Message, term.ResultRef
	out.Reason = ExitReasonFor(out.State, out.Message)
	l, found, err := readAgentLedger(out.Ledger)
	if err != nil {
		return out, err
	}
	if !found && out.Ledger == "" {
		// A hook-written terminal carries no result_ref; the spool may still
		// hold a ledger the agent wrote before it died.
		if path, err := deps.Ringmaster.SpoolPath(ctx, rec.Parent, rec.Job); err == nil {
			if l, found, _ = readAgentLedger(path); found {
				out.Ledger = path
			}
		}
	}
	if found {
		out.Artifacts = ArtifactsFromLedger(l)
		out.CannotComplete = l.CannotComplete
	}
	return out, nil
}
