package main

import "testing"

func TestLaunchPlanJSON_IsDeterministic(t *testing.T) {
	p := launchPlan{
		Binary: "/nix/store/xxx/bin/claude",
		Args:   []string{"--plugin-dir", "/a", "--resume"},
		Env:    []string{"B=2", "A=1"},
		Files:  []string{"/stage/opencode.json", "/stage/prompt.txt"},
	}
	got, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	// Env and Files MUST be sorted: map/dir iteration order would otherwise
	// make the golden fixtures flap.
	want := `{"binary":"/nix/store/xxx/bin/claude","args":["--plugin-dir","/a","--resume"],"env":["A=1","B=2"],"files":["/stage/opencode.json","/stage/prompt.txt"]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestLaunchPlanJSON_DoesNotMutateReceiver pins that sorting happens on a copy.
// JSON() has a value receiver, but slices share their backing array, so a naive
// sort.Strings(p.Env) would reorder the CALLER's slice as a side effect — and
// the caller is runProvider, holding the very env it hands the child process.
func TestLaunchPlanJSON_DoesNotMutateReceiver(t *testing.T) {
	env := []string{"B=2", "A=1"}
	p := launchPlan{Binary: "/bin/true", Env: env}
	if _, err := p.JSON(); err != nil {
		t.Fatal(err)
	}
	if env[0] != "B=2" || env[1] != "A=1" {
		t.Errorf("JSON() reordered the caller's slice: %v", env)
	}
}

// TestLaunchPlanJSON_RedactsSecrets is why redaction lives INSIDE JSON() rather
// than at the call site: these plans get committed as golden fixtures, so an
// unredacted API token would land in git history permanently. There must be no
// way to emit a plan that skipped redaction.
func TestLaunchPlanJSON_RedactsSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"api key", "ANTHROPIC_API_KEY=sk-ant-real", "ANTHROPIC_API_KEY=<redacted>"},
		{"lowercase key", "openai_api_key=sk-real", "openai_api_key=<redacted>"},
		{"token", "GITHUB_TOKEN=ghp_real", "GITHUB_TOKEN=<redacted>"},
		{"mixed-case token", "Auth_Token=abc", "Auth_Token=<redacted>"},
		{"secret", "CLIENT_SECRET=hunter2", "CLIENT_SECRET=<redacted>"},
		{"password", "DB_PASSWORD=hunter2", "DB_PASSWORD=<redacted>"},
		// The match is on the KEY only. A value that merely mentions a secret
		// word is not itself a secret, and redacting it would destroy exactly
		// the argv/config paths the fixtures exist to pin.
		{"value mentions token", "OPENCODE_CONFIG=/stage/token.json", "OPENCODE_CONFIG=/stage/token.json"},
		{"unrelated", "PATH=/usr/bin", "PATH=/usr/bin"},
		// Only the FIRST "=" separates, so a value containing "=" survives.
		{"equals in value", "FOO=a=b", "FOO=a=b"},
		{"equals in secret value", "MY_TOKEN=a=b", "MY_TOKEN=<redacted>"},
		// A malformed entry has no value to leak; pass it through rather than
		// inventing one.
		{"no separator", "BAREWORD", "BAREWORD"},
		// Redacted unconditionally: whether a secret var is set-but-empty is
		// itself information, and the uniform shape is one less thing to reason
		// about when reviewing a fixture diff.
		{"empty value", "SOME_KEY=", "SOME_KEY=<redacted>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactEnvEntry(tc.in); got != tc.want {
				t.Errorf("redactEnvEntry(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	p := launchPlan{Binary: "/bin/true", Env: []string{"ANTHROPIC_API_KEY=sk-ant-real"}}
	got, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"binary":"/bin/true","args":[],"env":["ANTHROPIC_API_KEY=<redacted>"],"files":[]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestLaunchPlanJSON_NilSlicesEmitEmptyArrays keeps the fixture shape stable
// whether or not a provider contributes env/files: a `null` vs `[]` diff says
// nothing about the launch path.
func TestLaunchPlanJSON_NilSlicesEmitEmptyArrays(t *testing.T) {
	p := launchPlan{Binary: "/bin/true"}
	got, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"binary":"/bin/true","args":[],"env":[],"files":[]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
