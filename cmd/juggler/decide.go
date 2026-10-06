package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	jd "code.linenisgreat.com/clown/internal/jugglerdecide"
)

const decideUsage = "usage: juggler decide --model <name> --room <muc-jid> --parent <stanza-id> [--min-confidence 0.5] [--question <id>] [--troupe <path>]  (payload {\"state\",\"questions\"} on stdin)"

// decideTimeout spans model resolution and the Decisions call.
const decideTimeout = 60 * time.Second

// decidePostTimeout bounds the `troupe muc send` post.
const decidePostTimeout = 30 * time.Second

// decideModelStyle is the registry style `juggler decide` requires.
const decideModelStyle = "decisions"

// decideModel is what decide needs from the registry: where to POST and
// with which bearer token.
type decideModel struct {
	URL   string
	Token string
}

// decideResolver resolves a registry name to a Decisions endpoint. It is the
// seam the daemon-free resolver (FDR 0019 §10) swaps in behind; the daemon
// RPC implementation is daemonDecideResolver.
type decideResolver interface {
	ResolveDecisionsModel(ctx context.Context, name string) (decideModel, error)
}

// daemonDecideResolver resolves through the juggler daemon's ResolveModel RPC.
type daemonDecideResolver struct{ cli *rm.Client }

func (d daemonDecideResolver) ResolveDecisionsModel(ctx context.Context, name string) (decideModel, error) {
	resolved, err := d.cli.ResolveModel(ctx, rm.ResolveModelParams{Name: name})
	if err != nil {
		return decideModel{}, fmt.Errorf("resolve model %q: %w", name, err)
	}
	return decisionsModelFrom(name, resolved)
}

// decisionsModelFrom validates a resolved entry as a remote decisions-style
// model; an unknown style is a config error naming the style.
func decisionsModelFrom(name string, r rm.ResolveModelResult) (decideModel, error) {
	if r.Kind != rm.ModelKindRemote {
		return decideModel{}, fmt.Errorf("model %q is %s; decide requires a remote model", name, r.Kind)
	}
	if r.Style != decideModelStyle {
		return decideModel{}, fmt.Errorf("model %q has style %q; decide requires style %q", name, r.Style, decideModelStyle)
	}
	return decideModel{URL: r.URL, Token: r.Token}, nil
}

// decideOpts is the parsed flag set.
type decideOpts struct {
	model         string
	room          string
	parent        string
	question      string
	troupe        string
	minConfidence float64
}

func parseDecideFlags(args []string, stderr io.Writer) (decideOpts, error) {
	o := decideOpts{}
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.model, "model", "", "registry model name (style = decisions)")
	fs.StringVar(&o.room, "room", "", "run MUC JID the decision stanza is posted to")
	fs.StringVar(&o.parent, "parent", "", "provenance parent stanza id (the recording)")
	fs.StringVar(&o.question, "question", "", "question id to gate on (default: the single question)")
	fs.StringVar(&o.troupe, "troupe", "troupe", "path to the troupe binary")
	fs.Float64Var(&o.minConfidence, "min-confidence", jd.DefaultMinConfidence, "usable confidence threshold in [0,1]")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch {
	case o.model == "":
		return o, errors.New("--model is required")
	case o.room == "":
		return o, errors.New("--room is required")
	case o.parent == "":
		return o, errors.New("--parent is required")
	case o.minConfidence < 0 || o.minConfidence > 1:
		return o, fmt.Errorf("--min-confidence %v out of range [0,1]", o.minConfidence)
	}
	return o, nil
}

// cmdDecide is `juggler decide`: resolve, decide, post the stanza, print the
// answers, exit per FDR 0019 §1's table.
func cmdDecide(resolver decideResolver, httpClient *http.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseDecideFlags(args, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "juggler: decide: %v\n%s\n", err, decideUsage)
		}
		return jd.ExitUsageConfig
	}
	payload, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "juggler: decide: reading stdin: %v\n", err)
		return jd.ExitUsageConfig
	}

	ctx, cancel := context.WithTimeout(context.Background(), decideTimeout)
	defer cancel()

	model, err := resolver.ResolveDecisionsModel(ctx, opts.model)
	if err != nil {
		fmt.Fprintf(stderr, "juggler: decide: %v\n", err)
		return jd.ExitUsageConfig
	}

	out, err := jd.Decide(ctx, jd.Request{
		HTTPClient:    httpClient,
		Endpoint:      jd.EndpointFor(model.URL),
		Token:         model.Token,
		Model:         opts.model,
		Payload:       payload,
		MinConfidence: opts.minConfidence,
		Question:      opts.question,
	})
	if err != nil {
		fmt.Fprintf(stderr, "juggler: decide: %v\n", err)
		return jd.ExitUsageConfig
	}

	// The stanza records every outcome, including a no-choice attempt.
	stanza, err := out.Stanza(opts.parent).Marshal()
	if err != nil {
		fmt.Fprintf(stderr, "juggler: decide: marshal stanza: %v\n", err)
		return jd.ExitUsageConfig
	}
	postErr := postDecisionStanza(opts.troupe, opts.room, stanza, stderr)

	if line, err := out.StdoutJSON(); err == nil {
		fmt.Fprintln(stdout, string(line))
	}
	if postErr != nil {
		fmt.Fprintf(stderr, "juggler: decide: posting stanza to %s: %v\n", opts.room, postErr)
		return jd.ExitRoomPostFailed
	}
	return out.Verdict.ExitCode()
}

// mucSendArgv is the `troupe muc send` argv for a decision stanza. The shape
// is clown-hook-tee's spawnSend verbatim (--room, --subject, --body,
// --source). The stanza JSON has no blank line, so tee's subject/body split
// puts all of it in --subject with an empty --body, and troupe's wire body
// is then exactly the stanza JSON (no "\n\n" suffix to break parsing).
func mucSendArgv(troupe, room string, stanza []byte) []string {
	return []string{
		troupe, "muc", "send",
		"--room", room,
		"--subject", string(stanza),
		"--body", "",
		"--source", "juggler-decide",
	}
}

// postDecisionStanza runs `troupe muc send` synchronously with the ambient
// environment (TROUPE_XMPP_USER/PASSWORD_FILE/DOMAIN, nick resolution), like
// the hook tee but waiting so a failed post is detectable.
func postDecisionStanza(troupe, room string, stanza []byte, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), decidePostTimeout)
	defer cancel()
	argv := mucSendArgv(troupe, room, stanza)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s muc send: %w", troupe, err)
	}
	return nil
}
