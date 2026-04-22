package promptwalk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkPrompts_BuiltinAppendOnly(t *testing.T) {
	tmp := t.TempDir()
	builtinDir := filepath.Join(tmp, "builtin")
	os.MkdirAll(builtinDir, 0o755)
	os.WriteFile(filepath.Join(builtinDir, "00-identity.md"), []byte("identity"), 0o644)
	os.WriteFile(filepath.Join(builtinDir, "01-rules.md"), []byte("rules"), 0o644)

	startDir := filepath.Join(tmp, "project")
	os.MkdirAll(startDir, 0o755)

	result, err := WalkPrompts(startDir, tmp, builtinDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.SystemPromptFile != "" {
		t.Errorf("SystemPromptFile = %q, want empty", result.SystemPromptFile)
	}
	want := "identity\n\nrules\n\n"
	if result.AppendFragments != want {
		t.Errorf("AppendFragments = %q, want %q", result.AppendFragments, want)
	}
}

func TestWalkPrompts_CircusPromptDFragments(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "code", "myproject")
	os.MkdirAll(project, 0o755)

	homePromptD := filepath.Join(home, ".circus", "system-prompt.d")
	os.MkdirAll(homePromptD, 0o755)
	os.WriteFile(filepath.Join(homePromptD, "global.md"), []byte("global-fragment"), 0o644)

	codePromptD := filepath.Join(home, "code", ".circus", "system-prompt.d")
	os.MkdirAll(codePromptD, 0o755)
	os.WriteFile(filepath.Join(codePromptD, "code.md"), []byte("code-fragment"), 0o644)

	projPromptD := filepath.Join(project, ".circus", "system-prompt.d")
	os.MkdirAll(projPromptD, 0o755)
	os.WriteFile(filepath.Join(projPromptD, "project.md"), []byte("project-fragment"), 0o644)

	result, err := WalkPrompts(project, home, "")
	if err != nil {
		t.Fatal(err)
	}

	// Shallowest first: home → code → project
	want := "global-fragment\n\ncode-fragment\n\nproject-fragment\n\n"
	if result.AppendFragments != want {
		t.Errorf("AppendFragments = %q, want %q", result.AppendFragments, want)
	}
}

func TestWalkPrompts_SystemPromptFileDeepestWins(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "code", "myproject")
	os.MkdirAll(project, 0o755)

	os.MkdirAll(filepath.Join(home, ".circus"), 0o755)
	os.WriteFile(filepath.Join(home, ".circus", "system-prompt"), []byte("home-prompt"), 0o644)

	os.MkdirAll(filepath.Join(project, ".circus"), 0o755)
	os.WriteFile(filepath.Join(project, ".circus", "system-prompt"), []byte("project-prompt"), 0o644)

	result, err := WalkPrompts(project, home, "")
	if err != nil {
		t.Fatal(err)
	}

	wantPath := filepath.Join(project, ".circus", "system-prompt")
	if result.SystemPromptFile != wantPath {
		t.Errorf("SystemPromptFile = %q, want %q", result.SystemPromptFile, wantPath)
	}
}

func TestWalkPrompts_SystemPromptFileFromAncestor(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "code", "myproject")
	os.MkdirAll(project, 0o755)

	os.MkdirAll(filepath.Join(home, ".circus"), 0o755)
	os.WriteFile(filepath.Join(home, ".circus", "system-prompt"), []byte("home-prompt"), 0o644)

	result, err := WalkPrompts(project, home, "")
	if err != nil {
		t.Fatal(err)
	}

	wantPath := filepath.Join(home, ".circus", "system-prompt")
	if result.SystemPromptFile != wantPath {
		t.Errorf("SystemPromptFile = %q, want %q", result.SystemPromptFile, wantPath)
	}
}

func TestWalkPrompts_EmptyFilesSkipped(t *testing.T) {
	tmp := t.TempDir()
	builtinDir := filepath.Join(tmp, "builtin")
	os.MkdirAll(builtinDir, 0o755)
	os.WriteFile(filepath.Join(builtinDir, "00-content.md"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(builtinDir, "01-empty.md"), []byte(""), 0o644)

	result, err := WalkPrompts(tmp, tmp, builtinDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.AppendFragments, "\n\n") != 1 {
		t.Errorf("expected 1 fragment, got %q", result.AppendFragments)
	}
}

func TestWalkPrompts_NoCircusDirs(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	os.MkdirAll(project, 0o755)

	result, err := WalkPrompts(project, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.SystemPromptFile != "" {
		t.Errorf("SystemPromptFile = %q, want empty", result.SystemPromptFile)
	}
	if result.AppendFragments != "" {
		t.Errorf("AppendFragments = %q, want empty", result.AppendFragments)
	}
}

func TestWalkPrompts_BuiltinBeforeUserFragments(t *testing.T) {
	home := t.TempDir()
	builtinDir := filepath.Join(home, "builtin")
	os.MkdirAll(builtinDir, 0o755)
	os.WriteFile(filepath.Join(builtinDir, "00-builtin.md"), []byte("BUILTIN"), 0o644)

	homePromptD := filepath.Join(home, ".circus", "system-prompt.d")
	os.MkdirAll(homePromptD, 0o755)
	os.WriteFile(filepath.Join(homePromptD, "user.md"), []byte("USER"), 0o644)

	result, err := WalkPrompts(home, home, builtinDir)
	if err != nil {
		t.Fatal(err)
	}

	want := "BUILTIN\n\nUSER\n\n"
	if result.AppendFragments != want {
		t.Errorf("AppendFragments = %q, want %q", result.AppendFragments, want)
	}
}

func TestWalkPrompts_FragmentsSortedWithinDir(t *testing.T) {
	home := t.TempDir()
	promptD := filepath.Join(home, ".circus", "system-prompt.d")
	os.MkdirAll(promptD, 0o755)
	os.WriteFile(filepath.Join(promptD, "02-second.md"), []byte("second"), 0o644)
	os.WriteFile(filepath.Join(promptD, "01-first.md"), []byte("first"), 0o644)

	result, err := WalkPrompts(home, home, "")
	if err != nil {
		t.Fatal(err)
	}

	want := "first\n\nsecond\n\n"
	if result.AppendFragments != want {
		t.Errorf("AppendFragments = %q, want %q", result.AppendFragments, want)
	}
}
