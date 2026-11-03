package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"code.linenisgreat.com/clown/internal/alfa/agentscope"
)

// The model credential of a confined launch (clown#336). With a token file the
// frontend runs an InjectProxy and the unit holds a placeholder; without one,
// CLOWN_MODEL_CREDENTIAL (a profile env knob, frontend only) decides:
//
//	proxy-or-login (default) bind the operator's login read-only, as before,
//	                         with one stderr line saying so
//	proxy-only               refuse the launch before the unit starts
const (
	modelCredentialPolicyEnv = "CLOWN_MODEL_CREDENTIAL"
	policyProxyOrLogin       = "proxy-or-login"
	policyProxyOnly          = "proxy-only"
)

// modelTokenPath is where the frontend reads the bearer to inject:
// $XDG_RUNTIME_DIR/clown/credentials/anthropic-oauth, or CLOWN_MODEL_TOKEN_FILE
// (frontend only; never passed into the unit).
func modelTokenPath() string {
	if p := os.Getenv("CLOWN_MODEL_TOKEN_FILE"); p != "" {
		return p
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		return ""
	}
	return filepath.Join(runtime, "clown", "credentials", "anthropic-oauth")
}

// launchModelCredential decides how a confined launch gets its model
// credential: agentscope.ModelCredentialProxy or ModelCredentialLoginFile, or
// "" when the launch routes the model API itself (a gateway or juggler-remote
// profile sets its own ANTHROPIC_BASE_URL and token, which the proxy would
// misroute).
func launchModelCredential(warn io.Writer) (string, error) {
	if !targetsDefaultModelAPI(os.Getenv("ANTHROPIC_BASE_URL")) {
		return "", nil
	}
	return chooseModelCredential(modelTokenPath(), os.Getenv(modelCredentialPolicyEnv), warn)
}

// targetsDefaultModelAPI reports whether baseURL leaves the model API at
// Anthropic's default host.
func targetsDefaultModelAPI(baseURL string) bool {
	if baseURL == "" {
		return true
	}
	u, err := url.Parse(baseURL)
	return err == nil && u.Scheme == "https" && u.Hostname() == "api.anthropic.com" && (u.Path == "" || u.Path == "/")
}

// chooseModelCredential applies the policy to the token file at tokenPath.
func chooseModelCredential(tokenPath, policy string, warn io.Writer) (string, error) {
	switch policy {
	case "", policyProxyOrLogin, policyProxyOnly:
	default:
		return "", fmt.Errorf("%s=%q is not %s or %s", modelCredentialPolicyEnv, policy, policyProxyOrLogin, policyProxyOnly)
	}
	_, err := agentscope.TokenSource{Path: tokenPath}.Token()
	if err == nil {
		return agentscope.ModelCredentialProxy, nil
	}
	if policy == policyProxyOnly {
		return "", fmt.Errorf("%s=%s and %v; write the credential with `piggy secrets reconcile` (the token file is %s)",
			modelCredentialPolicyEnv, policyProxyOnly, err, tokenPath)
	}
	fmt.Fprintf(warn, "clown: agent scope: %v; binding the operator login read-only (fallback)\n", err)
	return agentscope.ModelCredentialLoginFile, nil
}

// newModelInjectProxy is the InjectProxy for unit: it reads the bearer from the
// token file on every request. CLOWN_MODEL_UPSTREAM (http on the loopback
// only) points it elsewhere and CLOWN_MODEL_PROXY_PERMISSIVE=1 forwards
// other /v1/ and /api/ paths, logged; both are frontend-only, for the bats
// case and for the probe of a new claude version, and neither reaches the unit.
func newModelInjectProxy(unit string, logger *slog.Logger) (*agentscope.InjectProxy, error) {
	p := &agentscope.InjectProxy{
		Unit:       unit,
		Token:      agentscope.TokenSource{Path: modelTokenPath()}.Token,
		Logger:     logger,
		Permissive: os.Getenv("CLOWN_MODEL_PROXY_PERMISSIVE") == "1",
	}
	if raw := os.Getenv("CLOWN_MODEL_UPSTREAM"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("CLOWN_MODEL_UPSTREAM: %w", err)
		}
		// Only a loopback http fake: an https override would let a planted
		// environment (a worktree .envrc) send the operator's bearer anywhere.
		if u.Scheme != "http" || !agentscope.IsLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("CLOWN_MODEL_UPSTREAM %q must be http on the loopback: the bearer is sent there", u.Redacted())
		}
		p.Upstream = u
	}
	return p, nil
}
