package jugglerloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// ArgsDigest hashes args' canonical JSON encoding (CanonicalJSON; clown#255):
// a sha256, hex-encoded, so two logically-equal argument documents — however
// differently ordered or formatted — digest equal, and a one-byte change in
// either digests differently. If args is not valid JSON at all (e.g.
// truncated by a token limit; see validateToolArgs), CanonicalJSON cannot
// run, so the raw bytes are hashed instead: a digest is still produced and
// still deterministic, just not canonicalized — invalid JSON has no
// canonical form to begin with.
func ArgsDigest(args json.RawMessage) string {
	payload, err := CanonicalJSON(args)
	if err != nil {
		payload = args
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// TrafficRecord is one traffic.jsonl line (clown#255): a tool call's
// arguments as sent, in full, plus enough about its result to join back to
// the ledger entry at the same position without the traffic file carrying
// the result body. Every tool call gets one, including a read-only kind and
// one whose arguments failed validation or whose executor returned a
// transport error (ResultSize 0, OK false in the latter case: there is no
// result content to size). Never read by the evaluator.
type TrafficRecord struct {
	Step       int             `json:"step"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	ArgsDigest string          `json:"args_digest"`
	ResultSize int             `json:"result_size"`
	OK         bool            `json:"ok"`
	TS         time.Time       `json:"ts"`
}
