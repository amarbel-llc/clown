package agentscope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canaryToken is a fake bearer, never a real credential: every test below
// asserts it appears in no error text and no log.
const canaryToken = "clown-test-bearer-not-real"

func writeTokenFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "anthropic-oauth")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTokenSourceReadsTheFileAndTrimsOneTrailingNewline(t *testing.T) {
	for name, content := range map[string]string{
		"bare":           canaryToken,
		"newline":        canaryToken + "\n",
		"crlf":           canaryToken + "\r\n",
		"double newline": canaryToken + "\n\n",
	} {
		want := canaryToken
		if name == "double newline" {
			want = canaryToken + "\n"
		}
		got, err := TokenSource{Path: writeTokenFile(t, content, 0o600)}.Token()
		if err != nil || got != want {
			t.Errorf("%s: Token() = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestTokenSourceRefusesAFileItCannotTrust(t *testing.T) {
	otherUID := func() int { return os.Getuid() + 1 }
	cases := map[string]struct {
		src  func(t *testing.T) TokenSource
		want string
	}{
		"missing": {
			src:  func(t *testing.T) TokenSource { return TokenSource{Path: filepath.Join(t.TempDir(), "absent")} },
			want: "absent",
		},
		"empty": {
			src:  func(t *testing.T) TokenSource { return TokenSource{Path: writeTokenFile(t, "", 0o600)} },
			want: "empty",
		},
		"only a newline": {
			src:  func(t *testing.T) TokenSource { return TokenSource{Path: writeTokenFile(t, "\n", 0o600)} },
			want: "empty",
		},
		"world readable": {
			src:  func(t *testing.T) TokenSource { return TokenSource{Path: writeTokenFile(t, canaryToken, 0o644)} },
			want: "0644",
		},
		"group readable": {
			src:  func(t *testing.T) TokenSource { return TokenSource{Path: writeTokenFile(t, canaryToken, 0o640)} },
			want: "0640",
		},
		"wrong owner": {
			src: func(t *testing.T) TokenSource {
				return TokenSource{Path: writeTokenFile(t, canaryToken, 0o600), ownerUID: otherUID}
			},
			want: "owned by uid",
		},
		"a directory": {
			src:  func(t *testing.T) TokenSource { return TokenSource{Path: t.TempDir()} },
			want: "regular file",
		},
	}
	for name, tc := range cases {
		src := tc.src(t)
		got, err := src.Token()
		if err == nil {
			t.Errorf("%s: want a refusal, got a token of %d bytes", name, len(got))
			continue
		}
		if got != "" {
			t.Errorf("%s: a refusal returned %q", name, got)
		}
		if !strings.Contains(err.Error(), src.Path) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q should name the path and %q", name, err, tc.want)
		}
		if strings.Contains(err.Error(), canaryToken) {
			t.Errorf("%s: the error leaks the token: %q", name, err)
		}
	}
}

// TestTokenSourceReadsPerRequest: replacing the file is seen by the next read
// and deleting it revokes at once; nothing is cached.
func TestTokenSourceReadsPerRequest(t *testing.T) {
	p := writeTokenFile(t, "clown-test-bearer-one", 0o600)
	src := TokenSource{Path: p}
	if got, _ := src.Token(); got != "clown-test-bearer-one" {
		t.Fatalf("first read = %q", got)
	}
	if err := os.WriteFile(p, []byte("clown-test-bearer-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := src.Token(); got != "clown-test-bearer-two" {
		t.Fatalf("after replacement = %q", got)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got, err := src.Token(); err == nil || got != "" {
		t.Fatalf("after deletion = %q, %v; want a refusal", got, err)
	}
}
