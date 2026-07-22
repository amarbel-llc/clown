// Session-key derivation for `clown resume <repo>/<worktree>` (clown#192).
//
// A session's KEY is the spinclass-shaped human handle "repo/branch" derived
// from the transcript-recorded CWD and GitBranch — the same string
// cmd/clown's title fallback (gitRepoAndBranch, clown#180) shows for a live
// session in that directory, and the same string spinclass uses as its
// session key for worktrees (worktree branch == worktree name). Deriving it
// from the RECORDED fields means resume-by-key needs no new persistence and
// works for conversations whose directory has since been deleted.
package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Key returns this session's "repo/branch" handle, or a degraded form when
// fields are missing: "repo" alone when no branch was recorded, "" when no
// repo can be determined at all (callers treat "" as unmatchable).
//
// Repo resolution prefers asking git at the recorded CWD (correct even for
// unusual layouts), but only when the directory still exists — a deleted
// worktree must not fail key derivation, so it falls back to the eng
// path-shape convention `.../repos/<repo>/.worktrees/<wt>` (in which case the
// worktree segment, not GitBranch, is authoritative) and finally to the
// basename of the nearest ancestor that looks like a repo checkout.
func (s Session) Key() string {
	repo, worktree := repoFromCWD(s.CWD)
	if repo == "" {
		return ""
	}
	branch := s.GitBranch
	if worktree != "" {
		branch = worktree
	}
	if branch == "" {
		return repo
	}
	return repo + "/" + branch
}

// repoFromCWD resolves (repo, worktreeName) for a recorded cwd. worktreeName
// is non-empty only when the path-shape fallback identified an explicit
// `.worktrees/<name>` segment (the spinclass layout), in which case it is
// more authoritative than a recorded git branch.
func repoFromCWD(cwd string) (repo, worktreeName string) {
	if cwd == "" {
		return "", ""
	}

	// Path-shape first when it matches: it is cheap, works for deleted
	// dirs, and for spinclass worktrees the `<repo>/.worktrees/<wt>`
	// segments are definitionally the session key.
	parts := strings.Split(filepath.Clean(cwd), string(filepath.Separator))
	for i := len(parts) - 1; i > 0; i-- {
		if parts[i] == ".worktrees" && i+1 < len(parts) {
			return parts[i-1], parts[i+1]
		}
	}

	// Live directory: ask git for the toplevel (handles nested cwds and
	// non-spinclass layouts).
	if st, err := os.Stat(cwd); err == nil && st.IsDir() {
		out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
		if err == nil {
			base := filepath.Base(strings.TrimSpace(string(out)))
			if base != "" && base != "." && base != string(filepath.Separator) {
				return base, ""
			}
		}
	}

	// Dead directory, no worktree shape: the basename is the best remaining
	// guess (a main-checkout cwd IS the repo dir in the common case).
	base := filepath.Base(filepath.Clean(cwd))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "", ""
	}
	return base, ""
}

// FilterByKey returns the sessions whose Key() equals key, preserving input
// order (ListClaudeSessions order is mtime-descending, so the first match is
// the most recent conversation for that key).
func FilterByKey(ss []Session, key string) []Session {
	if key == "" {
		return nil
	}
	var out []Session
	for _, s := range ss {
		if s.Key() == key {
			out = append(out, s)
		}
	}
	return out
}
