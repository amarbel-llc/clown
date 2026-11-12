package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// tokenEnvVar names the optional per-bridge bearer token (clown#288). When it
// is set in the bridge's environment, every route except /healthz refuses a
// request that does not carry it; unset, the bridge serves as before. With
// CLOWN_BRIDGE_AUTH=1 the host mints a per-session token, sets this variable
// on every bridge it starts and sends the token itself; a token set by hand
// is unknown to the host and locks its control calls out.
const tokenEnvVar = "CLOWN_BRIDGE_TOKEN"

// withoutEnv returns environ without any entry for the named variable. The
// wrapped child must not inherit the token: it would hand the credential to
// the very server it guards.
func withoutEnv(environ []string, name string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k == name {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// requireToken wraps next so that, when token is non-empty, a request must
// send "Authorization: Bearer <token>" or get a bodiless 401. /healthz stays
// open: it reveals only whether the wrapped child is alive, and the host's
// health probe sends no credentials. An empty token returns next unchanged.
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		scheme, presented, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		got := sha256.Sum256([]byte(presented))
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
