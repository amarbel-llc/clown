package pluginhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CompilePluginManifest compiles a replacement plugin.json for claude by
// removing the mcpServers block. Unknown top-level keys are preserved. The
// second return value reports whether mcpServers was actually present and
// stripped; callers may log the no-op case but still use the returned bytes
// (re-marshalled with 2-space indent).
func CompilePluginManifest(raw []byte) ([]byte, bool, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("parsing plugin.json: %w", err)
	}
	_, had := doc["mcpServers"]
	delete(doc, "mcpServers")
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshalling compiled plugin.json: %w", err)
	}
	return out, had, nil
}

// CompilePluginDir compiles a plugin directory for consumption by claude's
// native --plugin-dir loader. It writes a fresh staging directory whose
// contents are symlinks back to sourceDir, except for
// .claude-plugin/plugin.json which is replaced with a compiled copy (the
// mcpServers block is removed — see CompilePluginManifest).
//
// The caller owns cleanup: pass the returned path to os.RemoveAll when the
// staged directory is no longer needed. sourceDir must contain a
// .claude-plugin/plugin.json file.
func CompilePluginDir(sourceDir string) (string, error) {
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return "", fmt.Errorf("resolving source plugin dir: %w", err)
	}

	stageDir, err := os.MkdirTemp("", "clown-plugin-compile-*")
	if err != nil {
		return "", fmt.Errorf("creating staging dir: %w", err)
	}

	if err := stagePluginDir(absSource, stageDir); err != nil {
		os.RemoveAll(stageDir)
		return "", err
	}
	return stageDir, nil
}

func stagePluginDir(sourceAbs, stageDir string) error {
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
			if err := stageClaudePluginDir(src, dst); err != nil {
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

func stageClaudePluginDir(sourceAbs, stageDir string) error {
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
			if err := writeCompiledManifest(src, dst); err != nil {
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

func writeCompiledManifest(sourcePath, stagedPath string) error {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("reading plugin.json: %w", err)
	}
	out, _, err := CompilePluginManifest(raw)
	if err != nil {
		return err
	}
	if err := os.WriteFile(stagedPath, out, 0o644); err != nil {
		return fmt.Errorf("writing compiled plugin.json: %w", err)
	}
	return nil
}
