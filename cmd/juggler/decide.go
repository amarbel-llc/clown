package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	jd "code.linenisgreat.com/clown/internal/jugglerdecide"
	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const decideUsage = "usage: juggler decide --model <name> --room <muc-jid> --parent <stanza-id> [--min-confidence 0.5] [--question <id>] [--run-key <key>] [--troupe <path>]  (payload {\"state\",\"questions\"} on stdin)"

// decideTimeout spans model resolution and the Decisions call; the stanza
// post gets its own budget of the same length, so a timed-out call is still
// recorded in the room.
const decideTimeout = 60 * time.Second

// decideSource is the troupe source tag of the decision stanza.
const decideSource = "juggler-decide"

// decideOpts is the parsed flag set.
type decideOpts struct {
	bins          platformBins
	model         string
	room          string
	parent        string
	question      string
	runKey        string
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
	fs.StringVar(&o.runKey, "run-key", "", "record the outcome as a route entry in this run's ledger (juggler spawn --new-run's key)")
	fs.Float64Var(&o.minConfidence, "min-confidence", jd.DefaultMinConfidence, "usable confidence threshold in [0,1]")
	o.bins.registerTroupe(fs)
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

// cmdDecide is `juggler decide`: resolve (daemon-free for the remote entry
// decide needs, as `juggler run` does), decide, post the stanza, print the
// answers, exit per FDR 0019 §1's table.
func cmdDecide(resolve func(context.Context, string) (rm.ResolveModelResult, error), httpClient *http.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseDecideFlags(args, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "juggler: decide: %v\n%s\n", err, decideUsage)
		}
		return jr.ExitUsage
	}
	payload, err := io.ReadAll(stdin)
	if err != nil {
		return fail(stderr, "decide", fmt.Errorf("reading stdin: %w", err))
	}
	var store jr.Store
	if opts.runKey != "" {
		// Fail before any call when the run does not exist.
		if store, err = jr.DefaultStore(); err == nil {
			var run *jr.RunRecord
			if run, err = store.LoadRun(opts.runKey); err == nil && run == nil {
				err = fmt.Errorf("no run with key %q", opts.runKey)
			}
		}
		if err != nil {
			return fail(stderr, "decide", fmt.Errorf("--run-key: %w", err))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), decideTimeout)
	defer cancel()

	model, err := resolve(ctx, opts.model)
	if err != nil {
		return fail(stderr, "decide", fmt.Errorf("resolve model %q: %w", opts.model, err))
	}
	if model.Kind != rm.ModelKindRemote || model.Style != rm.StyleDecisions {
		return fail(stderr, "decide", fmt.Errorf("model %q is %s with style %q; decide requires a remote model with style %q", opts.model, model.Kind, model.Style, rm.StyleDecisions))
	}

	out, err := jd.Decide(ctx, jd.Request{
		HTTPClient:    httpClient,
		Endpoint:      jd.EndpointFor(model.URL),
		Token:         model.Token,
		Model:         model.UpstreamModel(opts.model),
		Payload:       payload,
		MinConfidence: opts.minConfidence,
		Question:      opts.question,
	})
	if err != nil {
		return fail(stderr, "decide", err)
	}

	// The stanza records every outcome, including a no-choice attempt, with
	// the ambient TROUPE_XMPP_* identity (troupe muc send's stdout is its id).
	stanza, err := out.Stanza(opts.parent).Marshal()
	if err != nil {
		return fail(stderr, "decide", fmt.Errorf("marshal stanza: %w", err))
	}
	postCtx, cancelPost := context.WithTimeout(context.Background(), decideTimeout)
	defer cancelPost()
	stanzaID, postErr := opts.bins.troupeClient().PostStanza(postCtx, jr.Identity{}, opts.room, decideSource, stanza)

	if line, err := out.StdoutJSON(); err == nil {
		fmt.Fprintln(stdout, string(line))
	}
	if opts.runKey != "" {
		// The lifecycle owner's route entry (FDR 0019 §1, §4), so the glue
		// never writes ledger entries itself.
		entry := jr.RouteEntry(jr.RouteDecision{
			Choice: out.Choice, Confidence: out.Confidence, TopProbability: out.TopProbability,
			Threshold: out.Threshold, Verdict: string(out.Verdict), Reason: out.Reason, StanzaID: stanzaID,
		})
		if err := jr.AppendRunLedgerEntry(store, opts.runKey, entry); err != nil {
			return fail(stderr, "decide", fmt.Errorf("recording the route entry: %w", err))
		}
	}
	if postErr != nil {
		fmt.Fprintf(stderr, "juggler: decide: posting stanza to %s: %v\n", opts.room, postErr)
		return jd.ExitRoomPostFailed
	}
	return out.Verdict.ExitCode()
}
