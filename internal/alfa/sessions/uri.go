package sessions

import (
	"fmt"
	"strings"
)

// uriScheme is the only scheme accepted by ParseURI. Sessions are
// clown-canonical handles, not generic URIs — we don't support http://,
// file://, etc.
const uriScheme = "clown://"

// ParseURI parses a clown session URI of the form clown://<provider>/<id>
// and returns its components. Any deviation from that shape is rejected
// with a descriptive error.
func ParseURI(s string) (provider, id string, err error) {
	rest, ok := strings.CutPrefix(s, uriScheme)
	if !ok {
		return "", "", fmt.Errorf("invalid session uri %q: must start with %q", s, uriScheme)
	}
	provider, id, ok = strings.Cut(rest, "/")
	if !ok || provider == "" || id == "" {
		return "", "", fmt.Errorf("invalid session uri %q: expected %s<provider>/<id>", s, uriScheme)
	}
	if strings.Contains(id, "/") {
		return "", "", fmt.Errorf("invalid session uri %q: id must not contain '/'", s)
	}
	return provider, id, nil
}

// FindByID returns the session whose ID matches id, or nil if none does.
// Caller is responsible for filtering ss to the right provider first.
func FindByID(ss []Session, id string) *Session {
	for i := range ss {
		if ss[i].ID == id {
			return &ss[i]
		}
	}
	return nil
}
