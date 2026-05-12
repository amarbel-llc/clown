package tent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// parseGitdirLine extracts the value after `gitdir:` in a .git pointer
// file's contents. Returns "" if no such line is present. The first
// matching line wins; trailing whitespace (including the newline) is
// stripped.
//
// Pure: no IO, no allocation beyond the returned string.
func parseGitdirLine(contents string) string {
	const prefix = "gitdir:"
	for _, line := range strings.Split(contents, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

// DiscoverGitWorktreeBinds returns host paths that must be bind-mounted
// into the tent container so the worktree's git metadata resolves from
// inside the namespace.
//
// When Workdir is a regular (non-worktree) git checkout, `.git` is an
// in-tree directory and is already covered by the Workdir bind, so
// this returns nil. When Workdir is a worktree, `.git` is a file
// whose `gitdir:` value points outside the Workdir tree; that path —
// and the common git dir it transitively names through its
// `commondir` file — needs explicit binding.
//
// Returns nil for non-git workdirs and on benign discovery failures
// (e.g. .git is a file but not a worktree pointer). A non-nil error
// indicates a real IO problem the caller should surface.
func DiscoverGitWorktreeBinds(workdir string) ([]string, error) {
	gitMarker := filepath.Join(workdir, ".git")
	info, err := os.Stat(gitMarker)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", gitMarker, err)
	}
	if info.IsDir() {
		// Regular repo — .git is in-tree, covered by the Workdir bind.
		return nil, nil
	}

	raw, err := os.ReadFile(gitMarker)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", gitMarker, err)
	}
	gitdirRel := parseGitdirLine(string(raw))
	if gitdirRel == "" {
		// Not a worktree pointer; nothing to expose beyond Workdir.
		return nil, nil
	}

	gitdir := gitdirRel
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(workdir, gitdir)
	}
	gitdir = filepath.Clean(gitdir)

	// Read the commondir pointer (relative or absolute) from inside
	// gitdir. Missing or unreadable is non-fatal: we fall back to
	// binding just the gitdir, which is enough for HEAD/refs/index
	// even if it misses the common object store.
	var commonDir string
	commonRaw, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	switch {
	case err == nil:
		commonRel := strings.TrimSpace(string(commonRaw))
		if commonRel != "" {
			if filepath.IsAbs(commonRel) {
				commonDir = filepath.Clean(commonRel)
			} else {
				commonDir = filepath.Clean(filepath.Join(gitdir, commonRel))
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		// commondir absent — gitdir alone is best-effort.
	default:
		return nil, fmt.Errorf("read commondir in %s: %w", gitdir, err)
	}

	binds := []string{}
	switch {
	case commonDir == "":
		binds = append(binds, gitdir)
	case isInside(gitdir, commonDir):
		// gitdir is a subdir of commonDir (the normal case for
		// `git worktree add`) — one bind on commonDir covers both.
		binds = append(binds, commonDir)
	default:
		// Disjoint layout (rare; ungit-typical). Bind both.
		binds = append(binds, gitdir, commonDir)
	}

	// Filter out anything already covered by the Workdir bind. Paths
	// inside workdir would produce duplicate `--volume` entries that
	// podman would reject or layer in surprising ways.
	out := make([]string, 0, len(binds))
	for _, b := range binds {
		if isInside(b, workdir) {
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// isInside reports whether child is the same path as parent or a
// descendant of it. Both paths are expected to be cleaned absolute
// paths; this is purely lexical and does not resolve symlinks.
func isInside(child, parent string) bool {
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
