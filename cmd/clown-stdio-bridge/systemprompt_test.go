package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
)

// runPromptChild wires a translator to a fake child that answers prompts/get.
// When fragment is non-empty the child returns it as a text message for the
// well-known childPromptName; otherwise (or for any other name) it returns a
// JSON-RPC error, exercising the 204 path.
func runPromptChild(t *testing.T, fragment string) (*translator, func()) {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = tr.Run(ctx) }()
	go serveFakePromptChild(stdinR, stdoutW, fragment)
	cleanup := func() {
		cancel()
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}
	return tr, cleanup
}

func serveFakePromptChild(in io.Reader, out io.Writer, fragment string) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if req.Method != "prompts/get" {
			continue
		}
		var resp map[string]any
		if fragment != "" && req.Params.Name == childPromptName {
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"messages": []map[string]any{{
					"role":    "user",
					"content": map[string]any{"type": "text", "text": fragment},
				}},
			}}
		} else {
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32602, "message": "unknown prompt"}}
		}
		b, _ := json.Marshal(resp)
		_, _ = out.Write(append(b, '\n'))
	}
}

func TestFetchChildSystemPrompt(t *testing.T) {
	tr, cleanup := runPromptChild(t, "## live fragment\n\ndetail line")
	defer cleanup()
	text, ok := fetchChildSystemPrompt(context.Background(), tr)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if text != "## live fragment\n\ndetail line" {
		t.Errorf("text = %q", text)
	}
}

func TestFetchChildSystemPromptChildErrors(t *testing.T) {
	tr, cleanup := runPromptChild(t, "") // child errors on prompts/get
	defer cleanup()
	if text, ok := fetchChildSystemPrompt(context.Background(), tr); ok || text != "" {
		t.Errorf("want (\"\", false) when child errors, got (%q, %v)", text, ok)
	}
}

func TestParsePromptGetText(t *testing.T) {
	cases := []struct {
		name     string
		resp     string
		wantText string
		wantOK   bool
	}{
		{"single text message",
			`{"result":{"messages":[{"role":"user","content":{"type":"text","text":"hello"}}]}}`,
			"hello", true},
		{"two text messages joined",
			`{"result":{"messages":[{"content":{"type":"text","text":"a"}},{"content":{"type":"text","text":"b"}}]}}`,
			"a\n\nb", true},
		{"non-text content skipped",
			`{"result":{"messages":[{"content":{"type":"image","text":""}}]}}`,
			"", false},
		{"error envelope",
			`{"error":{"code":-32602,"message":"unknown prompt"}}`,
			"", false},
		{"empty messages",
			`{"result":{"messages":[]}}`,
			"", false},
		{"malformed json",
			`{not json`,
			"", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, ok := parsePromptGetText(json.RawMessage(c.resp))
			if text != c.wantText || ok != c.wantOK {
				t.Errorf("got (%q, %v), want (%q, %v)", text, ok, c.wantText, c.wantOK)
			}
		})
	}
}
