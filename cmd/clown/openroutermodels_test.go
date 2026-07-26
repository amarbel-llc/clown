package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fixtureModelsJSON = `{"data":[` +
	`{"id":"openai/gpt-4o","context_length":128000,"description":"OpenAI's flagship multimodal model.","pricing":{"prompt":"0.0000025","completion":"0.00001"}},` +
	`{"id":"anthropic/claude-3.5-sonnet","context_length":200000,"description":"Anthropic's mid-tier model.","pricing":{"prompt":"0.000003","completion":"0.000015"}}` +
	`],"total_count":2,"links":{"next":null}}`

func TestFetchOpenRouterModelsFrom_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixtureModelsJSON))
	}))
	defer srv.Close()

	models, err := fetchOpenRouterModelsFrom(context.Background(), srv.URL, "test-token")
	if err != nil {
		t.Fatalf("fetchOpenRouterModelsFrom: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	got := models[0]
	if got.ID != "openai/gpt-4o" || got.ContextLen != 128000 {
		t.Errorf("models[0] = %+v", got)
	}
	if got.PromptPrice != 0.0000025 || got.CompPrice != 0.00001 {
		t.Errorf("models[0] pricing = %+v", got)
	}
}

func TestFetchOpenRouterModelsFrom_Errors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			"non-200 status",
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		},
		{
			"malformed JSON body",
			func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{not json")) },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()

			if _, err := fetchOpenRouterModelsFrom(context.Background(), srv.URL, "token"); err == nil {
				t.Fatalf("expected error for %s, got nil", c.name)
			}
		})
	}
}
