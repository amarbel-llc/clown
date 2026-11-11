package agentscope

import (
	"path/filepath"
	"strings"
)

// TrimPathKeep are the package base names (name without version or hash) whose
// /nix/store bin directories survive the clown#341 PATH trial: the tools
// recipes and clown's own plumbing call. The compiler toolchain, the other
// agent CLIs, python, bun, fish and conformist are not on it. Entries outside
// /nix/store (login-shell and profile dirs, where git lives on the dev hosts)
// always stay.
var TrimPathKeep = map[string]bool{
	"just": true, "coreutils": true, "findutils": true, "gnugrep": true, "gnused": true,
	"gawk": true, "gnutar": true, "gzip": true, "xz": true, "bzip2": true, "diffutils": true,
	"file": true, "bash": true, "util-linux": true, "git": true,
	"mesa": true, "crap-present": true, "ringmaster": true, "troupe": true, "clown": true,
}

// TrimPath drops, from a PATH, the /nix/store entries whose package is not in
// TrimPathKeep and every duplicate, keeping order. The directory of the shim
// binary (shim[0]) is put first, so clown's own tools resolve from the binary
// that runs the unit.
func TrimPath(path string, shim []string) string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(shim) > 0 {
		add(filepath.Dir(shim[0]))
	}
	for _, p := range strings.Split(path, ":") {
		rest, inStore := strings.CutPrefix(p, "/nix/store/")
		if inStore {
			_, name, _ := strings.Cut(rest, "-")
			if !TrimPathKeep[packageBase(name)] {
				continue
			}
		}
		add(p)
	}
	return strings.Join(out, ":")
}

// packageBase turns "gnugrep-3.12/bin" or "bzip2-1.0.8-bin/bin" into the name
// before the first "-<digit>": "gnugrep", "bzip2".
func packageBase(name string) string {
	name, _, _ = strings.Cut(name, "/")
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '-' && name[i+1] >= '0' && name[i+1] <= '9' {
			return name[:i]
		}
	}
	return name
}
