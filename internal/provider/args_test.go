package provider

import (
	"os"
	"strings"
	"testing"
)

func TestBuildClaudeArgs_DisallowedToolsFromFile(t *testing.T) {
	f, err := os.CreateTemp("", "disallowed-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString("Bash(*)\nAgent(Explore)\nWebFetch\n")
	f.Close()

	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{
		DisallowedToolsFile: f.Name(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	found := map[string]bool{}
	for i, a := range args {
		if a == "--disallowed-tools" && i+1 < len(args) {
			found[args[i+1]] = true
		}
	}
	for _, want := range []string{"Bash(*)", "Agent(Explore)", "WebFetch"} {
		if !found[want] {
			t.Errorf("missing --disallowed-tools %s", want)
		}
	}
}

func TestBuildClaudeArgs_DisallowedToolsFileEmpty(t *testing.T) {
	f, err := os.CreateTemp("", "disallowed-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Close()

	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{
		DisallowedToolsFile: f.Name(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for _, a := range args {
		if a == "--disallowed-tools" {
			t.Error("no --disallowed-tools should be emitted for empty file")
		}
	}
}

func TestBuildClaudeArgs_DisallowedToolsFileCommentsAndBlanks(t *testing.T) {
	f, err := os.CreateTemp("", "disallowed-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString("# comment\nBash(*)\n\n  \n# another comment\nWrite\n")
	f.Close()

	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{
		DisallowedToolsFile: f.Name(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	var got []string
	for i, a := range args {
		if a == "--disallowed-tools" && i+1 < len(args) {
			got = append(got, args[i+1])
		}
	}
	want := []string{"Bash(*)", "Write"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildClaudeArgs_NoDisallowedToolsFile(t *testing.T) {
	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for _, a := range args {
		if a == "--disallowed-tools" {
			t.Error("no --disallowed-tools should be emitted when file is unset")
		}
	}
}

func TestBuildClaudeArgs_AgentsFile(t *testing.T) {
	f, err := os.CreateTemp("", "agents-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(`{"test-agent": {}}`)
	f.Close()

	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{
		AgentsFile: f.Name(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for i, a := range args {
		if a == "--agents" && i+1 < len(args) {
			if args[i+1] != `{"test-agent": {}}` {
				t.Errorf("agents content = %q", args[i+1])
			}
			return
		}
	}
	t.Error("--agents not found in args")
}

func TestBuildClaudeArgs_SystemPromptFile(t *testing.T) {
	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{
		SystemPromptFile: "/tmp/test-prompt",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for i, a := range args {
		if a == "--system-prompt-file" && i+1 < len(args) {
			if args[i+1] != "/tmp/test-prompt" {
				t.Errorf("system-prompt-file = %q", args[i+1])
			}
			return
		}
	}
	t.Error("--system-prompt-file not found in args")
}

func TestBuildClaudeArgs_AppendFragments(t *testing.T) {
	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{
		AppendFragments: "test fragment content",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for i, a := range args {
		if a == "--append-system-prompt-file" && i+1 < len(args) {
			data, err := os.ReadFile(args[i+1])
			if err != nil {
				t.Fatalf("reading temp file: %v", err)
			}
			if string(data) != "test fragment content" {
				t.Errorf("temp file content = %q", string(data))
			}
			return
		}
	}
	t.Error("--append-system-prompt-file not found in args")
}

func TestBuildClaudeArgs_ForwardedArgs(t *testing.T) {
	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{}, []string{"chat", "--resume"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	last2 := args[len(args)-2:]
	if last2[0] != "chat" || last2[1] != "--resume" {
		t.Errorf("forwarded args at end = %v", last2)
	}
}

// clown#163: a non-empty SettingsJSON is emitted as --settings <json> so clown
// can inject the AskUserQuestion AFK-timeout override through a path claude
// actually reads (clown ships no managed-settings, clown#133).
func TestBuildClaudeArgs_Settings(t *testing.T) {
	settings := `{"env":{"CLAUDE_AFK_TIMEOUT_MS":"2147483647"}}`
	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{SettingsJSON: settings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for i, a := range args {
		if a == "--settings" && i+1 < len(args) {
			if args[i+1] != settings {
				t.Errorf("--settings value = %q, want %q", args[i+1], settings)
			}
			return
		}
	}
	t.Error("--settings not found in args")
}

func TestBuildClaudeArgs_NoSettings(t *testing.T) {
	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for _, a := range args {
		if a == "--settings" {
			t.Error("no --settings should be emitted when SettingsJSON is empty")
		}
	}
}

// --settings must precede the forwarded user args so a user's own --settings
// (passed after the `--`) wins on claude's last-flag precedence, preserving the
// user's ability to override clown's safety default.
func TestBuildClaudeArgs_SettingsBeforeForwarded(t *testing.T) {
	args, _, cleanup, err := BuildClaudeArgs(ClaudeArgs{SettingsJSON: `{"env":{}}`}, []string{"--resume"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	settingsIdx, fwdIdx := -1, -1
	for i, a := range args {
		switch a {
		case "--settings":
			settingsIdx = i
		case "--resume":
			fwdIdx = i
		}
	}
	if settingsIdx == -1 || fwdIdx == -1 || settingsIdx > fwdIdx {
		t.Errorf("--settings (idx %d) must precede forwarded --resume (idx %d)", settingsIdx, fwdIdx)
	}
}

func TestBuildCodexArgs_SandboxWrite(t *testing.T) {
	args, cleanup, err := BuildCodexArgs(CodexArgs{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if len(args) < 2 || args[0] != "--sandbox" || args[1] != "workspace-write" {
		t.Errorf("args = %v, want [--sandbox workspace-write ...]", args)
	}
}

func TestBuildCodexArgs_CombinedPrompt(t *testing.T) {
	promptFile, err := os.CreateTemp("", "prompt-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(promptFile.Name())
	promptFile.WriteString("base prompt")
	promptFile.Close()

	args, cleanup, err := BuildCodexArgs(CodexArgs{
		SystemPromptFile: promptFile.Name(),
		AppendFragments:  "extra fragment",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			val := args[i+1]
			if !strings.HasPrefix(val, "experimental_instructions_file=") {
				t.Errorf("config value = %q", val)
				return
			}
			tmpPath := strings.TrimPrefix(val, "experimental_instructions_file=")
			data, err := os.ReadFile(tmpPath)
			if err != nil {
				t.Fatalf("reading temp file: %v", err)
			}
			content := string(data)
			if !strings.Contains(content, "base prompt") {
				t.Error("missing base prompt in combined file")
			}
			if !strings.Contains(content, "extra fragment") {
				t.Error("missing extra fragment in combined file")
			}
			return
		}
	}
	t.Error("--config not found in args")
}

func TestBuildCodexArgs_AppendOnly(t *testing.T) {
	args, cleanup, err := BuildCodexArgs(CodexArgs{
		AppendFragments: "only fragment",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			val := args[i+1]
			tmpPath := strings.TrimPrefix(val, "experimental_instructions_file=")
			data, err := os.ReadFile(tmpPath)
			if err != nil {
				t.Fatalf("reading temp file: %v", err)
			}
			if string(data) != "only fragment" {
				t.Errorf("temp file content = %q, want 'only fragment'", string(data))
			}
			return
		}
	}
	t.Error("--config not found in args")
}

func TestBuildCodexArgs_NoPrompts(t *testing.T) {
	args, cleanup, err := BuildCodexArgs(CodexArgs{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for _, a := range args {
		if a == "--config" {
			t.Error("--config should not be present when no prompts")
		}
	}
}

func TestBuildCodexArgs_ForwardedArgs(t *testing.T) {
	args, cleanup, err := BuildCodexArgs(CodexArgs{}, []string{"--model", "o3"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	last2 := args[len(args)-2:]
	if last2[0] != "--model" || last2[1] != "o3" {
		t.Errorf("forwarded args at end = %v", last2)
	}
}
