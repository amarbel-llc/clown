package tent

import (
	"path/filepath"
	"strings"
)

// FilterPathToNixStore returns the entries of hostPath whose prefix is
// /nix/store/<hash>-<name>/{bin,sbin}, joined by ":" in original order.
//
// This is the strict filter used by the original `--tent-pass-devshell`
// shape. It rejects profile-link directories (`~/.nix-profile/bin`,
// `/nix/var/nix/profiles/default/bin`) even though they resolve into
// /nix/store, which makes home-manager-managed tools invisible to the
// in-tent agent. RewritePathToNixStore is the preferred entry point;
// this strict variant is kept as the documented fallback for the case
// where an in-tent tool turns out to key off its own profile-link
// path (see FDR-0007's 2026-05-19 Q2 matrix).
//
// Order and duplicates are preserved. PATH semantics treat earlier-wins,
// so reordering would change which binary the agent invokes for a name.
func FilterPathToNixStore(hostPath string) string {
	if hostPath == "" {
		return ""
	}
	var kept []string
	for _, entry := range strings.Split(hostPath, ":") {
		if isNixStoreBinDir(entry) {
			kept = append(kept, entry)
		}
	}
	return strings.Join(kept, ":")
}

// RewritePathToNixStore takes a host $PATH and returns the in-tent $PATH:
// for each entry, follow symlinks to its canonical path, and keep the
// canonical form iff it starts with /nix/store/. Entries that don't
// resolve into /nix/store (e.g. /usr/bin, $HOME/.local/bin) are
// dropped.
//
// This is strategy B from FDR-0007's 2026-05-19 PATH-construction
// matrix. The realpath rewrite makes the in-tent PATH hermetic — every
// kept entry references /nix/store directly and reaches a real binary
// through the existing read-only /nix/store bind, with no dependency
// on $HOME or any other host-side mount.
//
// Empty entries (consecutive colons) are skipped. Resolution errors are
// treated as "not in /nix/store" and the entry is dropped. Duplicates
// in the resolved output ARE preserved — two host PATH entries that
// realpath to the same /nix/store path stay as two entries, mirroring
// the FilterPathToNixStore behavior. Use callers can dedup if they
// want a shorter PATH.
//
// resolver is the symlink-resolution function. Pass filepath.EvalSymlinks
// in production; tests pass a stub so they can run without an actual
// /nix/store on disk. A typical wrapper:
//
//	in := os.Getenv("PATH")
//	out := tent.RewritePathToNixStore(in, filepath.EvalSymlinks)
func RewritePathToNixStore(hostPath string, resolver func(string) (string, error)) string {
	if hostPath == "" {
		return ""
	}
	var kept []string
	for _, entry := range strings.Split(hostPath, ":") {
		if entry == "" {
			continue
		}
		resolved, err := resolver(entry)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(resolved, "/nix/store/") {
			continue
		}
		kept = append(kept, resolved)
	}
	return strings.Join(kept, ":")
}

// EvalSymlinks is the production resolver for RewritePathToNixStore.
// Thin wrapper around filepath.EvalSymlinks kept here so callers can
// reference `tent.EvalSymlinks` symmetrically with `tent.RewritePathToNixStore`
// and so the package surface lists the supported resolvers explicitly.
func EvalSymlinks(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}

func isNixStoreBinDir(p string) bool {
	if !strings.HasPrefix(p, "/nix/store/") {
		return false
	}
	return strings.HasSuffix(p, "/bin") || strings.HasSuffix(p, "/sbin")
}
