package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// `clown --profile` refuses a base spinclass would refuse: the launch and the
// launcher must agree on what a profile narrows.
func TestVerifyProfileBase(t *testing.T) {
	current, err := profilelookup.Resolve("canary-spinclass", profilelookup.Options{RegistryPath: filepath.Join(t.TempDir(), "absent.toml")})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(base string) profile.Profile {
		return profile.Profile{Name: "mine", Provider: "claude", Backend: "anthropic", Base: base}
	}
	for name, c := range map[string]struct {
		base   string
		refuse string
	}{
		"no base":      {"", ""},
		"base":         {"canary-spinclass", ""},
		"current pin":  {"canary-spinclass@sha256:" + current.DigestSHA256, ""},
		"stale pin":    {"canary-spinclass@sha256:" + strings.Repeat("0", 64), "but the builtin now has " + current.DigestSHA256},
		"unknown base": {"no-such-builtin", `no builtin profile is named "no-such-builtin"`},
	} {
		t.Run(name, func(t *testing.T) {
			p := mk(c.base)
			path := filepath.Join(t.TempDir(), "profiles.toml")
			if err := profile.Save(path, []profile.Profile{p}, "0.4.5"); err != nil {
				t.Fatal(err)
			}
			err := verifyProfileBase(p, path)
			if c.refuse == "" {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			var be *profilelookup.BaseError
			if !errors.As(err, &be) || !strings.Contains(err.Error(), c.refuse) {
				t.Errorf("err = %v, want a refusal saying %q", err, c.refuse)
			}
		})
	}
}
