package clownfile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeFile writes raw content to an arbitrary path (the burned-in base
// clownfile is a single file, not a `Filename` inside a discovered ancestor).
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverCascadeDeeperOverrides(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	child := filepath.Join(repo, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	// Shallow (home): provider=claude, model=opus, env A=1.
	write(t, home, "[profile]\nprovider = \"claude\"\nmodel = \"opus\"\n[profile.env]\nA = \"1\"\n")
	// Deep (repo): override provider, add env B; model + A inherited.
	write(t, repo, "[profile]\nprovider = \"codex\"\n[profile.env]\nB = \"2\"\n")

	cf, err := Discover(child, home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "codex" {
		t.Errorf("provider = %q, want codex (deeper overrides)", cf.Profile.Provider)
	}
	if cf.Profile.Model != "opus" {
		t.Errorf("model = %q, want opus (inherited from shallower)", cf.Profile.Model)
	}
	if cf.Profile.Env["A"] != "1" || cf.Profile.Env["B"] != "2" {
		t.Errorf("env = %v, want A=1,B=2 (union)", cf.Profile.Env)
	}
}

// [profile].profile pins a named registry profile: a deeper clownfile's pin
// overrides a shallower one; absent everywhere leaves it "".
func TestDiscoverProfilePin(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, home, "[profile]\nprofile = \"shallow-pin\"\n")
	write(t, repo, "[profile]\nprofile = \"deep-pin\"\n")

	cf, err := Discover(repo, home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.ProfileName != "deep-pin" {
		t.Errorf("profile pin = %q, want deep-pin (deeper overrides)", cf.Profile.ProfileName)
	}

	home2 := t.TempDir()
	cf2, err := Discover(home2, home2, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cf2.Profile.ProfileName != "" {
		t.Errorf("profile pin = %q, want empty when unset", cf2.Profile.ProfileName)
	}
}

func TestDiscoverAbsentIsZero(t *testing.T) {
	home := t.TempDir()
	cf, err := Discover(home, home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "" || cf.Profile.Model != "" || len(cf.Profile.Env) != 0 {
		t.Errorf("absent clownfile must yield zero value, got %+v", cf)
	}
}

func TestDiscoverMalformedErrors(t *testing.T) {
	home := t.TempDir()
	write(t, home, "this is not = valid toml [[[")
	if _, err := Discover(home, home, "", ""); err == nil {
		t.Fatal("malformed clownfile must error")
	}
}

// The burned-in base clownfile (RFC-0013 §1.3) is the lowest-precedence layer:
// an ancestor clownfile overrides it per key, but its un-overridden keys survive.
func TestDiscoverBaseIsLowestPrecedence(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(t.TempDir(), "default-clownfile")
	writeFile(t, base, "[profile]\nprovider = \"claude\"\n[attach]\nmultiplexer = \"zmx\"\n"+
		"start = [\"zmx\", \"attach\", \"{id}\", \"{entry}\"]\nresume-title = \"{id}\"\n")
	// User clownfile at home overrides multiplexer + provider; start/resume-title inherit from base.
	write(t, home, "[profile]\nprovider = \"codex\"\n[attach]\nmultiplexer = \"none\"\n")

	cf, err := Discover(home, home, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "codex" {
		t.Errorf("provider = %q, want codex (user overrides base)", cf.Profile.Provider)
	}
	if cf.Attach.Multiplexer != "none" {
		t.Errorf("multiplexer = %q, want none (user overrides base)", cf.Attach.Multiplexer)
	}
	if want := []string{"zmx", "attach", "{id}", "{entry}"}; !reflect.DeepEqual(cf.Attach.Start, want) {
		t.Errorf("start = %v, want %v (inherited from base)", cf.Attach.Start, want)
	}
	if cf.Attach.ResumeTitle != "{id}" {
		t.Errorf("resume-title = %q, want {id} (inherited from base)", cf.Attach.ResumeTitle)
	}
}

// With no ancestor clownfile, the burned-in base supplies the defaults.
func TestDiscoverBaseOnlyWhenNoAncestor(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(t.TempDir(), "default-clownfile")
	writeFile(t, base, "[attach]\nmultiplexer = \"zmx\"\nspawn-entry = [\"clown\", \"--\", \"{prompt}\"]\n")
	cf, err := Discover(home, home, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cf.Attach.Enabled() {
		t.Fatal("base default must enable the multiplexer with no ancestor clownfile")
	}
	if want := []string{"clown", "--", "{prompt}"}; !reflect.DeepEqual(cf.Attach.SpawnEntry, want) {
		t.Errorf("spawn-entry = %v, want base default %v", cf.Attach.SpawnEntry, want)
	}
}

// An absent base path is non-fatal (dev builds leave it ""/missing); a present
// but malformed base is an error.
func TestDiscoverBaseAbsentAndMalformed(t *testing.T) {
	home := t.TempDir()
	cf, err := Discover(home, home, filepath.Join(home, "nonexistent-base"), "")
	if err != nil {
		t.Fatalf("absent base must be non-fatal, got %v", err)
	}
	if cf.Attach.Enabled() || cf.Profile.Provider != "" {
		t.Errorf("absent base must yield zero value, got %+v", cf)
	}
	bad := filepath.Join(t.TempDir(), "bad-base")
	writeFile(t, bad, "not = valid [[[")
	if _, err := Discover(home, home, bad, ""); err == nil {
		t.Fatal("malformed base clownfile must error")
	}
}

// The XDG user-global clownfile (clown#149) sits above the burned-in base but
// below the $PWD→$HOME ancestor chain: it overrides base keys, and an ancestor
// (here $HOME/clownfile) overrides it, while its un-overridden keys survive.
func TestDiscoverXDGLayerPrecedence(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(t.TempDir(), "default-clownfile")
	xdg := filepath.Join(t.TempDir(), "xdg-clownfile")
	writeFile(t, base, "[profile]\nprovider = \"claude\"\nmodel = \"opus\"\n")
	// XDG overrides provider, adds backend; model inherits from base.
	writeFile(t, xdg, "[profile]\nprovider = \"codex\"\nbackend = \"lima\"\n")
	// Ancestor ($HOME/clownfile) overrides provider again; backend + model inherit.
	write(t, home, "[profile]\nprovider = \"juggler\"\n")

	cf, err := Discover(home, home, base, xdg)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "juggler" {
		t.Errorf("provider = %q, want juggler (ancestor overrides XDG)", cf.Profile.Provider)
	}
	if cf.Profile.Backend != "lima" {
		t.Errorf("backend = %q, want lima (from XDG, un-overridden)", cf.Profile.Backend)
	}
	if cf.Profile.Model != "opus" {
		t.Errorf("model = %q, want opus (from base, un-overridden)", cf.Profile.Model)
	}
}

// With no base and no ancestor clownfile, the XDG file alone supplies defaults,
// and a malformed XDG file is an error.
func TestDiscoverXDGOnlyAndMalformed(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "xdg-clownfile")
	writeFile(t, xdg, "[profile]\nprovider = \"codex\"\n")
	cf, err := Discover(home, home, "", xdg)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "codex" {
		t.Errorf("provider = %q, want codex (from XDG alone)", cf.Profile.Provider)
	}

	bad := filepath.Join(t.TempDir(), "bad-xdg")
	writeFile(t, bad, "not = valid [[[")
	if _, err := Discover(home, home, "", bad); err == nil {
		t.Fatal("malformed XDG clownfile must error")
	}
}

// pty-suspend is a *bool so a deeper clownfile can flip an enabled default back
// off (a plain bool's false zero value would not override). Verify both the
// unset (off) default and a base=true -> ancestor=false override.
func TestPtySuspendOverride(t *testing.T) {
	// Unset everywhere => off.
	home := t.TempDir()
	cf, err := Discover(home, home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Attach.PtySuspendEnabled() {
		t.Error("unset pty-suspend must be off")
	}

	// base enables it; an ancestor ($HOME/clownfile) turns it back off.
	home2 := t.TempDir()
	base := filepath.Join(t.TempDir(), "default-clownfile")
	writeFile(t, base, "[attach]\npty-suspend = true\n")
	write(t, home2, "[attach]\npty-suspend = false\n")
	cf, err = Discover(home2, home2, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Attach.PtySuspendEnabled() {
		t.Error("ancestor pty-suspend = false must override base = true")
	}

	// base enables it, no override => on.
	home3 := t.TempDir()
	cf, err = Discover(home3, home3, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cf.Attach.PtySuspendEnabled() {
		t.Error("base pty-suspend = true with no override must be on")
	}
}

func TestXDGPath(t *testing.T) {
	// $XDG_CONFIG_HOME set wins.
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if got, want := XDGPath("/home/u"), filepath.Join("/cfg", "clown", Filename); got != want {
		t.Errorf("XDGPath with XDG_CONFIG_HOME = %q, want %q", got, want)
	}
	// Unset falls back to <homeDir>/.config.
	t.Setenv("XDG_CONFIG_HOME", "")
	if got, want := XDGPath("/home/u"), filepath.Join("/home/u", ".config", "clown", Filename); got != want {
		t.Errorf("XDGPath fallback = %q, want %q", got, want)
	}
	// No home and no env yields "" (Discover then skips the layer).
	if got := XDGPath(""); got != "" {
		t.Errorf("XDGPath(\"\") with no env = %q, want empty", got)
	}
}

func TestMessagingEnv(t *testing.T) {
	tru := true
	// local / unset emits nothing.
	for _, transport := range []string{"", "local"} {
		env, err := Messaging{Transport: transport}.Env()
		if err != nil {
			t.Fatalf("transport %q: unexpected error %v", transport, err)
		}
		if len(env) != 0 {
			t.Errorf("transport %q: want empty env, got %v", transport, env)
		}
	}

	// Unknown transport is an error.
	if _, err := (Messaging{Transport: "carrier-pigeon"}).Env(); err == nil {
		t.Error("unknown transport: want error, got nil")
	}

	// xmpp without the required vars fails fast.
	if _, err := (Messaging{Transport: "xmpp", XMPPDomain: "x"}).Env(); err == nil {
		t.Error("xmpp missing muc-domain: want error, got nil")
	}
	if _, err := (Messaging{Transport: "xmpp", XMPPMUCDomain: "x"}).Env(); err == nil {
		t.Error("xmpp missing domain: want error, got nil")
	}

	// xmpp with only the required vars: emits transport + domain + muc, no optionals.
	env, err := Messaging{
		Transport:     "xmpp",
		XMPPDomain:    "clown.local",
		XMPPMUCDomain: "muc.clown.local",
	}.Env()
	if err != nil {
		t.Fatalf("minimal xmpp: %v", err)
	}
	want := map[string]string{
		"TROUPE_TRANSPORT":       "xmpp",
		"TROUPE_XMPP_DOMAIN":     "clown.local",
		"TROUPE_XMPP_MUC_DOMAIN": "muc.clown.local",
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("minimal xmpp env = %v, want %v", env, want)
	}

	// Full xmpp incl. authenticated + credential-by-reference (env-interpolated).
	t.Setenv("MSG_TEST_SECRET_DIR", "/run/secrets")
	env, err = Messaging{
		Transport:        "xmpp",
		XMPPHost:         "127.0.0.1",
		XMPPPort:         5223,
		XMPPDomain:       "clown.local",
		XMPPMUCDomain:    "muc.clown.local",
		XMPPNick:         "sharp-hickory",
		XMPPInsecure:     &tru,
		XMPPUser:         "clown-dev",
		XMPPPasswordFile: "${MSG_TEST_SECRET_DIR}/troupe.pass",
	}.Env()
	if err != nil {
		t.Fatalf("full xmpp: %v", err)
	}
	want = map[string]string{
		"TROUPE_TRANSPORT":          "xmpp",
		"TROUPE_XMPP_HOST":          "127.0.0.1",
		"TROUPE_XMPP_PORT":          "5223",
		"TROUPE_XMPP_DOMAIN":        "clown.local",
		"TROUPE_XMPP_MUC_DOMAIN":    "muc.clown.local",
		"TROUPE_XMPP_NICK":          "sharp-hickory",
		"TROUPE_XMPP_INSECURE":      "1",
		"TROUPE_XMPP_USER":          "clown-dev",
		"TROUPE_XMPP_PASSWORD_FILE": "/run/secrets/troupe.pass",
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("full xmpp env = %v, want %v", env, want)
	}
}
