package agentscope

import (
	"fmt"
	"sort"
	"strings"
)

// EnvReport is the names-only difference between the launcher's environment
// and the one the unit gets (clown#341 ask 3). It is for logging before any
// enforcement tightens: no value is ever recorded, so it is safe to write to a
// shared log.
type EnvReport struct {
	// Inherited are the frontend's variable names the unit does not get.
	Inherited []string
	// Passed are the frontend's variable names the unit gets (with the
	// frontend's value, or for the temp dirs a redirected one).
	Passed []string
	// Stated are the names the unit gets that the frontend did not supply
	// (clown's own set, the provider binding's, the spec's).
	Stated []string
	// PathEntries counts the entries of the PATH the unit gets, and
	// PathStoreEntries those of them under /nix/store.
	PathEntries      int
	PathStoreEntries int
	// PathStoreNames are the store entries, in PATH order, without hashes.
	PathStoreNames []string
}

// EnvironmentReport computes the difference from the same inputs
// environment() uses, so it cannot disagree with what the unit receives.
func (l Launch) EnvironmentReport() EnvReport {
	set, _ := l.environment()
	unit := map[string]string{}
	for _, kv := range set {
		if k, v, ok := strings.Cut(kv, "="); ok {
			unit[k] = v
		}
	}
	front := map[string]bool{}
	var r EnvReport
	for _, kv := range l.FrontendEnv {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || front[k] {
			continue
		}
		front[k] = true
		if _, in := unit[k]; in {
			r.Passed = append(r.Passed, k)
		} else {
			r.Inherited = append(r.Inherited, k)
		}
	}
	for k := range unit {
		if !front[k] {
			r.Stated = append(r.Stated, k)
		}
	}
	sort.Strings(r.Inherited)
	sort.Strings(r.Passed)
	sort.Strings(r.Stated)
	for _, p := range strings.Split(unit["PATH"], ":") {
		if p == "" {
			continue
		}
		r.PathEntries++
		if rest, ok := strings.CutPrefix(p, "/nix/store/"); ok {
			r.PathStoreEntries++
			r.PathStoreNames = append(r.PathStoreNames, storeName(rest))
		}
	}
	return r
}

// storeName turns "<hash>-name-1.2/bin" into "name-1.2/bin": the package and
// the path inside it, without the hash. Entries outside the store are never
// named (they can sit under a home directory).
func storeName(rest string) string {
	if _, name, ok := strings.Cut(rest, "-"); ok {
		return name
	}
	return rest
}

// String renders one log line: counts, then the names.
func (r EnvReport) String() string {
	return fmt.Sprintf("environment: %d inherited-not-passed [%s]; %d passed [%s]; %d stated [%s]; PATH %d entries (%d in the nix store) [%s]",
		len(r.Inherited), strings.Join(r.Inherited, " "),
		len(r.Passed), strings.Join(r.Passed, " "),
		len(r.Stated), strings.Join(r.Stated, " "),
		r.PathEntries, r.PathStoreEntries, strings.Join(r.PathStoreNames, " "))
}
