package profilelookup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"

	"code.linenisgreat.com/clown/internal/sessions"
)

// RedactedFormVersion versions the redacted form. It is independent of
// ProtocolVersion: the redacted form is derived from Canonical and never
// feeds the digest, so changing it never changes a digest.
const RedactedFormVersion = 1

// MaxRedactedFormBytes is the largest redacted form a pin stores. A pin of a
// larger form keeps its name and digest but stores no form (the record says
// so), and RedactedFormByDigest then reports it missing.
const MaxRedactedFormBytes = sessions.MaxProfileFormBytes

// redactedForm is the wire shape of the redacted form; field order is the
// key order.
type redactedForm struct {
	Form            int                 `json:"form"`
	Protocol        int                 `json:"protocol"`
	Name            string              `json:"name"`
	Display         string              `json:"display"`
	Provider        string              `json:"provider"`
	Backend         string              `json:"backend"`
	Model           string              `json:"model"`
	URL             string              `json:"url"`
	TokenSet        bool                `json:"token_set"`
	EnvKeys         []string            `json:"env_keys"`
	ContextServers  []string            `json:"context_servers"`
	ContextExcluded map[string][]string `json:"context_excluded"`
	Base            string              `json:"base"`
}

// RedactedForm renders r's canonical form with everything secret-shaped
// removed, for clown to store beside a session's pin (clown#298) and for
// DiffRedacted to compare later, when the pinned definition may no longer
// resolve. It is deterministic one-line JSON:
//
//   - the canonical fields, in canonical order, under a leading "form"
//     (RedactedFormVersion); protocol is the canonical form's;
//   - url is stored as scheme://host[:port] only, never its path (a key in a
//     plain path segment passes Diff's rule); a URL that may carry a
//     credential (the rule on Diff, the same function) is stored as
//     RedactedURL, so its userinfo, query and fragment are never stored;
//   - env is env_keys, the sorted keys only: neither values nor their
//     digests are stored (a low-entropy value is guessable from its digest);
//   - the token is token_set only, as in Canonical.
//
// It fails when r has no readable Canonical (a Resolved not made by
// Resolve). It does not change Canonical or the digest.
func (r *Resolved) RedactedForm() ([]byte, error) {
	if r == nil || len(r.Canonical) == 0 {
		return nil, errors.New("profilelookup: RedactedForm needs a Resolved made by Resolve")
	}
	var c canonical
	if err := json.Unmarshal(r.Canonical, &c); err != nil {
		return nil, fmt.Errorf("profilelookup: reading the canonical form of %q: %w", r.Name, err)
	}
	f := redactedForm{
		Form:            RedactedFormVersion,
		Protocol:        c.Protocol,
		Name:            c.Name,
		Display:         c.Display,
		Provider:        c.Provider,
		Backend:         c.Backend,
		Model:           c.Model,
		URL:             c.URL,
		TokenSet:        c.TokenSet,
		EnvKeys:         make([]string, 0, len(c.Env)),
		ContextServers:  c.ContextServers,
		ContextExcluded: c.ContextExcluded,
		Base:            c.Base,
	}
	f.URL = storedURL(f.URL)
	for k := range c.Env {
		f.EnvKeys = append(f.EnvKeys, k)
	}
	sort.Strings(f.EnvKeys)
	if f.ContextExcluded == nil {
		f.ContextExcluded = map[string][]string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("profilelookup: rendering the redacted form of %q: %w", r.Name, err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// storedURL is the url a redacted form keeps: RedactedURL for one that may
// carry a credential (urlMayCarryCredential, Diff's rule), else only
// scheme://host[:port]. The path is dropped because a key in a plain path
// segment passes that rule, and the sidecar is durable and world-readable.
func storedURL(raw string) string {
	if raw == "" {
		return ""
	}
	if urlMayCarryCredential(raw) {
		return RedactedURL
	}
	u, err := url.Parse(raw) // parses: urlMayCarryCredential is true otherwise
	if err != nil {
		return RedactedURL
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// DiffRedacted is Diff over two redacted forms (RedactedForm), with Diff's
// line contract, order and cap. What the forms do not hold cannot be seen:
// an env value change is invisible (only added and removed keys show), a
// URL path change is invisible (only scheme://host[:port] is stored), and
// two different credential-bearing URLs both read as RedactedURL, so they
// do not show either. A caller that holds both Resolved values should use
// Diff instead. It fails on a form that does not parse or has a form
// version this build does not read.
func DiffRedacted(old, new []byte, maxLines int) (lines []string, more int, err error) {
	o, err := viewOfRedacted(old)
	if err != nil {
		return nil, 0, fmt.Errorf("old form: %w", err)
	}
	n, err := viewOfRedacted(new)
	if err != nil {
		return nil, 0, fmt.Errorf("new form: %w", err)
	}
	lines, more = capChanges(diffViews(o, n), maxLines)
	return lines, more, nil
}

func viewOfRedacted(form []byte) (diffView, error) {
	var f redactedForm
	if err := json.Unmarshal(form, &f); err != nil {
		return diffView{}, fmt.Errorf("unreadable redacted profile form: %w", err)
	}
	if f.Form != RedactedFormVersion {
		return diffView{}, fmt.Errorf("redacted profile form version %d; this build reads %d", f.Form, RedactedFormVersion)
	}
	env := make(map[string]string, len(f.EnvKeys))
	for _, k := range f.EnvKeys {
		env[k] = "" // values are not stored: equal by construction
	}
	c := canonical{
		Name: f.Name, Display: f.Display, Provider: f.Provider, Backend: f.Backend,
		Model: f.Model, URL: f.URL, TokenSet: f.TokenSet, Env: env,
		ContextServers: f.ContextServers, ContextExcluded: f.ContextExcluded, Base: f.Base,
	}
	return diffView{present: true, fields: c, selected: c.ContextServers != nil}, nil
}

// RedactedFormByDigest returns the redacted form clown stored with a
// session pin of digest (the lowercase hex DigestSHA256), read from
// $XDG_STATE_HOME/clown/session-names.jsonl: the last record for digest
// that carries one. ok is false when no pin of digest stored a form (a pin
// written before forms existed, or one over MaxRedactedFormBytes); err is a
// failure to read the sidecar. The form is a claim by whoever could write
// that file, for display only: it is not verified against the digest
// (it cannot be, being redacted), so never use it to decide anything.
func RedactedFormByDigest(digest string) (form []byte, ok bool, err error) {
	return sessions.ProfileFormFor(digest)
}
