package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

const decideTestPayload = `{"state":{"t":"x"},"questions":{"filer":{"type":"choice","instructions":"?","criteria":{"issue":"a","note":"b"}}}}`

type fakeResolver struct {
	model decideModel
	err   error
}

func (f fakeResolver) ResolveDecisionsModel(context.Context, string) (decideModel, error) {
	return f.model, f.err
}

// fakeTroupe writes a script that records its argv (one arg per line, NUL-free
// test data) and $TROUPE_XMPP_USER into dir, then exits with code.
func fakeTroupe(t *testing.T, code int) (bin, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "troupe")
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do printf '%%s\\n' \"$a\" >> %q; done\nprintf 'user=%%s\\n' \"$TROUPE_XMPP_USER\" >> %q\nexit %d\n", argvFile, argvFile, code)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

func runDecideTest(t *testing.T, body string, status int, troupeCode int, extra ...string) (code int, stdout, argv string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	bin, argvFile := fakeTroupe(t, troupeCode)
	t.Setenv("TROUPE_XMPP_USER", "root-user")
	args := append([]string{"--model", "jev", "--room", "run@rooms.x", "--parent", "rec-1", "--troupe", bin}, extra...)
	var out, errb bytes.Buffer
	code = cmdDecide(fakeResolver{model: decideModel{URL: srv.URL, Token: "tok"}}, srv.Client(), args, strings.NewReader(decideTestPayload), &out, &errb)
	data, _ := os.ReadFile(argvFile)
	return code, out.String(), string(data)
}

const decideOK = `{"answers":{"filer":{"type":"choice","choice":"issue","confidence":0.81}},"id":"g1","model":"m"}`

func TestDecideExitCodes(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		status     int
		troupeCode int
		extra      []string
		want       int
	}{
		{"usable", decideOK, 200, 0, nil, 0},
		{"below threshold", decideOK, 200, 0, []string{"--min-confidence", "0.95"}, 4},
		{"no choice 500", "boom", 500, 0, nil, 2},
		{"no choice bad option", `{"answers":{"filer":{"type":"choice","choice":"zzz","confidence":1}}}`, 200, 0, nil, 2},
		{"post failed", decideOK, 200, 1, nil, 3},
		{"post failed on no choice", "boom", 500, 1, nil, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, argv := runDecideTest(t, tc.body, tc.status, tc.troupeCode, tc.extra...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (stdout %s)", code, tc.want, stdout)
			}
			// The stanza post is attempted for every outcome, including no-choice.
			if !strings.Contains(argv, "muc\nsend\n") {
				t.Errorf("troupe not invoked: %q", argv)
			}
			if !json.Valid([]byte(strings.TrimSpace(stdout))) {
				t.Errorf("stdout not JSON: %q", stdout)
			}
		})
	}
}

func TestDecideMucSendArgvAndEnv(t *testing.T) {
	code, stdout, argv := runDecideTest(t, decideOK, 200, 0)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	lines := strings.Split(strings.TrimSuffix(argv, "\n"), "\n")
	// muc send --room R --subject <stanza> --body "" --source juggler-decide, then env probe.
	if len(lines) != 11 {
		t.Fatalf("argv lines = %d: %q", len(lines), lines)
	}
	if !reflect.DeepEqual(lines[:4], []string{"muc", "send", "--room", "run@rooms.x"}) ||
		lines[4] != "--subject" || lines[6] != "--body" || lines[7] != "" ||
		lines[8] != "--source" || lines[9] != "juggler-decide" {
		t.Errorf("argv = %q", lines)
	}
	var stanza map[string]any
	if err := json.Unmarshal([]byte(lines[5]), &stanza); err != nil {
		t.Fatalf("subject not stanza JSON: %v", err)
	}
	if stanza["type"] != "decision" || stanza["parent"] != "rec-1" || stanza["verdict"] != "usable" {
		t.Errorf("stanza = %v", stanza)
	}
	if lines[10] != "user=root-user" {
		t.Errorf("ambient env not inherited: %q", lines[10])
	}
	var printed map[string]any
	if err := json.Unmarshal([]byte(stdout), &printed); err != nil || printed["id"] != "g1" || printed["answers"] == nil {
		t.Errorf("stdout = %q (%v)", stdout, err)
	}
}

func TestMucSendArgvShape(t *testing.T) {
	got := mucSendArgv("/bin/troupe", "r@x", []byte(`{"a":1}`))
	want := []string{"/bin/troupe", "muc", "send", "--room", "r@x", "--subject", `{"a":1}`, "--body", "", "--source", "juggler-decide"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q", got)
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
			if code := cmdDecide(fakeResolver{}, nil, args, strings.NewReader("{}"), &out, &errb); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
	}
}

func TestDecisionsModelFrom(t *testing.T) {
	if _, err := decisionsModelFrom("m", rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: "u", Token: "t", Style: "decisions"}); err != nil {
		t.Errorf("decisions style: %v", err)
	}
	_, err := decisionsModelFrom("m", rm.ResolveModelResult{Kind: rm.ModelKindRemote, Style: "openai-compat"})
	if err == nil || !strings.Contains(err.Error(), "openai-compat") {
		t.Errorf("want error naming style, got %v", err)
	}
	if _, err := decisionsModelFrom("m", rm.ResolveModelResult{Kind: rm.ModelKindLocal}); err == nil {
		t.Error("local must be rejected")
	}
}

func TestDecideResolveFailureIsConfigError(t *testing.T) {
	var out, errb bytes.Buffer
	args := []string{"--model", "m", "--room", "r", "--parent", "p"}
	code := cmdDecide(fakeResolver{err: fmt.Errorf("nope")}, nil, args, strings.NewReader(decideTestPayload), &out, &errb)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}
