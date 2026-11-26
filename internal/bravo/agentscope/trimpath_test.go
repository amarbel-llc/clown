package agentscope

import "testing"

func TestTrimPathKeepsTheNamedStoreEntriesAndEveryNonStoreEntry(t *testing.T) {
	in := "/nix/store/a1-just-1.58.0/bin:/nix/store/b2-gcc-16.2.0/bin:/nix/store/c3-bzip2-1.0.8-bin/bin" +
		":/run/current-system/sw/bin:/nix/store/a1-just-1.58.0/bin:/nix/store/d4-python3.14-flask-3.1.3/bin" +
		":/nix/store/e5-conformist/bin:/nix/store/f6-mesa-0.6.2/bin"
	got := TrimPath(in, []string{"/nix/store/z9-clown-0.4.4/bin/clown", "agent-exec"})
	want := "/nix/store/z9-clown-0.4.4/bin:/nix/store/a1-just-1.58.0/bin:/nix/store/c3-bzip2-1.0.8-bin/bin" +
		":/run/current-system/sw/bin:/nix/store/f6-mesa-0.6.2/bin"
	if got != want {
		t.Errorf("TrimPath =\n %s\nwant\n %s", got, want)
	}
}

func TestPackageBase(t *testing.T) {
	for in, want := range map[string]string{
		"gnugrep-3.12/bin": "gnugrep", "bzip2-1.0.8-bin/bin": "bzip2", "conformist/bin": "conformist",
		"python3.14-flask-3.1.3/bin": "python3.14-flask", "util-linux-2.42.3-bin/bin": "util-linux",
	} {
		if got := packageBase(in); got != want {
			t.Errorf("packageBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathIsUntouchedUnlessTrimIsOn(t *testing.T) {
	l := testLaunch()
	l.FrontendEnv = []string{"PATH=/nix/store/b2-gcc-16.2.0/bin:/nix/store/b2-gcc-16.2.0/bin"}
	if got := unitEnv(l)["PATH"]; got != "/nix/store/b2-gcc-16.2.0/bin:/nix/store/b2-gcc-16.2.0/bin" {
		t.Errorf("PATH changed with TrimPath off: %q", got)
	}
	l.TrimPath = true
	if got := unitEnv(l)["PATH"]; got == "/nix/store/b2-gcc-16.2.0/bin:/nix/store/b2-gcc-16.2.0/bin" {
		t.Errorf("PATH not trimmed with TrimPath on: %q", got)
	}
}
