package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// TestSendPrompt_DecisionsStyleRejected pins FDR 0019 §8: the "decisions"
// style belongs to `juggler decide` only, so `juggler prompt` must error
// clearly and never make an HTTP call.
func TestSendPrompt_DecisionsStyleRejected(t *testing.T) {
	var called int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
	}))
	defer srv.Close()

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL, Token: "t", Style: rm.StyleDecisions}
	_, err := sendPrompt(context.Background(), srv.Client(), resolved, "m", "p", 256)
	if err == nil || !strings.Contains(err.Error(), "decisions") {
		t.Fatalf("error = %v, want one naming the decisions style", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("HTTP handler invoked %d times", called)
	}
}
