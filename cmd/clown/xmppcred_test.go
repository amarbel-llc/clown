package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

// fakeTroupeMint installs a `troupe` that records the TROUPE_XMPP_* env it was
// minted with and answers with the given JSON.
func fakeTroupeMint(t *testing.T, answer string) (record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "minted-with")
	script := filepath.Join(dir, "troupe")
	body := "#!/bin/sh\n" +
		"printf 'user=%s file=%s\\n' \"$TROUPE_XMPP_USER\" \"$TROUPE_XMPP_PASSWORD_FILE\" > " + record + "\n" +
		"printf '%s\\n' '" + answer + "'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := buildcfg.TroupePath
	buildcfg.TroupePath = script
	t.Cleanup(func() { buildcfg.TroupePath = old })
	return record
}

// A spawned child inherits its parent's credential; troupe mint would adopt the
// inherited file as the child's (reusing the parent's password). The child must
// mint with neither variable set and end up with only its own credential.
func TestMintNeverSeesOrKeepsAnInheritedParentCredential(t *testing.T) {
	record := fakeTroupeMint(t, `{"jid":"child@x.example","password_file":"/run/user/1/troupe/accounts/child.pass"}`)
	t.Setenv("TROUPE_XMPP_USER", "parent")
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "/run/user/1/troupe/accounts/parent.pass")

	scrubInheritedXMPPCredential()
	mintSessionXMPPCredential("child-key")

	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "user= file=" {
		t.Fatalf("troupe mint ran with an inherited credential: %q", got)
	}
	if u := os.Getenv("TROUPE_XMPP_USER"); u != "child" {
		t.Errorf("TROUPE_XMPP_USER = %q, want child", u)
	}
	if f := os.Getenv("TROUPE_XMPP_PASSWORD_FILE"); f != "/run/user/1/troupe/accounts/child.pass" {
		t.Errorf("TROUPE_XMPP_PASSWORD_FILE = %q, want the child's own", f)
	}
}

func TestMintRefusesAFileThatDoesNotNameItsPrincipal(t *testing.T) {
	fakeTroupeMint(t, `{"jid":"child@x.example","password_file":"/run/user/1/troupe/accounts/parent.pass"}`)
	t.Setenv("TROUPE_XMPP_USER", "")
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "")

	mintSessionXMPPCredential("child-key")

	if f := os.Getenv("TROUPE_XMPP_PASSWORD_FILE"); f != "" {
		t.Errorf("TROUPE_XMPP_PASSWORD_FILE = %q, want unset", f)
	}
	if u := os.Getenv("TROUPE_XMPP_USER"); u != "" {
		t.Errorf("TROUPE_XMPP_USER = %q, want unset", u)
	}
}

func TestMintFailureLeavesNoCredential(t *testing.T) {
	fakeTroupeMint(t, `not json`)
	t.Setenv("TROUPE_XMPP_USER", "parent")
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "/run/user/1/troupe/accounts/parent.pass")

	scrubInheritedXMPPCredential()
	mintSessionXMPPCredential("child-key")

	if os.Getenv("TROUPE_XMPP_PASSWORD_FILE") != "" || os.Getenv("TROUPE_XMPP_USER") != "" {
		t.Fatalf("a failed mint left a credential: user=%q file=%q", os.Getenv("TROUPE_XMPP_USER"), os.Getenv("TROUPE_XMPP_PASSWORD_FILE"))
	}
}

// clown#284: --print-launch-plan is a diagnostic, so it must not run troupe
// mint (which provisions an account, through sudo for a system prosody); the
// plan names the deferred mint instead.
func TestPrintLaunchPlanRunsNoTroupeMint(t *testing.T) {
	record := fakeTroupeMint(t, `{"jid":"k@x.example","password_file":"/run/user/1/troupe/accounts/k.pass"}`)
	t.Setenv("TROUPE_TRANSPORT", "xmpp-native")
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	t.Setenv("TROUPE_XMPP_USER", "")
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "")

	f := parsedFlags{printLaunchPlan: true, identity: sessionIdentity{Key: "k"}}
	provisionSessionXMPPCredential(&f)

	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("troupe mint ran under --print-launch-plan (stat err %v)", err)
	}
	if os.Getenv("TROUPE_XMPP_USER") != "" || os.Getenv("TROUPE_XMPP_PASSWORD_FILE") != "" {
		t.Fatal("a plan must not carry a minted credential")
	}
	if len(f.launchPlanDeferred) != 1 || !strings.Contains(f.launchPlanDeferred[0], "troupe mint") {
		t.Fatalf("launchPlanDeferred = %v, want one troupe mint marker", f.launchPlanDeferred)
	}

	// A real launch still mints.
	g := parsedFlags{identity: sessionIdentity{Key: "k"}}
	provisionSessionXMPPCredential(&g)
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("a real launch must run troupe mint: %v", err)
	}
}

func TestXMPPCredentialConsistent(t *testing.T) {
	for _, c := range []struct {
		user, file string
		want       bool
	}{
		{"abc", "/run/user/1/troupe/accounts/abc.pass", true},
		{"abc", "/run/user/1/troupe/accounts/def.pass", false},
		{"", "/run/user/1/troupe/accounts/.pass", false},
		{"abc", "", false},
	} {
		if got := xmppCredentialConsistent(c.user, c.file); got != c.want {
			t.Errorf("xmppCredentialConsistent(%q, %q) = %v, want %v", c.user, c.file, got, c.want)
		}
	}
}
