package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func codeFor(t *testing.T, h http.Handler, path, authorization string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// With a token set, every route but /healthz refuses a request without it
// (clown#288); with the right one it passes.
func TestRequireTokenRefusesWithoutTheTokenAndServesWithIt(t *testing.T) {
	h := requireToken("s3cret-token", okHandler())
	for _, path := range []string{"/mcp", "/clown/exclude-tools", "/clown/system-prompt", "/clown/shutdown"} {
		if got := codeFor(t, h, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s with no credentials = %d, want 401", path, got)
		}
		if got := codeFor(t, h, path, "Bearer wrong"); got != http.StatusUnauthorized {
			t.Errorf("%s with a wrong token = %d, want 401", path, got)
		}
		if got := codeFor(t, h, path, "Basic s3cret-token"); got != http.StatusUnauthorized {
			t.Errorf("%s with the right value under another scheme = %d, want 401", path, got)
		}
		if got := codeFor(t, h, path, "Bearer s3cret-token"); got != http.StatusOK {
			t.Errorf("%s with the token = %d, want 200", path, got)
		}
	}
	if got := codeFor(t, h, "/mcp", "bearer s3cret-token"); got != http.StatusOK {
		t.Errorf("a lower-case scheme = %d, want 200 (schemes are case-insensitive)", got)
	}
}

func TestRequireTokenLeavesHealthzOpenAndSendsNoBody(t *testing.T) {
	h := requireToken("s3cret-token", okHandler())
	if got := codeFor(t, h, "/healthz", ""); got != http.StatusOK {
		t.Errorf("/healthz with no credentials = %d, want 200", got)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.Len() != 0 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("401 body %q, WWW-Authenticate %q; want no body and Bearer", rec.Body.String(), rec.Header().Get("WWW-Authenticate"))
	}
}

// With no token the handler is returned as it was: the bridge behaves as
// before.
func TestRequireTokenUnsetChangesNothing(t *testing.T) {
	h := requireToken("", okHandler())
	for _, path := range []string{"/mcp", "/clown/exclude-tools", "/healthz"} {
		if got := codeFor(t, h, path, ""); got != http.StatusOK {
			t.Errorf("%s with no token configured = %d, want 200", path, got)
		}
	}
}

func TestWithoutEnvDropsOnlyTheNamedVariable(t *testing.T) {
	in := []string{"A=1", tokenEnvVar + "=x", "B=2=3", tokenEnvVar + "X=keep", "C"}
	got := withoutEnv(in, tokenEnvVar)
	if want := []string{"A=1", "B=2=3", tokenEnvVar + "X=keep", "C"}; !slices.Equal(got, want) {
		t.Errorf("withoutEnv = %v, want %v", got, want)
	}
}
