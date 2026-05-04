package pluginhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CompileInputs groups the clown-derived data injected into the
// compiled .claude-plugin/plugin.json. Each field is independently
// optional; nil/empty leaves the corresponding plugin.json key
// untouched (or, in the case of mcpServers, stripped — see Servers).
type CompileInputs struct {
	// Servers, when non-nil, replaces the mcpServers block with
	// url-based entries pointing at the running HTTP servers. When
	// nil, the mcpServers block is stripped entirely.
	Servers map[string]MCPServerEntry
	// Monitors, when non-empty, is injected as the top-level monitors
	// array in plugin.json. CompilePluginManifest refuses if the
	// source plugin.json already declares a monitors key, since
	// merging two declarations of the same passthrough field would
	// silently lose information.
	Monitors []MonitorDef
}

// CompilePluginManifest compiles a replacement plugin.json for claude.
// in.Servers controls the mcpServers block (see CompileInputs.Servers).
// in.Monitors, when non-empty, is injected as the top-level monitors
// array; it is an error for the source plugin.json to also declare
// monitors. Unknown top-level keys are preserved. The second return
// value reports whether mcpServers was actually present in the
// original manifest.
func CompilePluginManifest(raw []byte, in CompileInputs) ([]byte, bool, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("parsing plugin.json: %w", err)
	}
	_, had := doc["mcpServers"]
	if in.Servers != nil {
		encoded, err := json.Marshal(in.Servers)
		if err != nil {
			return nil, false, fmt.Errorf("marshalling server entries: %w", err)
		}
		doc["mcpServers"] = json.RawMessage(encoded)
	} else {
		delete(doc, "mcpServers")
	}
	if len(in.Monitors) > 0 {
		if _, present := doc["monitors"]; present {
			return nil, false, fmt.Errorf("monitors declared in both clown.json and .claude-plugin/plugin.json: remove one source")
		}
		encoded, err := json.Marshal(in.Monitors)
		if err != nil {
			return nil, false, fmt.Errorf("marshalling monitors: %w", err)
		}
		doc["monitors"] = json.RawMessage(encoded)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshalling compiled plugin.json: %w", err)
	}
	return out, had, nil
}

// CompilePluginDir compiles a plugin directory for consumption by claude's
// native --plugin-dir loader. It writes a fresh staging directory whose
// contents are symlinks back to sourceDir, except for
// .claude-plugin/plugin.json which is replaced with a compiled copy.
//
// in.Servers and in.Monitors carry the same semantics as in
// CompilePluginManifest.
//
// The caller owns cleanup: pass the returned path to os.RemoveAll when the
// staged directory is no longer needed. sourceDir must contain a
// .claude-plugin/plugin.json file.
func CompilePluginDir(sourceDir string, in CompileInputs) (string, error) {
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return "", fmt.Errorf("resolving source plugin dir: %w", err)
	}

	stageDir, err := os.MkdirTemp("", "clown-plugin-compile-*")
	if err != nil {
		return "", fmt.Errorf("creating staging dir: %w", err)
	}

	if err := stagePluginDir(absSource, stageDir, in); err != nil {
		os.RemoveAll(stageDir)
		return "", err
	}
	return stageDir, nil
}

func stagePluginDir(sourceAbs, stageDir string, in CompileInputs) error {
	entries, err := os.ReadDir(sourceAbs)
	if err != nil {
		return fmt.Errorf("reading source plugin dir: %w", err)
	}

	var sawClaudePlugin bool
	for _, e := range entries {
		src := filepath.Join(sourceAbs, e.Name())
		dst := filepath.Join(stageDir, e.Name())
		if e.Name() == ".claude-plugin" {
			sawClaudePlugin = true
			if err := stageClaudePluginDir(src, dst, in); err != nil {
				return err
			}
			continue
		}
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlinking %s: %w", e.Name(), err)
		}
	}

	if !sawClaudePlugin {
		return fmt.Errorf("source plugin dir %s: missing .claude-plugin/", sourceAbs)
	}
	return nil
}

func stageClaudePluginDir(sourceAbs, stageDir string, in CompileInputs) error {
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("creating staged .claude-plugin dir: %w", err)
	}

	entries, err := os.ReadDir(sourceAbs)
	if err != nil {
		return fmt.Errorf("reading .claude-plugin: %w", err)
	}

	var sawPluginJSON bool
	for _, e := range entries {
		src := filepath.Join(sourceAbs, e.Name())
		dst := filepath.Join(stageDir, e.Name())
		if e.Name() == "plugin.json" {
			sawPluginJSON = true
			if err := writeCompiledManifest(src, dst, in); err != nil {
				return err
			}
			continue
		}
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlinking .claude-plugin/%s: %w", e.Name(), err)
		}
	}

	if !sawPluginJSON {
		return fmt.Errorf(".claude-plugin/plugin.json missing in %s", sourceAbs)
	}
	return nil
}

func writeCompiledManifest(sourcePath, stagedPath string, in CompileInputs) error {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("reading plugin.json: %w", err)
	}
	out, _, err := CompilePluginManifest(raw, in)
	if err != nil {
		return err
	}
	if err := os.WriteFile(stagedPath, out, 0o644); err != nil {
		return fmt.Errorf("writing compiled plugin.json: %w", err)
	}
	return nil
}
