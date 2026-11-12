package pluginhost

import (
	"net/http"
)

// The per-session bridge token (clown#288, second slice). When Host.BridgeToken
// is set, every clown-stdio-bridge the host starts gets it in its environment
// and refuses a request that does not carry it (cmd/clown-stdio-bridge/auth.go),
// the manifest entry the host writes for that bridge names it as an
// Authorization header, and the host's own calls to the bridge send it. Unset,
// none of this happens and nothing changes. The token is never logged.

// EnvBridgeToken is the variable the bridge reads its token from; it must
// match cmd/clown-stdio-bridge/auth.go's tokenEnvVar.
const EnvBridgeToken = "CLOWN_BRIDGE_TOKEN"

// withBridgeToken returns env with the bridge token added, copying env so a
// map shared by a parsed clown.json is never mutated.
func withBridgeToken(env map[string]string, token string) map[string]string {
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	out[EnvBridgeToken] = token
	return out
}

// token is the bearer token this server's bridge was given, or "" when it was
// not given one (the flag is off, or the server is not a bridge). A server
// reserved for the agent unit (--agent-scope) has it too: the in-unit relay
// starts the bridge with this same env, so the manifest entry must carry the
// header even though the frontend never dials it.
func (s *ManagedServer) token() string {
	return s.Def.Env[EnvBridgeToken]
}

// Client is the HTTP client the host's own calls to this server use: the
// handshake's client, sending the bridge token as a bearer credential when the
// server has one. Use it instead of Handshake().Client() for every call to a
// started server except the health probe, which the bridge leaves open.
func (s *ManagedServer) Client() *http.Client {
	base := s.handshake.Client()
	tok := s.token()
	if tok == "" {
		return base
	}
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &http.Client{Transport: bearerTransport{base: rt, token: tok}, Timeout: base.Timeout}
}

// bearerTransport adds "Authorization: Bearer <token>" to every request that
// does not already carry an Authorization header.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context()) // a RoundTripper must not modify the request it is given
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// manifestHeaders is the headers a claude manifest entry for this server
// carries: the bearer token, or nil when it has none.
func (s *ManagedServer) manifestHeaders() map[string]string {
	if tok := s.token(); tok != "" {
		return map[string]string{"Authorization": "Bearer " + tok}
	}
	return nil
}
