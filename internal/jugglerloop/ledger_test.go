package jugglerloop

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// fdrLedgerExample is the ledger from FDR 0019's Examples section.
const fdrLedgerExample = `{
  "schema": 1,
  "calls": [
    {"tool": "list_repos",   "kind": null,    "ok": true,  "uris": []},
    {"tool": "create_issue", "kind": "issue", "ok": true,
     "uris": ["https://code.example/clown/issues/301"]}
  ],
  "end": {"reason": "end_turn"},
  "cannot_complete": null,
  "steps": 3,
  "elapsed_ms": 18340
}`

func compactJSON(t *testing.T, s []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, s); err != nil {
		t.Fatalf("compact %s: %v", s, err)
	}
	return buf.String()
}

func TestLedger_MatchesFDRExample(t *testing.T) {
	issue := "issue"
	l := newLedger()
	l.Calls = append(l.Calls,
		LedgerCall{Tool: "list_repos", OK: true, URIs: []string{}},
		LedgerCall{Tool: "create_issue", Kind: &issue, OK: true, URIs: []string{"https://code.example/clown/issues/301"}},
	)
	l.End = LedgerEnd{Reason: EndTurn}
	l.Steps = 3
	l.ElapsedMS = 18340

	got, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := compactJSON(t, got), compactJSON(t, []byte(fdrLedgerExample)); g != w {
		t.Errorf("ledger JSON\n got %s\nwant %s", g, w)
	}
}

func TestLedger_CannotCompleteAndErrorFields(t *testing.T) {
	l := newLedger()
	l.Calls = append(l.Calls, LedgerCall{Tool: "create_issue", URIs: []string{}, Error: "denied"})
	l.End = LedgerEnd{Reason: EndCannotComplete}
	l.CannotComplete = &CannotComplete{Reason: "no repo matches"}
	l.Steps = 1

	got, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":1,"calls":[{"tool":"create_issue","kind":null,"ok":false,"uris":[],"error":"denied"}],"end":{"reason":"cannot_complete"},"cannot_complete":{"reason":"no repo matches"},"steps":1,"elapsed_ms":0}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestLedger_EmptyCallsSerialiseAsArray(t *testing.T) {
	got, _ := json.Marshal(newLedger())
	if !bytes.Contains(got, []byte(`"calls":[]`)) {
		t.Errorf("empty ledger = %s, want calls: []", got)
	}
}

func TestExtractURIsFromTopLevelFields(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"uri", `{"uri":"madder://blobs/x"}`, []string{"madder://blobs/x"}},
		{"url", `{"url":"https://a/1","other":1}`, []string{"https://a/1"}},
		{"arrays", `{"uris":["a","b"],"urls":["c",3]}`, []string{"a", "b", "c"}},
		{"all in order", `{"urls":["d"],"uris":["c"],"url":"b","uri":"a"}`, []string{"a", "b", "c", "d"}},
		{"nested ignored", `{"result":{"url":"https://a"}}`, []string{}},
		{"non-object", `["https://a"]`, []string{}},
		{"string", `"https://a"`, []string{}},
		{"non-string uri", `{"uri":5}`, []string{}},
		{"empty", ``, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractURIsFromTopLevelFields("tool", json.RawMessage(tc.content))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}
