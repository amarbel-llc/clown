package ringmaster

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Envelope is the JSON-RPC 2.0 message shape. Either Method (request)
// or Result/Error (response) is populated. ID is opaque to satisfy
// the JSON-RPC 2.0 ID rules (string | number | null).
type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.Number     `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// WriteFrame writes a single JSON-RPC envelope followed by a newline.
func WriteFrame(w io.Writer, env Envelope) error {
	if env.JSONRPC == "" {
		env.JSONRPC = "2.0"
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// ReadFrame reads one newline-terminated JSON envelope from r.
// Callers wrap r in a bufio.Reader if they want buffering across calls.
func ReadFrame(r io.Reader) (Envelope, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	line, err := br.ReadBytes('\n')
	if err != nil {
		return Envelope{}, fmt.Errorf("read frame: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Envelope{}, fmt.Errorf("decode frame: %w", err)
	}
	return env, nil
}
