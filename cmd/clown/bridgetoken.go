package main

import (
	"crypto/rand"
	"encoding/hex"
	"io"
)

// The per-session bridge token opt-in (clown#288, second slice). With
// CLOWN_BRIDGE_AUTH=1 the launch mints one random token, hands it to every
// clown-stdio-bridge it starts (CLOWN_BRIDGE_TOKEN), names it as an
// Authorization header in the manifest it writes for claude, and sends it on
// the host's own control calls. Default off: nothing changes. The token is
// never logged or put in the launch plan.

// bridgeAuthEnv is the opt-in switch.
const bridgeAuthEnv = "CLOWN_BRIDGE_AUTH"

// bridgeTokenUnsupported says why this launch cannot use the token, or "" when
// it can. The token rides in claude's manifest `headers`, so only the claude
// provider is covered, and the aggregator cannot send it to the bridges it
// fronts. A confined launch (--agent-scope) is covered: the relays are byte
// splices that pass the header through, the in-unit bridges are started with
// the token in their env, and the unit's own calls to them send it.
func bridgeTokenUnsupported(flags parsedFlags) string {
	switch {
	case flags.provider != "claude":
		return "provider " + flags.provider + " does not take manifest headers"
	case flags.mcpCollapse:
		return "--mcp-collapse's aggregator cannot send the token to the bridges it fronts"
	}
	return ""
}

// bridgeTokenFor returns the token for this launch, or "" when the opt-in is
// off or the launch cannot use it (warn is told why, never the token). random
// is the entropy source, crypto/rand in production.
func bridgeTokenFor(flags parsedFlags, getenv func(string) string, random io.Reader, warn func(msg string)) string {
	if getenv(bridgeAuthEnv) != "1" {
		return ""
	}
	if why := bridgeTokenUnsupported(flags); why != "" {
		warn("bridge token auth requested (" + bridgeAuthEnv + "=1) but not enabled: " + why)
		return ""
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(random, b); err != nil {
		warn("bridge token auth requested but not enabled: no randomness: " + err.Error())
		return ""
	}
	return hex.EncodeToString(b)
}

// randomSource is crypto/rand, named so tests can swap their own.
var randomSource io.Reader = rand.Reader
