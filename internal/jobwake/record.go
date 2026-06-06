package jobwake

const (
	// SchemaVersion is the journal record `v` and the nudge `wire-version`.
	SchemaVersion = 1

	TypeStarted     = "started"
	TypeProgress    = "progress"
	TypeSucceeded   = "succeeded"
	TypeFailed      = "failed"
	TypeCancelled   = "cancelled"
	TypeInterrupted = "interrupted"
	TypeMessage     = "message"

	// BroadcastKey is the reserved session key naming the broadcast channel
	// (RFC-0009 §2). It is never a real session; records targeted at it land
	// in ChannelID(BroadcastKey) and every monitor services that channel with
	// condvar semantics (RFC-0009 §9).
	BroadcastKey = "*"
)

// Record is one line in a job's JSONL journal (RFC-0009 §4).
type Record struct {
	V         int    `json:"v"`
	Job       string `json:"job"`
	Session   string `json:"session"`
	Source    string `json:"source"`
	From      string `json:"from,omitempty"`
	Type      string `json:"type"`
	Seq       int    `json:"seq"`
	TS        string `json:"ts"`
	Message   string `json:"message,omitempty"`
	ResultRef string `json:"result_ref,omitempty"`
}

// IsTerminal reports whether an event type ends a job's lifecycle (RFC-0009 §5).
// After a terminal record is written no further records may be appended.
func IsTerminal(t string) bool {
	switch t {
	case TypeSucceeded, TypeFailed, TypeCancelled, TypeInterrupted:
		return true
	}
	return false
}

// IsWaking reports whether an event of this type wakes the agent. The waking
// set is the terminal set plus the non-terminal `message` type; unknown and
// reserved types (`needs-attention`) do not wake (RFC-0009 §5).
func IsWaking(t string) bool { return IsTerminal(t) || t == TypeMessage }
