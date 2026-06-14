// Package jobwake implements the clown job-wakeup channel: a durable journal
// plus a lossy UDS-datagram nudge per docs/rfcs/0009-job-wakeup-channel.md.
package jobwake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// SessionKey resolves the active session key per RFC-0009 §2 (see
// ResolveSessionKey for the precedence; this drops the source label).
func SessionKey() string {
	k, _ := ResolveSessionKey()
	return k
}

// ResolveSessionKey resolves the active per-instance session key AND reports
// which precedence branch supplied it (RFC-0009 §2, as amended by RFC-0013 §2.3):
// CLOWN_SESSION_ID, else CLAUDE_SESSION_ID, else a freshly generated UUIDv4
// (source "generated"). SPINCLASS_SESSION_ID is NOT in the routing precedence —
// RFC-0013 demotes it to a non-routing group decoration (see GroupKey). The
// source label backs `clown job whoami` (RFC-0012 §1): it lets a consumer tell a
// leaked/inherited CLOWN_SESSION_ID from a freshly minted per-instance key.
func ResolveSessionKey() (key, source string) {
	if v := os.Getenv("CLOWN_SESSION_ID"); v != "" {
		return v, "CLOWN_SESSION_ID"
	}
	if v := os.Getenv("CLAUDE_SESSION_ID"); v != "" {
		return v, "CLAUDE_SESSION_ID"
	}
	return NewUUID(), "generated"
}

// NewUUID returns a fresh RFC 4122 v4 UUID (lowercase, hyphenated). It is the
// per-instance session-key generator (RFC-0013 §2.1): the generated key doubles
// as the claude --session-id, so it MUST be UUID-shaped. On the (vanishingly
// unlikely) rand failure it returns the nil UUID rather than aborting — the
// session is still resolvable, only entropy is lost.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10x
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ChannelID derives the filesystem-safe channel identifier from a session key:
// the first 16 bytes of SHA-256(key) as 32 lowercase hex digits (RFC-0009 §2).
func ChannelID(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:16])
}

// GroupKey returns the group-label decoration for this session — the
// SPINCLASS_SESSION_ID (RFC-0013 §2.2), or "" when not running under spinclass.
// It is NOT the routing key (see ResolveSessionKey); it names the group of clown
// instances that share a spinclass worktree.
func GroupKey() string { return os.Getenv("SPINCLASS_SESSION_ID") }

// GroupChannel returns the channel a group message fans out on —
// ChannelID(GroupKey()) (RFC-0013 §3.2) — or "" when there is no group
// decoration. Every clown under a spinclass session watches this channel, so a
// message addressed to the SPINCLASS_SESSION_ID reaches all of them.
func GroupChannel() string {
	k := GroupKey()
	if k == "" {
		return ""
	}
	return ChannelID(k)
}

// ChannelForTarget resolves the channel id for a target session key, applying
// the same empty=>current-session rule as the producer surface (resolveSession).
// It is the session-key addressing path; ValidateChannelID plus the *Channel
// helpers (StatusOfChannel/ResolveSpoolChannel/DoneChannel) are the raw
// channel-id path the operator uses to reach a job by the id `ls --all` prints.
func ChannelForTarget(target string) string {
	return ChannelID(resolveSession(target))
}

var channelIDRe = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// ErrInvalidChannelID is wrapped by ValidateChannelID failures so callers can
// map a malformed operator-supplied channel id to a usage error (exit 2),
// mirroring ErrInvalidJobID on the job-id side.
var ErrInvalidChannelID = errors.New("invalid channel id")

// ValidateChannelID enforces the channel-id alphabet (lowercase hex, 1–64 chars)
// before an operator-supplied id is used to compose a filesystem path. ChannelID
// emits exactly 32 lowercase hex digits; the looser 1–64 bound keeps a future
// short-prefix addressing mode non-breaking while still excluding "/", ".", and
// "..", so a traversal id like "../foo" can never survive. Failures wrap
// ErrInvalidChannelID.
func ValidateChannelID(cid string) error {
	if !channelIDRe.MatchString(cid) {
		return fmt.Errorf("%w %q: must match %s", ErrInvalidChannelID, cid, channelIDRe.String())
	}
	return nil
}

func stateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state")
}

func runtimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return filepath.Join(v, "clown", "jobs")
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	return filepath.Join(tmp, "clown-jobs-"+strconv.Itoa(os.Getuid()))
}

// JournalDir is the per-channel journal directory (created mode 0700).
func JournalDir(channelID string) string {
	return filepath.Join(stateHome(), "clown", "jobs", channelID)
}

// JournalFile is the JSONL file for one job.
func JournalFile(channelID, jobID string) string {
	return filepath.Join(JournalDir(channelID), jobID+".jsonl")
}

// SpoolFile is the producer-written output spool for one job: a sibling of the
// job's journal carrying the live subprocess output (RFC-0010 §1).
func SpoolFile(channelID, jobID string) string {
	return filepath.Join(JournalDir(channelID), jobID+".out")
}

// AckFile is the per-channel monitor ack cursor.
func AckFile(channelID string) string {
	return filepath.Join(JournalDir(channelID), ".ack.json")
}

// SocketPath is the per-channel unixgram nudge socket.
func SocketPath(channelID string) string {
	return filepath.Join(runtimeDir(), channelID+".sock")
}
