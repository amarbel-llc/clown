package jugglerloop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// ToolSpec is one tool offered to the model. Kind is the artifact kind the
// tool server declares (empty when undeclared); it is recorded in the
// ledger and never sent to the provider.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Kind        string          `json:"kind,omitempty"`
}

// StopReason is the provider's stop reason normalised across codecs.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
)

// ModelRequest is the provider-neutral input to one model round trip.
// Agent is the principal whose text and tool_call turns are the model's own
// (assistant role); text turns from any other sender are user input, and
// tool_result turns always map to the provider's tool-result shape.
// Terminal turns are never sent.
type ModelRequest struct {
	System    string
	Agent     string
	Turns     []Turn
	Tools     []ToolSpec
	MaxTokens int
}

// ModelReply is one parsed provider response. Turns carry only a Body
// (text or tool_call); the loop stamps ID, Sender, Parent and At.
type ModelReply struct {
	Turns []Turn
	Stop  StopReason
}

// Codec projects the stanza-shaped transcript onto one provider wire shape
// and parses that provider's reply back.
type Codec interface {
	EncodeRequest(resolved rm.ResolveModelResult, model string, req ModelRequest) (*http.Request, error)
	DecodeResponse(body []byte) (ModelReply, error)
}

// CodecFor picks the codec the way cmd/juggler's sendPrompt picks a sender:
// a local result or Style "anthropic" speaks Anthropic Messages, Style
// "openai-compat" speaks OpenAI chat completions, anything else is refused
// before any HTTP call.
func CodecFor(resolved rm.ResolveModelResult) (Codec, error) {
	switch {
	case resolved.Kind == rm.ModelKindLocal || resolved.Style == "anthropic":
		return AnthropicCodec{}, nil
	case resolved.Style == "openai-compat":
		return OpenAICompatCodec{}, nil
	default:
		return nil, fmt.Errorf("style %q has no loop codec", resolved.Style)
	}
}

// RoundTrip sends one encoded request and decodes the reply. A non-2xx
// status is an error carrying the response body.
func RoundTrip(ctx context.Context, httpClient *http.Client, codec Codec, resolved rm.ResolveModelResult, model string, req ModelRequest) (ModelReply, error) {
	httpReq, err := codec.EncodeRequest(resolved, model, req)
	if err != nil {
		return ModelReply{}, err
	}
	httpReq = httpReq.WithContext(ctx)
	url := httpReq.URL.String()

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return ModelReply{}, fmt.Errorf("request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ModelReply{}, fmt.Errorf("reading response body from %s: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ModelReply{}, fmt.Errorf("%s returned status %d: %s", url, resp.StatusCode, string(body))
	}
	reply, err := codec.DecodeResponse(body)
	if err != nil {
		return ModelReply{}, fmt.Errorf("parsing response from %s: %w", url, err)
	}
	return reply, nil
}

func newJSONPost(url string, payload any) (*http.Request, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	return req, nil
}

type wireRole int

const (
	roleSkip wireRole = iota
	roleUser
	roleAssistant
	roleToolResult
)

func classifyTurn(t Turn, agent string) wireRole {
	switch t.Body.Type {
	case BodyToolResult:
		return roleToolResult
	case BodyToolCall:
		return roleAssistant
	case BodyText:
		if t.Sender == agent {
			return roleAssistant
		}
		return roleUser
	default:
		return roleSkip
	}
}

// toolResultText renders a tool result's content as the plain string both
// providers accept: a JSON string is unquoted, any other JSON is sent
// verbatim, and an empty content falls back to the Error text.
func toolResultText(r ToolResult) string {
	if len(r.Content) == 0 {
		return r.Error
	}
	var s string
	if json.Unmarshal(r.Content, &s) == nil {
		return s
	}
	return string(r.Content)
}

var emptyObjectSchema = json.RawMessage(`{"type":"object"}`)

func schemaOrEmptyObject(s json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(s)) == 0 {
		return emptyObjectSchema
	}
	return s
}

func argsOrEmptyObject(a json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(a)) == 0 {
		return json.RawMessage(`{}`)
	}
	return a
}
