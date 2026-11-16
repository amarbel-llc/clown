package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/profile"
	"code.linenisgreat.com/clown/internal/0/staging"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// Secrets a user profile is written with; no profile resource may carry any.
const (
	profSecretUser  = "alice"
	profSecretPass  = "hunter2"
	profSecretQuery = "sekrit-q"
	profSecretFrag  = "frag-x"
	profSecretToken = "tok-SECRET-99"
	profSecretEnv   = "env-SECRET-value"
)

// writeSavedRegistry saves ps through the real writer (stamped) and returns
// its path.
func writeSavedRegistry(t *testing.T, ps ...profile.Profile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := profile.Save(path, ps, "0.4.4+test"); err != nil {
		t.Fatal(err)
	}
	return path
}

func leakyProfileRegistry(t *testing.T) string {
	return writeSavedRegistry(t,
		profile.Profile{
			Name: "leaky", Display: "Leaky", Provider: "claude", Backend: "anthropic", Model: "m1",
			URL:   "https://" + profSecretUser + ":" + profSecretPass + "@gw.example/v1?api_key=" + profSecretQuery + "#" + profSecretFrag,
			Token: profSecretToken,
			Env:   map[string]string{"API_KEY": profSecretEnv},
		},
		// A user profile shadowing a builtin.
		profile.Profile{Name: "canary", Provider: "claude", Backend: "anthropic"},
	)
}

func readReq(id int, uri string) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "resources/read", "params": map[string]any{"uri": uri}})
	return string(b)
}

// profileRPC runs requests through the server with prof and returns the raw
// output and the decoded replies.
func profileRPC(t *testing.T, prof profilesView, requests ...string) (string, []map[string]any) {
	t.Helper()
	var out bytes.Buffer
	if err := serveDisconnects(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out, t.TempDir(), testSession, prof); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad reply %q: %v", line, err)
		}
		replies = append(replies, m)
	}
	return out.String(), replies
}

func replyText(t *testing.T, reply map[string]any) string {
	t.Helper()
	res, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("reply = %v, want a result", reply)
	}
	return res["contents"].([]any)[0].(map[string]any)["text"].(string)
}

func errCode(reply map[string]any) int {
	e, ok := reply["error"].(map[string]any)
	if !ok {
		return 0
	}
	return int(e["code"].(float64))
}

// The list names every merged profile with its source and the digest `clown
// profile digest` prints; a definition is the redacted form plus the moxy
// table, digests and stamp; no secret is in any reply.
func TestProfileResourcesListAndDefinitions(t *testing.T) {
	path := leakyProfileRegistry(t)
	raw, replies := profileRPC(t, profilesView{Definitions: true, RegistryPath: path},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/templates/list"}`,
		readReq(4, profilesURI),
		readReq(5, profileURI("leaky")),
		readReq(6, profileURI("canary")),
		readReq(7, profileURI("no-such-profile")),
		readReq(8, profilesURI+"/leaky/../x"),
	)
	for _, s := range []string{profSecretUser, profSecretPass, profSecretQuery, profSecretFrag, profSecretToken, profSecretEnv} {
		if strings.Contains(raw, s) {
			t.Errorf("a reply carries the secret %q", s)
		}
	}
	listed, _ := json.Marshal(replies[1]["result"])
	if !strings.Contains(string(listed), `"uri":"`+profilesURI+`"`) {
		t.Errorf("resources/list lacks %s: %s", profilesURI, listed)
	}
	templates, _ := json.Marshal(replies[2]["result"])
	if !strings.Contains(string(templates), profilesURI+"/{name}") {
		t.Errorf("templates lack the definition template: %s", templates)
	}

	var list profileList
	if err := json.Unmarshal([]byte(replyText(t, replies[3])), &list); err != nil {
		t.Fatal(err)
	}
	if list.Error != "" {
		t.Errorf("list error = %q", list.Error)
	}
	sources := map[string]profileListEntry{}
	for _, e := range list.Profiles {
		sources[e.Name] = e
	}
	if sources["leaky"].Source != "user" || sources["canary"].Source != "user override" || sources["worker"].Source != "builtin" {
		t.Errorf("sources = %+v", sources)
	}
	for _, name := range []string{"leaky", "canary", "worker"} {
		var out, errb bytes.Buffer
		if code := profileDigest(name, path, &out, &errb); code != 0 {
			t.Fatalf("profile digest %s: %d %s", name, code, errb.String())
		}
		if e := sources[name]; e.Digest != strings.TrimSpace(out.String()) || e.URI != profileURI(name) {
			t.Errorf("%s: entry %+v, want digest %s", name, e, out.String())
		}
	}

	var d profileDefinition
	if err := json.Unmarshal([]byte(replyText(t, replies[4])), &d); err != nil {
		t.Fatal(err)
	}
	if d.Name != "leaky" || d.Model != "m1" || d.URL != profilelookup.RedactedURL || !d.TokenSet ||
		strings.Join(d.EnvKeys, ",") != "API_KEY" || d.Source != "user" || d.OverridesBuiltin ||
		d.Digest != sources["leaky"].Digest || !d.Registry.Present || d.Registry.Protocol < 1 || d.Protocol != profilelookup.ProtocolVersion {
		t.Errorf("leaky definition = %+v", d)
	}
	var c profileDefinition
	if err := json.Unmarshal([]byte(replyText(t, replies[5])), &c); err != nil {
		t.Fatal(err)
	}
	if c.Source != "user" || !c.OverridesBuiltin || c.TokenSet || len(c.EnvKeys) != 0 {
		t.Errorf("canary definition = %+v", c)
	}
	for _, i := range []int{6, 7} {
		if errCode(replies[i]) != rpcResourceNotFound {
			t.Errorf("reply %d = %v, want resource not found", i, replies[i])
		}
	}
}

// A builtin's definition carries its moxy table and context keep-set.
func TestProfileResourceBuiltinDefinitionCarriesMoxyAndKeepSet(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.toml")
	want, err := profilelookup.Resolve("worker", profilelookup.Options{RegistryPath: absent})
	if err != nil {
		t.Fatal(err)
	}
	_, replies := profileRPC(t, profilesView{Definitions: true, RegistryPath: absent}, readReq(1, profileURI("worker")))
	var d profileDefinition
	if err := json.Unmarshal([]byte(replyText(t, replies[0])), &d); err != nil {
		t.Fatal(err)
	}
	if d.Source != "builtin" || d.Digest != want.DigestSHA256 || strings.Join(d.ContextServers, ",") != strings.Join(want.Selection.Kept, ",") || d.Registry.Present {
		t.Errorf("worker definition = %+v", d)
	}
	if (want.Moxy == nil) != (d.Moxy == nil) || (d.Moxy != nil && d.Moxy.Placement != want.Moxy.Placement) {
		t.Errorf("moxy = %+v, want %+v", d.Moxy, want.Moxy)
	}
}

// A session served the list only (a confined launch) reads the list, without
// definition URIs, and no definition at all.
func TestProfileResourcesListOnlyServesNoDefinitions(t *testing.T) {
	path := leakyProfileRegistry(t)
	raw, replies := profileRPC(t, profilesView{RegistryPath: path},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/templates/list"}`,
		readReq(3, profilesURI),
		readReq(4, profileURI("leaky")),
		readReq(5, profileURI("worker")),
	)
	if strings.Contains(raw, "gw.example") || strings.Contains(raw, "API_KEY") || strings.Contains(raw, profilesURI+"/{name}") {
		t.Errorf("a list-only server leaks a definition detail: %s", raw)
	}
	var list profileList
	if err := json.Unmarshal([]byte(replyText(t, replies[2])), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Profiles) == 0 {
		t.Fatal("the list is empty")
	}
	for _, e := range list.Profiles {
		if e.URI != "" || e.Digest == "" {
			t.Errorf("entry %+v: want a digest and no definition URI", e)
		}
	}
	for _, i := range []int{3, 4} {
		if errCode(replies[i]) != rpcResourceNotFound {
			t.Errorf("reply %d = %v, want resource not found", i, replies[i])
		}
	}
}

// A registry this build refuses still lists the builtins, with the refusal,
// and nothing from the refused file.
func TestProfileResourceListShowsBuiltinsBesideARefusedRegistry(t *testing.T) {
	_, replies := profileRPC(t, profilesView{Definitions: true, RegistryPath: writeRegistry(t, skewedRegistry)}, readReq(1, profilesURI))
	var list profileList
	if err := json.Unmarshal([]byte(replyText(t, replies[0])), &list); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Error, "refuses the registry") {
		t.Errorf("list error = %q", list.Error)
	}
	for _, e := range list.Profiles {
		if e.Name == "mine" || e.Source != "builtin" {
			t.Errorf("entry %+v from the refused registry", e)
		}
	}
	if len(list.Profiles) == 0 {
		t.Error("no builtins listed")
	}
}

// The plugin passes definitions to an unconfined launch only.
func TestAddDiagnosticsPluginServesDefinitionsOnlyUnconfined(t *testing.T) {
	for name, c := range map[string]struct {
		spec string
		want string
	}{
		"unconfined": {"", profilesServeDefinitions},
		"confined":   {"/spec.json", profilesServeList},
	} {
		t.Run(name, func(t *testing.T) {
			root, err := staging.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			dir, err := synthDiagnosticsPluginDir(root, "/bin/clown", testSession, "/state/disconnects", servesProfileDefinitions(parsedFlags{agentScopeSpec: c.spec}))
			if err != nil {
				t.Fatal(err)
			}
			args := diagnosticsServerArgs(t, dir)
			if strings.Join(args, " ") != "disconnects-serve --session "+testSession+" --root /state/disconnects --profiles "+c.want {
				t.Errorf("args = %v", args)
			}
		})
	}
}

// diagnosticsServerArgs is the args of the diagnostics plugin's one server.
func diagnosticsServerArgs(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "clown.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Stdio map[string]struct {
			Args []string `json:"args"`
		} `json:"stdioServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Stdio[disconnectsServerName].Args
}

func TestRunDisconnectsServeRejectsABadProfilesMode(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runDisconnectsServe([]string{"--session", "x", "--root", "/r", "--profiles", "all"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}
