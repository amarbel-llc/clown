package jugglerloop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// AnthropicCodec speaks the Anthropic Messages API (local llama-server or a
// remote Style "anthropic" entry): POST <URL>/v1/messages with x-api-key
// and anthropic-version headers, tools as tool_use/tool_result blocks.
type AnthropicCodec struct{}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
}

func (AnthropicCodec) EncodeRequest(resolved rm.ResolveModelResult, model string, req ModelRequest) (*http.Request, error) {
	// A local llama-server doesn't check the key but the header must be
	// present (same dummy-auth convention as cmd/juggler's prompt.go).
	token := "dummy"
	if resolved.Kind == rm.ModelKindRemote {
		token = resolved.Token
	}

	body := anthropicRequest{
		Model:     model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
		Messages:  encodeAnthropicMessages(req.Agent, req.Turns),
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schemaOrEmptyObject(t.InputSchema),
		})
	}

	url := strings.TrimRight(resolved.URL, "/") + "/v1/messages"
	httpReq, err := newJSONPost(url, body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("x-api-key", token)
	return httpReq, nil
}

// encodeAnthropicMessages merges consecutive same-role turns into one
// message, as the Messages API requires alternating roles; tool_result
// blocks ride in user messages.
func encodeAnthropicMessages(agent string, turns []Turn) []anthropicMessage {
	var msgs []anthropicMessage
	appendBlock := func(role string, b anthropicBlock) {
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, b)
			return
		}
		msgs = append(msgs, anthropicMessage{Role: role, Content: []anthropicBlock{b}})
	}
	for _, t := range turns {
		switch classifyTurn(t, agent) {
		case roleUser:
			if t.Body.Text != "" {
				appendBlock("user", anthropicBlock{Type: "text", Text: t.Body.Text})
			}
		case roleAssistant:
			if t.Body.Type == BodyToolCall {
				c := t.Body.ToolCall
				appendBlock("assistant", anthropicBlock{Type: "tool_use", ID: c.CallID, Name: c.Name, Input: argsOrEmptyObject(c.Args)})
			} else if t.Body.Text != "" {
				appendBlock("assistant", anthropicBlock{Type: "text", Text: t.Body.Text})
			}
		case roleToolResult:
			r := t.Body.ToolResult
			appendBlock("user", anthropicBlock{Type: "tool_result", ToolUseID: r.CallID, Content: toolResultText(*r), IsError: !r.OK})
		}
	}
	return msgs
}

func (AnthropicCodec) DecodeResponse(body []byte) (ModelReply, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ModelReply{}, err
	}
	var reply ModelReply
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				reply.Turns = append(reply.Turns, Turn{Body: TextBody(b.Text)})
			}
		case "tool_use":
			if b.Name == "" {
				return ModelReply{}, fmt.Errorf("tool_use block %q has no name", b.ID)
			}
			reply.Turns = append(reply.Turns, Turn{Body: ToolCallBody(ToolCall{Name: b.Name, Args: argsOrEmptyObject(b.Input), CallID: b.ID})})
		}
	}
	switch resp.StopReason {
	case "end_turn", "stop_sequence":
		reply.Stop = StopEndTurn
	case "tool_use":
		reply.Stop = StopToolUse
	case "max_tokens":
		reply.Stop = StopMaxTokens
	default:
		reply.Stop = StopReason(resp.StopReason)
	}
	return reply, nil
}
