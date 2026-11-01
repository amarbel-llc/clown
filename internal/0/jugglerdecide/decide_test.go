package jugglerdecide

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPayload = `{"state":{"b":2,"a":1},"questions":{"filer":{"type":"choice","instructions":"which?","criteria":{"issue":"eng item","note":"other"}}}}`

func decisionsServer(t *testing.T, status int, body string, gotBody *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != DefaultPath {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if gotBody != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, gotBody)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func decide(t *testing.T, srv *httptest.Server, min float64, question string) Outcome {
	t.Helper()
	out, err := Decide(context.Background(), Request{
		HTTPClient: srv.Client(), Endpoint: EndpointFor(srv.URL), Token: "tok",
		Model: "typesafe/jev-1.13", Payload: []byte(testPayload),
		MinConfidence: min, Question: question,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return out
}

const okBody = `{"answers":{"filer":{"type":"choice","choice":"issue","confidence":0.81,"probabilities":{"issue":0.86,"note":0.14}}},"id":"gen-dec-1","model":"typesafe/jev-1.13","provider":"p","usage":{"cost":0.001,"input_tokens":10,"output_tokens":2}}`

func TestDecideUsable(t *testing.T) {
	var got map[string]any
	srv := decisionsServer(t, 200, okBody, &got)
	defer srv.Close()
	out := decide(t, srv, 0.5, "")
	if out.Verdict != Usable || out.Choice != "issue" || out.Confidence != 0.81 {
		t.Fatalf("outcome = %+v", out)
	}
	if got["model"] != "typesafe/jev-1.13" || got["questions"] == nil || got["state"] == nil {
		t.Errorf("request body = %v", got)
	}
	if out.Verdict.ExitCode() != ExitUsable {
		t.Errorf("exit = %d", out.Verdict.ExitCode())
	}
}

func TestDecideBelowThreshold(t *testing.T) {
	srv := decisionsServer(t, 200, okBody, nil)
	defer srv.Close()
	out := decide(t, srv, 0.9, "filer")
	if out.Verdict != BelowThreshold || out.Choice != "issue" || out.Threshold != 0.9 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Verdict.ExitCode() != ExitBelowThreshold {
		t.Errorf("exit = %d", out.Verdict.ExitCode())
	}
}

func TestDecideExactlyAtThresholdIsUsable(t *testing.T) {
	srv := decisionsServer(t, 200, okBody, nil)
	defer srv.Close()
	if out := decide(t, srv, 0.81, ""); out.Verdict != Usable {
		t.Fatalf("verdict = %s", out.Verdict)
	}
}

func TestDecideNoChoice(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"unknown option": {200, `{"answers":{"filer":{"type":"choice","choice":"bogus","confidence":0.99}}}`},
		"http 500":       {500, `boom`},
		"malformed json": {200, `{not json`},
		"missing answer": {200, `{"answers":{}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := decisionsServer(t, tc.status, tc.body, nil)
			defer srv.Close()
			out := decide(t, srv, 0.5, "")
			if out.Verdict != NoChoice || out.Reason == "" {
				t.Fatalf("outcome = %+v", out)
			}
			if out.Verdict.ExitCode() != ExitNoChoice {
				t.Errorf("exit = %d", out.Verdict.ExitCode())
			}
			if tc.status == 500 && out.HTTPStatus != 500 {
				t.Errorf("HTTPStatus = %d", out.HTTPStatus)
			}
			if _, err := out.StdoutJSON(); err != nil {
				t.Errorf("StdoutJSON: %v", err)
			}
			if _, err := out.Stanza("p").Marshal(); err != nil {
				t.Errorf("stanza: %v", err)
			}
		})
	}
}

func TestDecideConfigErrors(t *testing.T) {
	multi := `{"state":1,"questions":{"a":{"type":"choice","criteria":{"x":"y"}},"b":{"type":"choice","criteria":{"x":"y"}}}}`
	for name, p := range map[string]string{
		"not json":       `nope`,
		"no questions":   `{"state":1}`,
		"multi, no flag": multi,
		"non-choice":     `{"state":1,"questions":{"a":{"type":"score"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decide(context.Background(), Request{Payload: []byte(p)})
			if err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestStateDigestIsCanonical(t *testing.T) {
	a, _ := StateDigest(json.RawMessage(`{"b":2,"a":1}`))
	b, _ := StateDigest(json.RawMessage(`{ "a": 1, "b": 2 }`))
	if a != b || len(a) != 64 {
		t.Errorf("digests differ or wrong length: %s %s", a, b)
	}
}

func TestEndpointFor(t *testing.T) {
	for in, want := range map[string]string{
		"https://openrouter.ai":                     "https://openrouter.ai/api/alpha/decisions",
		"https://openrouter.ai/":                    "https://openrouter.ai/api/alpha/decisions",
		"https://openrouter.ai/api/alpha/decisions": "https://openrouter.ai/api/alpha/decisions",
	} {
		if got := EndpointFor(in); got != want {
			t.Errorf("EndpointFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStanzaGolden(t *testing.T) {
	srv := decisionsServer(t, 200, okBody, nil)
	defer srv.Close()
	out := decide(t, srv, 0.5, "")
	b, err := out.Stanza("stanza-42").Marshal()
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := StateDigest(json.RawMessage(`{"a":1,"b":2}`))
	want := `{"schema":1,"type":"decision","parent":"stanza-42","model":"typesafe/jev-1.13","state_digest":"` + digest +
		`","questions":{"filer":{"type":"choice","instructions":"which?","criteria":{"issue":"eng item","note":"other"}}},` +
		`"answers":{"filer":{"type":"choice","choice":"issue","confidence":0.81,"probabilities":{"issue":0.86,"note":0.14}}},` +
		`"threshold":0.5,"top_probability":0.86,"verdict":"usable","reason":"confidence 0.81 >= threshold 0.5","usage":{"cost":0.001,"input_tokens":10,"output_tokens":2}}`
	if string(b) != want {
		t.Errorf("stanza mismatch\n got: %s\nwant: %s", b, want)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decideFixture(t *testing.T, status int, reqFile, respFile string) Outcome {
	t.Helper()
	srv := decisionsServer(t, status, fixture(t, respFile), nil)
	defer srv.Close()
	// The recorded request (model included) is a valid payload: unknown keys are ignored.
	out, err := Decide(context.Background(), Request{
		HTTPClient: srv.Client(), Endpoint: EndpointFor(srv.URL), Token: "tok",
		Model: "typesafe/jev-1.13", Payload: []byte(fixture(t, reqFile)), MinConfidence: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLiveSampleIntegerFormsUsable(t *testing.T) {
	out := decideFixture(t, 200, "request1.json", "response1.json")
	if out.Verdict != Usable || out.Verdict.ExitCode() != ExitUsable {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Confidence != 1 || out.TopProbability == nil || *out.TopProbability != 1 {
		t.Errorf("confidence=%v top=%v", out.Confidence, out.TopProbability)
	}
	if out.Response.ID != "gen-dec-1791295458-rp2o9IEPrRcc9NU9vRnZ" || out.Response.Provider != "TypeSafe" ||
		out.Response.Model != "typesafe/jev-1.13-20260917" || out.Response.Usage == nil ||
		out.Response.Usage.InputTokens != 362 || out.Response.Usage.Cost != 0.000015204 {
		t.Errorf("response = %+v", out.Response)
	}
}

func TestLiveSampleMarginConfidenceBelowThreshold(t *testing.T) {
	out := decideFixture(t, 200, "request2.json", "response2.json")
	if out.Verdict != BelowThreshold || out.Verdict.ExitCode() != ExitBelowThreshold {
		t.Fatalf("outcome = %+v", out)
	}
	// Gated on the API's confidence (0.26), not the winning probability (0.63).
	if out.Confidence != 0.26 || out.TopProbability == nil || *out.TopProbability != 0.63 {
		t.Errorf("confidence=%v top=%v", out.Confidence, out.TopProbability)
	}
	st := out.Stanza("p")
	if st.TopProbability == nil || *st.TopProbability != 0.63 {
		t.Errorf("stanza top_probability = %v", st.TopProbability)
	}
	b, _ := st.Marshal()
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil || back["top_probability"] != 0.63 || back["verdict"] != "below-threshold" {
		t.Errorf("stanza = %s (%v)", b, err)
	}
}

func TestLiveSampleErrorShapes(t *testing.T) {
	for file, tc := range map[string]struct {
		status int
		msg    string
	}{
		"error400.json": {400, "is a decisions model and cannot be used with the chat/completions endpoint"},
		"error401.json": {401, "User not found."},
	} {
		t.Run(file, func(t *testing.T) {
			out := decideFixture(t, tc.status, "request1.json", file)
			if out.Verdict != NoChoice || out.Verdict.ExitCode() != ExitNoChoice || out.HTTPStatus != tc.status {
				t.Fatalf("outcome = %+v", out)
			}
			if !strings.Contains(out.Reason, tc.msg) {
				t.Errorf("reason = %q", out.Reason)
			}
			b, _ := out.StdoutJSON()
			var o struct {
				Reason     string `json:"reason"`
				HTTPStatus int    `json:"http_status"`
			}
			if err := json.Unmarshal(b, &o); err != nil || o.HTTPStatus != tc.status || !strings.Contains(o.Reason, tc.msg) {
				t.Errorf("stdout = %s (%v)", b, err)
			}
		})
	}
}

func TestExitCodeConstants(t *testing.T) {
	if ExitUsable != 0 || ExitUsageConfig != 1 || ExitNoChoice != 2 || ExitRoomPostFailed != 3 || ExitBelowThreshold != 4 {
		t.Error("exit constants drifted from FDR 0019 §1")
	}
}
