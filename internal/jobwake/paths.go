// Package jobwake implements the clown job-wakeup channel: a durable journal
// plus a lossy UDS-datagram nudge per docs/rfcs/0009-job-wakeup-channel.md.
package jobwake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
)

// SessionKey resolves the active session key per RFC-0009 §2:
// CLOWN_SESSION_ID, else SPINCLASS_SESSION_ID, else CLAUDE_SESSION_ID, else a
// generated random 128-bit value rendered as 32 lowercase hex digits.
func SessionKey() string {
	if v := os.Getenv("CLOWN_SESSION_ID"); v != "" {
		return v
	}
	if v := os.Getenv("SPINCLASS_SESSION_ID"); v != "" {
		return v
	}
	if v := os.Getenv("CLAUDE_SESSION_ID"); v != "" {
		return v
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ChannelID derives the filesystem-safe channel identifier from a session key:
// the first 16 bytes of SHA-256(key) as 32 lowercase hex digits (RFC-0009 §2).
func ChannelID(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:16])
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

// AckFile is the per-channel monitor ack cursor.
func AckFile(channelID string) string {
	return filepath.Join(JournalDir(channelID), ".ack.json")
}

// SocketPath is the per-channel unixgram nudge socket.
func SocketPath(channelID string) string {
	return filepath.Join(runtimeDir(), channelID+".sock")
}
