package promptwalk

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type PromptResult struct {
	SystemPromptFile string
	AppendFragments  string
}

// WalkPrompts walks from startDir up to homeDir (inclusive), collecting
// .circus/ prompt fragments for system-prompt append mode and finding
// the nearest system-prompt file for replace mode.
//
// builtinAppendDirs are read first, in order, for *.md files that are
// always included before user fragments. Empty entries and missing
// directories are silently skipped. Per FDR 0003, callers pass clown's
// build-time system-prompt-append.d/ followed by any
// plugin-contributed `.clown-plugin/system-prompt-append.d/`
// directories in plugin-list order.
//
// Fragment collection order: builtin dirs (in argument order), then
// .circus/system-prompt.d/*.md from shallowest ancestor to deepest
// (sorted within each directory). Each non-empty fragment is followed
// by two newlines.
//
// The system-prompt file search walks deepest-first and returns the
// first .circus/system-prompt found.
func WalkPrompts(startDir, homeDir string, builtinAppendDirs []string) (PromptResult, error) {
	ancestors, err := walkAncestors(startDir, homeDir)
	if err != nil {
		return PromptResult{}, err
	}

	var b strings.Builder

	for _, dir := range builtinAppendDirs {
		if dir == "" {
			continue
		}
		if err := collectFragments(&b, dir); err != nil && !os.IsNotExist(err) {
			return PromptResult{}, err
		}
	}

	reversed := make([]string, len(ancestors))
	for i, d := range ancestors {
		reversed[len(ancestors)-1-i] = d
	}

	for _, dir := range reversed {
		promptD := filepath.Join(dir, ".circus", "system-prompt.d")
		if err := collectFragments(&b, promptD); err != nil && !os.IsNotExist(err) {
			return PromptResult{}, err
		}
	}

	var systemPromptFile string
	for _, dir := range ancestors {
		candidate := filepath.Join(dir, ".circus", "system-prompt")
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			systemPromptFile = candidate
			break
		}
	}

	return PromptResult{
		SystemPromptFile: systemPromptFile,
		AppendFragments:  b.String(),
	}, nil
}

// walkAncestors returns directories from startDir up to homeDir (or /),
// in deepest-first order. Both startDir and the stop directory are
// included.
func walkAncestors(startDir, homeDir string) ([]string, error) {
	start, err := filepath.Abs(startDir)
	if err != nil {
		return nil, err
	}
	home, err := filepath.Abs(homeDir)
	if err != nil {
		return nil, err
	}

	var dirs []string
	d := start
	for {
		dirs = append(dirs, d)
		if d == home || d == "/" {
			break
		}
		d = filepath.Dir(d)
	}
	return dirs, nil
}

// collectFragments reads *.md files from dir (non-recursive, sorted by
// name) and appends each non-empty file's content followed by two
// newlines to b.
func collectFragments(b *strings.Builder, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		content := string(data)
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n\n")
		}
	}
	return nil
}
