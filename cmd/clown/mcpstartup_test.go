package main

import (
	"reflect"
	"testing"

	"code.linenisgreat.com/clown/internal/clownfile"
)

// These tests pin only the env clown hands claude (clown#296). Whether claude
// then actually holds turn 1 until its MCP servers connect is claude's
// behaviour and is not exercised here.

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestClaudeMCPStartupEnvInteractiveDefaults(t *testing.T) {
	got := claudeMCPStartupEnv("claude", false, envFrom(nil))
	want := map[string]string{"MCP_CONNECTION_NONBLOCKING": "false"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interactive claude: got %v, want %v", got, want)
	}
}

func TestClaudeMCPStartupEnvHeadlessDefaults(t *testing.T) {
	got := claudeMCPStartupEnv("claude", true, envFrom(nil))
	want := map[string]string{
		"MCP_CONNECTION_NONBLOCKING":      "false",
		"CLAUDE_CODE_MCP_STARTUP_WAIT_MS": "10000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("headless claude: got %v, want %v", got, want)
	}
}

func TestClaudeMCPStartupEnvRespectsUserValues(t *testing.T) {
	inherited := map[string]string{
		"MCP_CONNECTION_NONBLOCKING":      "true",
		"CLAUDE_CODE_MCP_STARTUP_WAIT_MS": "2500",
	}
	if got := claudeMCPStartupEnv("claude", true, envFrom(inherited)); len(got) != 0 {
		t.Fatalf("user-set values must be left alone, got %v", got)
	}
	// Only one set: the other default still applies.
	partial := map[string]string{"MCP_CONNECTION_NONBLOCKING": "true"}
	got := claudeMCPStartupEnv("claude", true, envFrom(partial))
	want := map[string]string{"CLAUDE_CODE_MCP_STARTUP_WAIT_MS": "10000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partial override: got %v, want %v", got, want)
	}
}

func TestClaudeMCPStartupEnvOtherProvidersUntouched(t *testing.T) {
	for _, p := range []string{"codex", "opencode", "openrouter", "crush", "juggler", "clownbox"} {
		for _, headless := range []bool{false, true} {
			if got := claudeMCPStartupEnv(p, headless, envFrom(nil)); len(got) != 0 {
				t.Errorf("provider %s headless=%v: want no env, got %v", p, headless, got)
			}
		}
	}
}

func TestClaudeLaunchIsHeadless(t *testing.T) {
	cases := []struct {
		name        string
		mode        clownfile.AttachMode
		forwarded   []string
		interactive bool
		want        bool
	}{
		{"tty start", clownfile.ModeStart, nil, true, false},
		{"tty resume", clownfile.ModeResume, []string{"--resume", "x"}, true, false},
		{"no tty", clownfile.ModeStart, nil, false, true},
		{"print short", clownfile.ModeStart, []string{"-p", "hi"}, true, true},
		{"print long", clownfile.ModeStart, []string{"--print", "hi"}, true, true},
		{"spawn", clownfile.ModeSpawn, nil, true, true},
	}
	for _, c := range cases {
		if got := claudeLaunchIsHeadless(c.mode, c.forwarded, c.interactive); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
