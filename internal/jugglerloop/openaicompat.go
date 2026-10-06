package jugglerloop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// OpenAICompatCodec speaks OpenAI-compatible chat completions (remote
// Style "openai-compat" entries such as OpenRouter). resolved.URL already
// includes "/v1", so only "/chat/completions" is appended; auth is a
// Bearer token.
type OpenAICompatCodec struct{}

type openAIFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAITool struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

// openAIMessage.Content is a pointer so an assistant message carrying only
// tool_calls serialises "content": null, and a null response content
// decodes without error.
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
}

type openAIChoice struct {
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
}

func (OpenAICompatCodec) EncodeRequest(resolved rm.ResolveModelResult, model string, req ModelRequest) (*http.Request, error) {
	body := openAIRequest{
		Model:     upstreamModel(resolved, model),
		MaxTokens: req.MaxTokens,
		Messages:  encodeOpenAIMessages(req.System, req.Agent, req.Turns),
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, openAITool{
			Type: "function",
			Function: openAIFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schemaOrEmptyObject(t.InputSchema),
			},
		})
	}

	url := strings.TrimRight(resolved.URL, "/") + "/chat/completions"
	httpReq, err := newJSONPost(url, body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+resolved.Token)
	return httpReq, nil
}

// encodeOpenAIMessages puts the system prompt first, merges consecutive
// agent text/tool_call turns into one assistant message, and emits each
// tool_result as its own "tool" message.
func encodeOpenAIMessages(system, agent string, turns []Turn) []openAIMessage {
	var msgs []openAIMessage
	if system != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: &system})
	}
	lastIsAssistant := false
	for _, t := range turns {
		role := classifyTurn(t, agent)
		switch role {
		case roleUser:
			text := t.Body.Text
			msgs = append(msgs, openAIMessage{Role: "user", Content: &text})
		case roleAssistant:
			if !lastIsAssistant {
				msgs = append(msgs, openAIMessage{Role: "assistant"})
			}
			m := &msgs[len(msgs)-1]
			if t.Body.Type == BodyToolCall {
				c := t.Body.ToolCall
				m.ToolCalls = append(m.ToolCalls, openAIToolCall{
					ID:       c.CallID,
					Type:     "function",
					Function: openAIFunctionCall{Name: c.Name, Arguments: string(argsOrEmptyObject(c.Args))},
				})
			} else {
				joined := t.Body.Text
				if m.Content != nil {
					joined = *m.Content + joined
				}
				m.Content = &joined
			}
		case roleToolResult:
			text := toolResultText(*t.Body.ToolResult)
			msgs = append(msgs, openAIMessage{Role: "tool", Content: &text, ToolCallID: t.Body.ToolResult.CallID})
		}
		if role != roleSkip {
			lastIsAssistant = role == roleAssistant
		}
	}
	return msgs
}

func (OpenAICompatCodec) DecodeResponse(body []byte) (ModelReply, error) {
	var resp openAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ModelReply{}, err
	}
	if len(resp.Choices) == 0 {
		return ModelReply{}, fmt.Errorf("no choices in response")
	}
	choice := resp.Choices[0]
	var reply ModelReply
	if c := choice.Message.Content; c != nil && *c != "" {
		reply.Turns = append(reply.Turns, Turn{Body: TextBody(*c)})
	}
	for _, tc := range choice.Message.ToolCalls {
		if tc.Function.Name == "" {
			return ModelReply{}, fmt.Errorf("tool call %q has no function name", tc.ID)
		}
		reply.Turns = append(reply.Turns, Turn{Body: ToolCallBody(ToolCall{
			Name:   tc.Function.Name,
			Args:   decodeOpenAIArguments(tc.Function.Arguments),
			CallID: tc.ID,
		})})
	}
	switch choice.FinishReason {
	case "stop":
		reply.Stop = StopEndTurn
	case "tool_calls", "function_call":
		reply.Stop = StopToolUse
	case "length":
		reply.Stop = StopMaxTokens
	default:
		reply.Stop = StopReason(choice.FinishReason)
	}
	return reply, nil
}

// decodeOpenAIArguments turns the wire's JSON-in-a-string arguments into
// raw JSON. Arguments that are not valid JSON are kept as a JSON string so
// the executor (and transcript) still see exactly what the model sent.
func decodeOpenAIArguments(arguments string) json.RawMessage {
	if strings.TrimSpace(arguments) == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(arguments)) {
		return json.RawMessage(arguments)
	}
	quoted, _ := json.Marshal(arguments)
	return quoted
}
