package juggler

import (
	"errors"
	"fmt"
	"os"
)

// Remote entry styles. StyleDecisions is consumed ONLY by `juggler decide`
// (FDR 0019 §1, §8): it is not a loop codec, and neither the agent loop nor
// `juggler prompt` accepts it (prompt's dispatch rejects any style other
// than anthropic / openai-compat, so a decisions entry errors clearly
// there).
const (
	StyleAnthropic    = "anthropic"
	StyleOpenAICompat = "openai-compat"
	StyleDecisions    = "decisions"
)

// RemoteStyles lists every style a remote registry entry may carry.
func RemoteStyles() []string {
	return []string{StyleAnthropic, StyleOpenAICompat, StyleDecisions}
}

// IsRemoteStyle reports whether s is a valid remote entry style.
func IsRemoteStyle(s string) bool {
	for _, v := range RemoteStyles() {
		if v == s {
			return true
		}
	}
	return false
}

// ErrDaemonRequired is returned by ResolveRemoteModelFromFile when the name
// is not a remote registry entry: local (GGUF) models can only be resolved
// by the juggler daemon, which owns the llama-server process.
var ErrDaemonRequired = errors.New("juggler daemon required to resolve local models")

// ResolveRemoteModelFromFile resolves a remote registry entry by name
// straight from the models file (RemoteModelsPath, honouring
// JUGGLER_MODELS_PATH) without dialing the daemon (FDR 0019 §10). URL and
// Token are env-expanded exactly as the daemon's ResolveModel does. A name
// with no remote entry yields an error wrapping ErrDaemonRequired.
func ResolveRemoteModelFromFile(name string) (ResolveModelResult, error) {
	path, err := RemoteModelsPath()
	if err != nil {
		return ResolveModelResult{}, err
	}
	return resolveRemoteModelFromPath(path, name)
}

func resolveRemoteModelFromPath(path, name string) (ResolveModelResult, error) {
	models, err := LoadRemoteModels(path)
	if err != nil {
		return ResolveModelResult{}, err
	}
	for _, m := range models {
		if m.Name == name {
			return ResolveModelResult{
				Kind:  ModelKindRemote,
				Style: m.Style,
				URL:   os.ExpandEnv(m.URL),
				Token: os.ExpandEnv(m.Token),
			}, nil
		}
	}
	return ResolveModelResult{}, fmt.Errorf("model %q is not a remote registry entry: %w", name, ErrDaemonRequired)
}
