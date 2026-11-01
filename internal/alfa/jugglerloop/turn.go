// Package jugglerloop is the tool-use loop behind `juggler run` (FDR 0019
// §1, §4, §8): a stanza-shaped transcript, the runtime-kept ledger, and two
// thin provider codecs (OpenAI-compatible chat completions and Anthropic
// Messages) that project the transcript onto the wire.
package jugglerloop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// TranscriptSchema is the version carried by every Transcript.
const TranscriptSchema = 1

// Transcript is the run's turns written out. It is the canonical record;
// the codecs only project it.
type Transcript struct {
	Schema int    `json:"schema"`
	Turns  []Turn `json:"turns"`
}

// Turn is one stanza-shaped message: a sender principal, a provenance link
// to its parent turn, a signature slot (always empty in slice 0) and a
// typed body.
type Turn struct {
	ID        string    `json:"id"`
	Sender    string    `json:"sender"`
	Parent    string    `json:"parent"`
	Signature string    `json:"signature,omitempty"`
	At        time.Time `json:"at"`
	Body      Body      `json:"body"`
}

// BodyType discriminates a Body.
type BodyType string

const (
	BodyText       BodyType = "text"
	BodyToolCall   BodyType = "tool_call"
	BodyToolResult BodyType = "tool_result"
	BodyTerminal   BodyType = "terminal"
)

// Body is a tagged union: Type names which one of the other fields is set.
type Body struct {
	Type       BodyType    `json:"type"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Terminal   *Terminal   `json:"terminal,omitempty"`
}

// ToolCall is a model's request to invoke a tool. CallID is the
// provider-assigned id that the matching ToolResult echoes.
type ToolCall struct {
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	CallID string          `json:"call_id"`
}

// ToolResult is the runtime's answer to one ToolCall. OK=false with Content
// set is a tool-level error the model sees; Error carries a transport
// failure's message.
type ToolResult struct {
	CallID  string          `json:"call_id"`
	OK      bool            `json:"ok"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Terminal records how the loop ended; it is always the transcript's last
// turn.
type Terminal struct {
	Reason EndReason `json:"reason"`
}

func TextBody(text string) Body { return Body{Type: BodyText, Text: text} }

func ToolCallBody(c ToolCall) Body { return Body{Type: BodyToolCall, ToolCall: &c} }

func ToolResultBody(r ToolResult) Body { return Body{Type: BodyToolResult, ToolResult: &r} }

func TerminalBody(reason EndReason) Body {
	return Body{Type: BodyTerminal, Terminal: &Terminal{Reason: reason}}
}

// NewRandomTurnID returns a 128-bit random hex id, the default turn id
// generator.
func NewRandomTurnID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
