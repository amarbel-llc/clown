package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglerrun/jugglerruntest"
)

const decideTestPayload = `{"state":{"t":"x"},"questions":{"filer":{"type":"choice","instructions":"?","criteria":{"issue":"a","note":"b"}}}}`

// resolvedAs is a model resolver that always answers r.
func resolvedAs(r rm.ResolveModelResult) func(context.Context, string) (rm.ResolveModelResult, error) {
	return func(context.Context, string) (rm.ResolveModelResult, error) { return r, nil }
}

// decisionsAt resolves every name to a decisions-style entry at url.
func decisionsAt(url string) func(context.Context, string) (rm.ResolveModelResult, error) {
	return resolvedAs(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: url, Token: "tok", Style: rm.StyleDecisions})
}

// mustNotResolve fails the test if decide reaches model resolution.
func mustNotResolve(t *testing.T) func(context.Context, string) (rm.ResolveModelResult, error) {
	return func(context.Context, string) (rm.ResolveModelResult, error) {
		t.Error("resolution must not be reached")
		return rm.ResolveModelResult{}, errors.New("unreachable")
	}
}

// runDecideTest runs decide against an endpoint answering status/body, with
// the fake troupe (JUGGLER_TROUPE_BIN) failing `muc send` when postFails.
func runDecideTest(t *testing.T, body string, status int, postFails bool, extra ...string) (code int, stdout string, f *jugglerruntest.Fakes) {
	t.Helper()
	f = lifecycleEnv(t)
	if postFails {
		f.Fail(t, "troupe", "muc")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TROUPE_XMPP_USER", "root-user")
	args := append([]string{"--model", "jev", "--room", "run@rooms.x", "--parent", "rec-1"}, extra...)
	var out, errb bytes.Buffer
	code = cmdDecide(decisionsAt(srv.URL), srv.Client(), args, strings.NewReader(decideTestPayload), &out, &errb)
	return code, out.String(), f
}

const decideOK = `{"answers":{"filer":{"type":"choice","choice":"issue","confidence":0.81}},"id":"g1","model":"m"}`

func TestDecideExitCodes(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		status    int
		postFails bool
		extra     []string
		want      int
	}{
		{"usable", decideOK, 200, false, nil, 0},
		{"below threshold", decideOK, 200, false, []string{"--min-confidence", "0.95"}, 4},
		{"no choice 500", "boom", 500, false, nil, 2},
		{"no choice bad option", `{"answers":{"filer":{"type":"choice","choice":"zzz","confidence":1}}}`, 200, false, nil, 2},
		{"post failed", decideOK, 200, true, nil, 3},
		{"post failed on no choice", "boom", 500, true, nil, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, f := runDecideTest(t, tc.body, tc.status, tc.postFails, tc.extra...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (stdout %s)", code, tc.want, stdout)
			}
			// The stanza post is attempted for every outcome, including no-choice.
			if calls := f.Calls(t, "troupe"); len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv[:2], []string{"muc", "send"}) {
				t.Errorf("troupe calls = %+v", calls)
			}
			if !json.Valid([]byte(strings.TrimSpace(stdout))) {
				t.Errorf("stdout not JSON: %q", stdout)
			}
		})
	}
}

func TestDecideMucSendArgvAndEnv(t *testing.T) {
	code, stdout, f := runDecideTest(t, decideOK, 200, false)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	calls := f.Calls(t, "troupe")
	if len(calls) != 1 {
		t.Fatalf("troupe calls = %+v", calls)
	}
	argv := calls[0].Argv
	// muc send --room R --subject <stanza> --body "" --source juggler-decide
	if len(argv) != 10 || !reflect.DeepEqual(argv[:5], []string{"muc", "send", "--room", "run@rooms.x", "--subject"}) ||
		!reflect.DeepEqual(argv[6:], []string{"--body", "", "--source", "juggler-decide"}) {
		t.Errorf("argv = %q", argv)
	}
	var stanza map[string]any
	if err := json.Unmarshal([]byte(argv[5]), &stanza); err != nil {
		t.Fatalf("subject not stanza JSON: %v", err)
	}
	if stanza["type"] != "decision" || stanza["parent"] != "rec-1" || stanza["verdict"] != "usable" {
		t.Errorf("stanza = %v", stanza)
	}
	if calls[0].Env["TROUPE_XMPP_USER"] != "root-user" {
		t.Errorf("ambient env not inherited: %v", calls[0].Env)
	}
	var printed map[string]any
	if err := json.Unmarshal([]byte(stdout), &printed); err != nil || printed["id"] != "g1" || printed["answers"] == nil {
		t.Errorf("stdout = %q (%v)", stdout, err)
	}
}

func TestDecideUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"no model":      {"--room", "r", "--parent", "p"},
		"no room":       {"--model", "m", "--parent", "p"},
		"no parent":     {"--model", "m", "--room", "r"},
		"bad threshold": {"--model", "m", "--room", "r", "--parent", "p", "--min-confidence", "2"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := cmdDecide(mustNotResolve(t), nil, args, strings.NewReader("{}"), &out, &errb); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

func TestDecideRequiresARemoteDecisionsModel(t *testing.T) {
	lifecycleEnv(t)
	args := []string{"--model", "m", "--room", "r", "--parent", "p"}
	for name, r := range map[string]rm.ResolveModelResult{
		"openai-compat style": {Kind: rm.ModelKindRemote, Style: rm.StyleOpenAICompat},
		"local":               {Kind: rm.ModelKindLocal},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := cmdDecide(resolvedAs(r), nil, args, strings.NewReader(decideTestPayload), &out, &errb); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if !strings.Contains(errb.String(), `"`+r.Style+`"`) || !strings.Contains(errb.String(), rm.StyleDecisions) {
				t.Errorf("stderr must name the style: %s", errb.String())
			}
		})
	}
}

func TestDecideSendsTheUpstreamModelID(t *testing.T) {
	lifecycleEnv(t)
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = body.Model
		_, _ = io.WriteString(w, decideOK)
	}))
	t.Cleanup(srv.Close)
	args := []string{"--model", "jev", "--room", "r@x", "--parent", "p"}
	resolve := resolvedAs(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL, Style: rm.StyleDecisions, ModelID: "typesafe/jev-1.13"})
	var out, errb bytes.Buffer
	if code := cmdDecide(resolve, srv.Client(), args, strings.NewReader(decideTestPayload), &out, &errb); code != 0 {
		t.Fatalf("exit = %d: %s", code, errb.String())
	}
	if sent != "typesafe/jev-1.13" {
		t.Errorf("request model = %q, want the upstream id", sent)
	}
	if got := (rm.ResolveModelResult{}).UpstreamModel("jev"); got != "jev" {
		t.Errorf("no ModelID must send the registry name: %q", got)
	}
}

func TestDecideResolveFailureIsConfigError(t *testing.T) {
	var out, errb bytes.Buffer
	args := []string{"--model", "m", "--room", "r", "--parent", "p"}
	failing := func(context.Context, string) (rm.ResolveModelResult, error) {
		return rm.ResolveModelResult{}, errors.New("nope")
	}
	if code := cmdDecide(failing, nil, args, strings.NewReader(decideTestPayload), &out, &errb); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}
