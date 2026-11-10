package catalogsnap

import (
	"encoding/json"
	"testing"
)

// Every id becomes one URI segment (cutting-garden RFC 0018 §3): an empty
// name, or a dot segment a URI normalizer would collapse, cannot round-trip,
// so such a name is not presented.
func TestNamesThatCannotBeOneURISegmentAreNotPresented(t *testing.T) {
	s := Snapshot{Principal: "p", Servers: []Server{
		{Plugin: "moxy", Server: "moxy", Tools: []Tool{
			{Name: "folio..", InputSchema: json.RawMessage(`{}`)}, // tool "."
			{Name: "folio.read", InputSchema: json.RawMessage(`{"properties":{"":{},"..":{},".":{},"ok":{}}}`)},
		}},
		{Plugin: "x", Server: "", Tools: []Tool{{Name: "t"}}},
		{Plugin: "y", Server: "y", Tools: []Tool{{Name: ""}, {Name: ".."}}},
	}}
	got := Present(s, contract(t))
	if len(got) != 1 || got[0].Plugin+"/"+got[0].Server+"/"+got[0].Tool != "moxy/folio/read" {
		t.Fatalf("Present = %+v, want only moxy/folio/read", got)
	}
	if len(got[0].Inputs) != 1 || got[0].Inputs[0].Name != "ok" {
		t.Errorf("inputs %+v, want only ok", got[0].Inputs)
	}
}
