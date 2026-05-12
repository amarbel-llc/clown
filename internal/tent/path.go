package tent

import "strings"

// FilterPathToNixStore returns the entries of hostPath whose prefix is
// /nix/store/<hash>-<name>/{bin,sbin}, joined by ":" in original order.
//
// Used by --tent-pass-devshell to derive the container's PATH from the
// host's PATH without dragging in entries that won't resolve inside
// the tent's filesystem namespace. Every kept entry resolves through
// the existing read-only /nix/store bind mount; everything else is
// dropped.
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

func isNixStoreBinDir(p string) bool {
	if !strings.HasPrefix(p, "/nix/store/") {
		return false
	}
	return strings.HasSuffix(p, "/bin") || strings.HasSuffix(p, "/sbin")
}
