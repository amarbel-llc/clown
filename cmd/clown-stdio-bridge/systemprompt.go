package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// childPromptName is the well-known MCP prompt name the bridge requests when
// clown asks for a dynamic system-prompt fragment. A wrapped child opts in by
// exposing a prompt of this name via prompts/get.
const childPromptName = "system-prompt-append"

// childPromptTimeout bounds the prompts/get round-trip to the child. clown's
// own fetch budget is shorter still; this is a backstop so a wedged child
// cannot hang the HTTP handler indefinitely.
const childPromptTimeout = 5 * time.Second

// fetchChildSystemPrompt issues an MCP prompts/get to the wrapped child for
// the well-known system-prompt-append prompt and returns the concatenated
// text of the returned messages. ok is false on any transport error,
// JSON-RPC error, or unparseable/empty response — the caller maps that to a
// 204 (no fragment). The request predates claude's own MCP initialize, which
// is fine for stateless stdio servers (e.g. ringmaster mcp); a child that
// enforces initialize ordering would simply error and yield 204.
func fetchChildSystemPrompt(ctx context.Context, tr *translator) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, childPromptTimeout)
	defer cancel()
	const idKey = `"clown-system-prompt"`
	reqMsg := []byte(`{"jsonrpc":"2.0","id":"clown-system-prompt","method":"prompts/get","params":{"name":"` + childPromptName + `"}}`)
	resp, err := tr.SendRequest(ctx, idKey, reqMsg)
	if err != nil {
		return "", false
	}
	return parsePromptGetText(resp)
}

// parsePromptGetText extracts and joins the text content of an MCP
// prompts/get result. MCP models each PromptMessage's content as a single
// content object; we keep only text parts.
func parsePromptGetText(resp json.RawMessage) (string, bool) {
	var env struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			Messages []struct {
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		return "", false
	}
	if len(env.Error) > 0 && string(env.Error) != "null" {
		return "", false
	}
	var sb strings.Builder
	for _, m := range env.Result.Messages {
		if m.Content.Type == "text" && m.Content.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(m.Content.Text)
		}
	}
	return sb.String(), sb.Len() > 0
}
