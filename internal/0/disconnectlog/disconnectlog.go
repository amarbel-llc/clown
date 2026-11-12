// Package disconnectlog is the plugin disconnect record (clown#344, design
// note docs/plans/2026-11-06-clown-344-disconnect-record.md): one JSON object
// per disconnect of a plugin's bridged MCP server, written by the bridge (what
// it sees of its child) or by the plugin host (the bridge itself dying), read
// back by the session as an MCP resource served from the same directory.
//
// It imports only the standard library, so the bridge, the host and the
// frontend can all use it and it sits at the bottom of the layout. Every
// function takes the records root as a parameter: callers decide where it is
// ($XDG_STATE_HOME/clown/disconnects).
//
// Layout under root: <hex sha256(principal)>/<plugin>/<server>/<n>.json, n
// from 1. A record is written whole by a temp file and a link, so a reader
// never sees a partial one and two writers never take the same n.
package disconnectlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Version is the record schema version.
	Version = 1
	// MaxRecords is how many records a server keeps; the oldest goes first.
	MaxRecords = 20
	// Retention is how long a session's records are kept after the newest.
	Retention = 30 * 24 * time.Hour
	// StderrTailLines and StderrLineBytes bound the child's stderr tail.
	StderrTailLines = 40
	StderrLineBytes = 512
	// MaxInFlight bounds the in-flight lists of one record.
	MaxInFlight = 100

	// TimeFormat is RFC 3339 UTC with nine fraction digits, so times sort as
	// strings (the status file's format).
	TimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

	// URIScheme is the scheme of the resource URIs.
	URIScheme = "clown://session/"
)

// The kinds of disconnect a record names.
const (
	KindChildExit        = "child_exit"
	KindChildStdoutEOF   = "child_stdout_eof"
	KindChildStdinClosed = "child_stdin_closed"
	KindSignal           = "signal"
	KindParentGone       = "parent_gone"
	KindHTTPServeError   = "http_serve_error"
	KindBridgeExit       = "bridge_exit"
	// KindServerExit is a started server that is not a bridge (a native
	// httpServers plugin) ending while the host did not ask for it (clown#241).
	// ChildExit holds how its process ended.
	KindServerExit = "server_exit"
)

// Which side closed.
const (
	ClosedByChild    = "child"
	ClosedByBridge   = "bridge"
	ClosedByHost     = "host"
	ClosedByExternal = "external"
	ClosedByUnknown  = "unknown"
)

// Exit is how a process ended: its exit code, and the signal name when it was
// killed by one.
type Exit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}

// InFlight is one request that had no response when the connection ended.
type InFlight struct {
	ID        string `json:"id"`
	Method    string `json:"method,omitempty"`
	StartedAt string `json:"started_at"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// StderrLine is one line of the child's stderr, already scrubbed.
type StderrLine struct {
	At   string `json:"at"`
	Line string `json:"line"`
}

// Record is one disconnect. Arguments and results of requests are never in it.
type Record struct {
	Version         int          `json:"version"`
	Session         string       `json:"session"`
	Plugin          string       `json:"plugin"`
	Server          string       `json:"server"`
	N               int          `json:"n"`
	At              string       `json:"at"`
	Kind            string       `json:"kind"`
	ClosedBy        string       `json:"closed_by"`
	BridgePID       int          `json:"bridge_pid,omitempty"`
	ChildPID        int          `json:"child_pid,omitempty"`
	ChildExit       *Exit        `json:"child_exit,omitempty"`
	BridgeExit      *Exit        `json:"bridge_exit,omitempty"`
	UptimeMS        int64        `json:"uptime_ms,omitempty"`
	InFlight        []InFlight   `json:"in_flight"`
	Abandoned       []InFlight   `json:"abandoned"`
	QueueDepth      int          `json:"queue_depth"`
	DroppedOutbound int64        `json:"dropped_outbound"`
	StderrTail      []StderrLine `json:"stderr_tail"`
	HostLog         string       `json:"host_log,omitempty"`
	URI             string       `json:"uri"`
}

// Entry is one row of a listing.
type Entry struct {
	Plugin string `json:"plugin"`
	Server string `json:"server"`
	N      int    `json:"n"`
	At     string `json:"at"`
	Kind   string `json:"kind"`
	URI    string `json:"uri"`
}

// Now is the current time in TimeFormat.
func Now() string { return time.Now().UTC().Format(TimeFormat) }

// SessionDir is the directory name a principal's records live under.
func SessionDir(principal string) string {
	sum := sha256.Sum256([]byte(principal))
	return hex.EncodeToString(sum[:])
}

// Segment reduces a plugin or server name to one safe path and URI segment:
// letters, digits, dot, dash and underscore; anything else becomes an
// underscore, and a leading dot is escaped so "." and ".." never occur.
func Segment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || strings.HasPrefix(out, ".") {
		out = "_" + out
	}
	return out
}

func serverDir(root, principal, plugin, server string) string {
	return filepath.Join(root, SessionDir(principal), Segment(plugin), Segment(server))
}

// URI is the resource URI of record n of a server's disconnects.
func URI(principal, plugin, server string, n int) string {
	return fmt.Sprintf("%s%s/plugins/%s/%s/disconnects/%d", URIScheme, url.PathEscape(principal), Segment(plugin), Segment(server), n)
}

// ListURI is the resource URI of a server's list of records.
func ListURI(principal, plugin, server string) string {
	return fmt.Sprintf("%s%s/plugins/%s/%s/disconnects", URIScheme, url.PathEscape(principal), Segment(plugin), Segment(server))
}

// LogURI is the resource URI of a server's log: the scrubbed stderr tail kept
// with its newest record that has one (clown#241).
func LogURI(principal, plugin, server string) string {
	return fmt.Sprintf("%s%s/plugins/%s/%s/log", URIScheme, url.PathEscape(principal), Segment(plugin), Segment(server))
}

// AllURI is the resource URI of the list across every plugin of a session.
func AllURI(principal string) string {
	return URIScheme + url.PathEscape(principal) + "/plugins/disconnects"
}

// RefKind says what a parsed URI names.
type RefKind int

const (
	RefAll  RefKind = iota // every plugin's records of the session
	RefList                // one server's records
	RefOne                 // one record
	RefLog                 // one server's log tail
)

// Ref is a parsed resource URI.
type Ref struct {
	Kind      RefKind
	Principal string
	Plugin    string
	Server    string
	N         int
}

// ParseURI parses one of the three URI shapes; anything else is an error.
func ParseURI(uri string) (Ref, error) {
	rest, ok := strings.CutPrefix(uri, URIScheme)
	if !ok {
		return Ref{}, fmt.Errorf("not a %s URI", URIScheme)
	}
	parts := strings.Split(rest, "/")
	for _, p := range parts {
		if p == "" {
			return Ref{}, errors.New("empty segment in URI")
		}
	}
	if len(parts) < 3 || parts[1] != "plugins" {
		return Ref{}, errors.New("URI is not clown://session/<id>/plugins/...")
	}
	// The principal rides percent-escaped (a "/" in an operator-provided
	// session key must not split the path).
	principal, err := url.PathUnescape(parts[0])
	if err != nil || principal == "" {
		return Ref{}, errors.New("session in URI is not a valid escaped name")
	}
	ref := Ref{Principal: principal}
	switch {
	case len(parts) == 3 && parts[2] == "disconnects":
		ref.Kind = RefAll
	case len(parts) == 5 && parts[4] == "disconnects":
		ref.Kind, ref.Plugin, ref.Server = RefList, parts[2], parts[3]
	case len(parts) == 5 && parts[4] == "log":
		ref.Kind, ref.Plugin, ref.Server = RefLog, parts[2], parts[3]
	case len(parts) == 6 && parts[4] == "disconnects":
		n, err := strconv.Atoi(parts[5])
		if err != nil || n < 1 {
			return Ref{}, fmt.Errorf("record number %q is not a positive integer", parts[5])
		}
		ref.Kind, ref.Plugin, ref.Server, ref.N = RefOne, parts[2], parts[3], n
	default:
		return Ref{}, errors.New("URI names no disconnect resource")
	}
	return ref, nil
}

// ensureRoot makes root and refuses one that is not a plain directory owned by
// this user (a symlink, or another user's), so a record is never written
// through a planted link.
func ensureRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("disconnect records root %s is not a plain directory", root)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("disconnect records root %s is not owned by this user", root)
	}
	return nil
}

func numberOf(name string) (int, bool) {
	base, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(base)
	return n, err == nil && n > 0
}

// numbers lists the record numbers in dir, ascending.
func numbers(dir string) []int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ns []int
	for _, e := range entries {
		if n, ok := numberOf(e.Name()); ok {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	return ns
}

// Write stores rec as the next record of its server and returns it with N,
// URI, Version and At filled. The oldest records beyond MaxRecords are
// removed. Session, Plugin and Server must be set.
func Write(root string, rec Record) (Record, error) {
	if root == "" || rec.Session == "" || rec.Plugin == "" || rec.Server == "" {
		return Record{}, errors.New("disconnect record needs a root, session, plugin and server")
	}
	if err := ensureRoot(root); err != nil {
		return Record{}, err
	}
	dir := serverDir(root, rec.Session, rec.Plugin, rec.Server)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Record{}, err
	}
	rec.Version = Version
	if rec.At == "" {
		rec.At = Now()
	}
	if rec.InFlight == nil {
		rec.InFlight = []InFlight{}
	}
	if rec.Abandoned == nil {
		rec.Abandoned = []InFlight{}
	}
	if rec.StderrTail == nil {
		rec.StderrTail = []StderrLine{}
	}
	n := 1
	if ns := numbers(dir); len(ns) > 0 {
		n = ns[len(ns)-1] + 1
	}
	for attempt := 0; attempt < 50; attempt, n = attempt+1, n+1 {
		rec.N = n
		rec.URI = URI(rec.Session, rec.Plugin, rec.Server, n)
		b, err := json.Marshal(rec)
		if err != nil {
			return Record{}, err
		}
		tmp, err := os.CreateTemp(dir, ".tmp-*")
		if err != nil {
			return Record{}, err
		}
		_, werr := tmp.Write(append(b, '\n'))
		cerr := tmp.Close()
		if err := errors.Join(werr, cerr); err != nil {
			_ = os.Remove(tmp.Name())
			return Record{}, err
		}
		lerr := os.Link(tmp.Name(), filepath.Join(dir, strconv.Itoa(n)+".json"))
		_ = os.Remove(tmp.Name())
		if lerr == nil {
			prune(dir)
			return rec, nil
		}
		if !errors.Is(lerr, os.ErrExist) {
			return Record{}, lerr
		}
	}
	return Record{}, errors.New("disconnect record: no free record number after 50 tries")
}

// prune removes the oldest records of dir beyond MaxRecords.
func prune(dir string) {
	ns := numbers(dir)
	for len(ns) > MaxRecords {
		_ = os.Remove(filepath.Join(dir, strconv.Itoa(ns[0])+".json"))
		ns = ns[1:]
	}
}

// Read returns the JSON of record n of a server of the session.
func Read(root, principal, plugin, server string, n int) ([]byte, error) {
	if root == "" || principal == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(serverDir(root, principal, plugin, server), strconv.Itoa(n)+".json"))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func entryOf(dir, plugin, server string, n int) (Entry, bool) {
	b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(n)+".json"))
	if err != nil {
		return Entry{}, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil {
		return Entry{}, false
	}
	return Entry{Plugin: plugin, Server: server, N: n, At: r.At, Kind: r.Kind, URI: r.URI}, true
}

func sortNewestFirst(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].At != es[j].At {
			return es[i].At > es[j].At
		}
		return es[i].N > es[j].N
	})
}

// List is a server's records, newest first.
func List(root, principal, plugin, server string) []Entry {
	if root == "" || principal == "" {
		return nil
	}
	dir := serverDir(root, principal, plugin, server)
	var es []Entry
	for _, n := range numbers(dir) {
		if e, ok := entryOf(dir, Segment(plugin), Segment(server), n); ok {
			es = append(es, e)
		}
	}
	sortNewestFirst(es)
	return es
}

// LogTail returns the stderr tail of the newest record of the server that kept
// one, as plain text, one "at line" row per line, and whether there was any.
func LogTail(root, principal, plugin, server string) (string, bool) {
	for _, e := range List(root, principal, plugin, server) { // newest first
		b, err := Read(root, principal, plugin, server, e.N)
		if err != nil {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil || len(r.StderrTail) == 0 {
			continue
		}
		var sb strings.Builder
		for _, l := range r.StderrTail {
			sb.WriteString(l.At + " " + l.Line + "\n")
		}
		return sb.String(), true
	}
	return "", false
}

// ListAll is every record of the session across its plugins and servers,
// newest first.
func ListAll(root, principal string) []Entry {
	if root == "" || principal == "" {
		return nil
	}
	base := filepath.Join(root, SessionDir(principal))
	plugins, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var es []Entry
	for _, p := range plugins {
		if !p.IsDir() {
			continue
		}
		servers, err := os.ReadDir(filepath.Join(base, p.Name()))
		if err != nil {
			continue
		}
		for _, s := range servers {
			if !s.IsDir() {
				continue
			}
			dir := filepath.Join(base, p.Name(), s.Name())
			for _, n := range numbers(dir) {
				if e, ok := entryOf(dir, p.Name(), s.Name(), n); ok {
					es = append(es, e)
				}
			}
		}
	}
	sortNewestFirst(es)
	return es
}

// Unseen returns, oldest first, the session's records numbered above the
// highest the caller has seen for their server, and raises those marks in seen
// (keyed "plugin/server" by directory name). It reads only the new records:
// a poll of a quiet session lists directories and opens no file. Numbers only
// grow while a server's directory lives (the oldest records are pruned, the
// highest stays), so the mark never goes stale. Call it once with an empty
// map to take a baseline; the records it returns then are history.
func Unseen(root, principal string, seen map[string]int) []Entry {
	if root == "" || principal == "" {
		return nil
	}
	base := filepath.Join(root, SessionDir(principal))
	plugins, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var es []Entry
	for _, p := range plugins {
		if !p.IsDir() {
			continue
		}
		servers, err := os.ReadDir(filepath.Join(base, p.Name()))
		if err != nil {
			continue
		}
		for _, s := range servers {
			if !s.IsDir() {
				continue
			}
			dir := filepath.Join(base, p.Name(), s.Name())
			key := p.Name() + "/" + s.Name()
			for _, n := range numbers(dir) {
				if n <= seen[key] {
					continue
				}
				seen[key] = n
				// Records are written whole (temp file and link), so one that
				// does not read is damaged or already pruned: skip it, never
				// let it hold the later ones back.
				if e, ok := entryOf(dir, p.Name(), s.Name(), n); ok {
					es = append(es, e)
				}
			}
		}
	}
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].At != es[j].At {
			return es[i].At < es[j].At
		}
		return es[i].N < es[j].N
	})
	return es
}

// Sweep removes every session directory whose newest record is older than
// retention, and returns how many it removed. It is the launch-time floor of
// "pruned with the session": spinclass's close may prune sooner.
func Sweep(root string, now time.Time, retention time.Duration) int {
	sessions, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		dir := filepath.Join(root, s.Name())
		newest := newestMod(dir)
		if newest.IsZero() {
			if fi, err := os.Stat(dir); err == nil {
				newest = fi.ModTime()
			}
		}
		if !newest.IsZero() && now.Sub(newest) > retention {
			if os.RemoveAll(dir) == nil {
				removed++
			}
		}
	}
	return removed
}

// newestMod is the latest modification time of a record file under dir.
func newestMod(dir string) time.Time {
	var newest time.Time
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if _, ok := numberOf(d.Name()); !ok {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest
}

// RemoveSession deletes a session's records now (a close's prune).
func RemoveSession(root, principal string) error {
	if root == "" || principal == "" {
		return errors.New("remove session records needs a root and a session")
	}
	return os.RemoveAll(filepath.Join(root, SessionDir(principal)))
}

var (
	// secretKV: a key naming a token, key, secret or password, then = or :,
	// then the value (quoted, as in JSON, or a bare word).
	secretKV = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:token|key|secret|password)[A-Za-z0-9_.-]*"?)\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`)
	// secretFlag: a command-line flag naming a secret, then its value.
	secretFlag = regexp.MustCompile(`(?i)(--?[A-Za-z0-9_-]*(?:token|key|secret|password)[A-Za-z0-9_-]*)\s+(\S+)`)
	// authHeader: Authorization: token x / Basic x (Bearer has its own rule).
	authHeader = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization\s*[=:]\s*)(?:token|basic)\s+\S+`)
	// urlUserinfo: user:password@ (or a bare token@) in a URL.
	urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@]+@`)
	bearerRe    = regexp.MustCompile(`(?i)\bbearer\s+[^\s"']+`)
	prefixRe    = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9]{8,}|xox[abprs]-[A-Za-z0-9-]{8,}|AKIA[0-9A-Z]{12,})`)
	hexRe       = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	b64Re       = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)
)

const redacted = "<redacted>"

// Scrub makes one stderr line safe to keep: control characters become spaces
// (other non-printing runes are dropped), KEY=VALUE fragments whose key names
// a token, key, secret or password lose the value, Bearer tokens and
// credential-shaped tokens (sk-, ghp_, xox, AKIA prefixes, long hex, long
// base64 with letters and digits) are replaced whole, and the line is cut to
// StderrLineBytes on a character boundary. It errs toward redacting.
func Scrub(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return r
		case unicode.IsControl(r):
			return ' '
		case !unicode.IsPrint(r) && !unicode.IsSpace(r):
			return -1
		}
		return r
	}, s)
	s = urlUserinfo.ReplaceAllString(s, "${1}"+redacted+"@")
	s = bearerRe.ReplaceAllString(s, "Bearer "+redacted)
	s = authHeader.ReplaceAllString(s, "${1}"+redacted)
	s = secretFlag.ReplaceAllString(s, "${1} "+redacted)
	s = secretKV.ReplaceAllString(s, "${1}="+redacted)
	s = prefixRe.ReplaceAllString(s, redacted)
	s = hexRe.ReplaceAllString(s, redacted)
	s = b64Re.ReplaceAllStringFunc(s, func(m string) string {
		var letter, digit bool
		for _, c := range m {
			switch {
			case c >= '0' && c <= '9':
				digit = true
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
				letter = true
			}
		}
		if letter && digit {
			return redacted
		}
		return m
	})
	if len(s) > StderrLineBytes {
		n := StderrLineBytes
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return s
}

// Ring keeps the last StderrTailLines lines of a child's stderr, scrubbed as
// they are added, so an unscrubbed line is never retained.
type Ring struct {
	mu    sync.Mutex
	lines []StderrLine
}

// NewRing returns an empty ring.
func NewRing() *Ring { return &Ring{} }

// Add scrubs line and keeps it, dropping the oldest beyond the bound. A nil
// ring does nothing.
func (r *Ring) Add(line string) {
	if r == nil {
		return
	}
	l := StderrLine{At: Now(), Line: Scrub(line)}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, l)
	if len(r.lines) > StderrTailLines {
		r.lines = r.lines[len(r.lines)-StderrTailLines:]
	}
}

// Snapshot is a copy of the kept lines, oldest first.
func (r *Ring) Snapshot() []StderrLine {
	if r == nil {
		return []StderrLine{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StderrLine{}, r.lines...)
}

// Summary is the one line a failed request and the session's wake carry: what
// disconnected, how, and where the record is. It is built from the record's
// own fields, so it holds nothing the record does not.
func Summary(rec Record) string {
	s := fmt.Sprintf("plugin %s/%s disconnected (%s", rec.Plugin, rec.Server, rec.Kind)
	if rec.ChildExit != nil {
		s += fmt.Sprintf(", child exit %d", rec.ChildExit.Code)
		if rec.ChildExit.Signal != "" {
			s += " by " + rec.ChildExit.Signal
		}
	}
	if rec.BridgeExit != nil {
		s += fmt.Sprintf(", bridge exit %d", rec.BridgeExit.Code)
		if rec.BridgeExit.Signal != "" {
			s += " by " + rec.BridgeExit.Signal
		}
	}
	return s + "); read the record at " + rec.URI + " (MCP server clown-builtin-diagnostics/disconnects)"
}
