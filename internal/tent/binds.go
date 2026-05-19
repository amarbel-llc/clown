package tent

import (
	"os"
	"path/filepath"
)

// DefaultReadOnlyBindCandidates is the FDR-0007 C+F allowlist (2026-05-19
// update) of host paths bind-mounted read-only into the tent. Each path
// gives the in-tent agent access to a specific class of host state that
// dev work requires but that doesn't fit the writable working-tree
// model:
//
//   - /nix/var       — nix-daemon socket + per-user/system profile-link
//                      targets. Required for `nix --version`, `nix
//                      flake show`, `nix build`, `nix shell`, `nix run`
//                      against the host store.
//   - /etc/nix       — daemon config (extra-platforms,
//                      experimental-features, substituters). Without
//                      this `nix` works but uses default settings,
//                      which often differs from the host.
//   - $HOME/.nix-profile, $HOME/.local/state/nix/profiles/profile
//                    — the user's home-manager profile-link dirs. Combined
//                      with RewritePathToNixStore these put the user's
//                      home-manager tool surface (git, jq, curl, …) on
//                      the in-tent PATH.
//   - $HOME/.gitconfig, $HOME/.config/git
//                    — git identity, signing config, aliases. Read-only;
//                      a future change synthesizes a tent-specific
//                      gitconfig instead of bind-mounting the user's
//                      (FDR-0007 follow-up).
//   - $HOME/.config/nix
//                    — user-level nix config (auth tokens for private
//                      caches, evaluator flags, …).
//   - $HOME/.config/ssh
//                    — ssh client config + known_hosts. Deliberately
//                      separate from $HOME/.ssh/ so the latter (which
//                      may hold private key material) stays
//                      unreachable inside the tent. Some users
//                      (notably this repo's primary author) ship an
//                      ssh wrapper via home-manager that defaults
//                      $SSH_HOME to ~/.config/ssh; pairing that
//                      with SSH_HOME in DefaultEnvPassthrough makes
//                      the wrapper resolve correctly inside the tent.
//                      Hosts without this convention simply have
//                      the path absent and the bind is skipped.
//
// Paths are returned in canonical bind order. Callers should filter to
// existing paths via DefaultReadOnlyBinds, which stats each entry.
func DefaultReadOnlyBindCandidates(home string) []string {
	return []string{
		"/nix/var",
		"/etc/nix",
		filepath.Join(home, ".nix-profile"),
		filepath.Join(home, ".local/state/nix/profiles/profile"),
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config/git"),
		filepath.Join(home, ".config/nix"),
		filepath.Join(home, ".config/ssh"),
	}
}

// DefaultReadOnlyBinds returns the subset of DefaultReadOnlyBindCandidates
// that actually exist on the host. A missing path would otherwise cause
// podman to either fail with `statfs ... no such file or directory` or
// (worse) silently create an empty directory as the bind source — the
// same trap ensureClaudeBindSources guards against for the
// claude-specific writable mounts.
//
// Lstat (not Stat) is used so symlinks like ~/.nix-profile, whose
// target lives under /nix/var, are kept even when the resolver would
// follow them to a dir already covered by the /nix/var mount. The
// in-tent agent expects to see ~/.nix-profile *as* a symlink so PATH
// entries that point to it resolve correctly.
func DefaultReadOnlyBinds(home string) []string {
	var out []string
	for _, p := range DefaultReadOnlyBindCandidates(home) {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}
