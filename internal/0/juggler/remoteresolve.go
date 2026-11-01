package juggler

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
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
func IsRemoteStyle(s string) bool { return slices.Contains(RemoteStyles(), s) }

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
	return ResolveRemoteModelFromPath(path, name)
}

// ResolveRemoteModelFromPath is ResolveRemoteModelFromFile against an
// explicit models file (the daemon's configured path).
func ResolveRemoteModelFromPath(path, name string) (ResolveModelResult, error) {
	models, err := LoadRemoteModels(path)
	if err != nil {
		return ResolveModelResult{}, fmt.Errorf("list remote models: %w", err)
	}
	for _, m := range models {
		if m.Name == name {
			return m.Resolve()
		}
	}
	return ResolveModelResult{}, fmt.Errorf("model %q is not a remote registry entry: %w", name, ErrDaemonRequired)
}

// Resolve turns a registry entry into a ResolveModelResult. The token comes
// from token_file (read by path, trailing whitespace trimmed) when set, else
// from token (literal or ${VAR}, os.ExpandEnv); setting both is a config
// error. Errors name the entry and the path, never the token.
func (m RemoteModel) Resolve() (ResolveModelResult, error) {
	token := os.ExpandEnv(m.Token)
	if m.TokenFile != "" {
		if m.Token != "" {
			return ResolveModelResult{}, fmt.Errorf("remote model %q: set only one of token and token_file", m.Name)
		}
		path := expandTokenFilePath(m.TokenFile)
		b, err := os.ReadFile(path)
		if err != nil {
			return ResolveModelResult{}, fmt.Errorf("remote model %q: read token_file %s: %w", m.Name, path, err)
		}
		token = strings.TrimSpace(string(b))
	}
	return ResolveModelResult{
		Kind:    ModelKindRemote,
		Style:   m.Style,
		URL:     os.ExpandEnv(m.URL),
		Token:   token,
		ModelID: m.ModelID,
	}, nil
}

func expandTokenFilePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	return os.ExpandEnv(p)
}
