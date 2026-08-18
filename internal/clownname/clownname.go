// Package clownname implements clown#169's human-ergonomic session names: a
// curated pool of famous clown names, allocated so no two live clown sessions
// ever hold the same one, with a generation suffix ("bozo-2") on reuse.
//
// The allocator is entirely clown-owned (design decision, clown#179): it
// treats ringmaster's presence index (jobwake.ListPresence) as the sole
// liveness truth — a name is "in use" iff some LIVE (non-stale) presence
// record already carries it — and never invents a second liveness signal.
// The only cross-process coordination needed is a momentary critical section
// around "read who's alive, pick a free name" (Allocate), guarded by an
// flock on a single well-known lockfile; once chosen, the name is persisted
// by the existing presence mechanism (CLOWN_NAME env -> jobwake.Presence.
// ClownName, clown#179), not by this package.
//
// Allocate/Claim mint a name for a genuinely NEW session. The caller
// (cmd/clown) binds that name to the session lineage in a persistent journal
// (internal/sessions, keyed by the harness session id) and consults the
// binding first, so a restart or resume of the SAME session reuses its bound
// name and only a new session ever reaches Claim (clown#216).
//
// Clown names must never contain '.', which the fleet reserves as the
// separator between decoration components (repo/worktree/clown) in MUC
// room-JID localparts (clown#217). The Pool is dot-free by construction and
// Validate guards any externally-supplied name.
package clownname

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Pool is the curated list of famous clown names, tried in this order. It is
// a var (not const) so a future clownfile-driven override could replace it,
// though no such override exists yet — deliberately not overengineered ahead
// of that need.
var Pool = []string{
	"bozo", "krusty", "pennywise", "pierrot", "coco", "clarabell", "grock",
	"koko", "bobo", "auguste", "ronald", "chuckles", "giggles", "binky",
	"twinkle", "sparkles", "wobbles", "juggles", "doodle", "squeaky",
	"tootsie", "bubbles", "peanut", "waffles", "noodle", "pickles",
	"biscuit", "gumdrop", "marbles", "wrinkles",
}

// Allocate picks a name from Pool that no LIVE session (per liveNames)
// currently holds, walking the pool in order and preferring the lowest
// available generation. When every base name in the pool is taken, it
// returns the lowest-numbered free generation suffix of the FIRST pool
// entry (Pool[0]-2, Pool[0]-3, ...) rather than round-robining through
// every base name's generation 2 first — simple, deterministic, and the
// issue only requires disjunct identities on reuse, not a specific
// round-robin order.
//
// liveNames is the set of names currently held by live sessions (e.g.
// derived from jobwake.ListPresence's ClownName field, once clown#179
// ships). Pure function, no I/O — the caller is responsible for computing
// liveNames under the flock (see Claim) so the read-then-decide step is
// consistent.
func Allocate(liveNames map[string]bool) string {
	for _, base := range Pool {
		if !liveNames[base] {
			return base
		}
	}
	// Every base name is taken: find the lowest free generation of Pool[0].
	// (Pool is never empty in practice, but guard anyway.)
	if len(Pool) == 0 {
		return "clown-1"
	}
	base := Pool[0]
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !liveNames[candidate] {
			return candidate
		}
	}
}

// Validate reports an error when name is not a legal clown name. A clown name
// must not contain '.', which the fleet reserves as the separator between the
// decoration components (repo/worktree/clown) encoded in a MUC room-JID
// localpart — e.g. "circus.clear-walnut@rooms.xmpp.<zone>". '/' being illegal
// in XMPP localparts (RFC 7622), the dot is the chosen encoding, so a dotted
// clown name would make room-JID parsing ambiguous (clown#217). The generated
// Pool names never contain a dot (guarded by TestPoolNamesContainNoDot); this
// guards the externally-supplied path — a user- or env-set CLOWN_NAME.
func Validate(name string) error {
	if strings.Contains(name, ".") {
		return fmt.Errorf("invalid clown name %q: must not contain '.' (reserved as the fleet room-JID component separator, clown#217)", name)
	}
	return nil
}

// lockPath resolves the allocator's flock file: $XDG_STATE_HOME/clown/names.lock,
// or ~/.local/state/clown/names.lock when XDG_STATE_HOME is unset — mirroring
// cmd/clown/configpath.go's userConfigWritePath pattern but for state, not
// config (this is throwaway coordination state, never user-edited).
func lockPath() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "clown", "names.lock"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "clown", "names.lock"), nil
}
