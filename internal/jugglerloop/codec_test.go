package jugglerloop

import (
	"encoding/json"
	"io"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

const agent = "agent-1"

func stampAsAgent(turns []Turn) []Turn {
	for i := range turns {
		turns[i].Sender = agent
	}
	return turns
}

func encodedBody(t *testing.T, codec Codec, resolved rm.ResolveModelResult, req ModelRequest) map[string]any {
	t.Helper()
	httpReq, err := codec.EncodeRequest(resolved, "m", req)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	data, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("request body: %v", err)
	}
	return out
}

func TestAnthropicCodec_ToolCallRoundTrip(t *testing.T) {
	codec := AnthropicCodec{}
	reply, err := codec.DecodeResponse([]byte(anthropicCreateIssueReply))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if reply.Stop != StopToolUse || len(reply.Turns) != 2 {
		t.Fatalf("reply = %+v", reply)
	}
	call := reply.Turns[1].Body.ToolCall
	if call == nil || call.Name != "create_issue" || call.CallID != "tu_1" || string(call.Args) != `{"title":"x"}` {
		t.Fatalf("tool call = %+v", call)
	}

	turns := []Turn{{Body: TextBody("task")}}
	turns = append(turns, stampAsAgent(reply.Turns)...)
	turns = append(turns, Turn{Sender: agent, Body: ToolResultBody(ToolResult{CallID: "tu_1", OK: true, Content: json.RawMessage(`{"url":"u"}`)})})
	turns = append(turns, Turn{Sender: agent, Body: TerminalBody(EndTurn)})

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: "http://x/", Token: "k", Style: "anthropic"}
	httpReq, _ := codec.EncodeRequest(resolved, "m", ModelRequest{Agent: agent, Turns: turns, MaxTokens: 10})
	if got := httpReq.URL.String(); got != "http://x/v1/messages" {
		t.Errorf("url = %s", got)
	}
	if got := httpReq.Header.Get("x-api-key"); got != "k" {
		t.Errorf("x-api-key = %q, want the remote token", got)
	}

	body := encodedBody(t, codec, resolved, ModelRequest{Agent: agent, Turns: turns, MaxTokens: 10})
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v (terminal turn must not be sent)", msgs)
	}
	assistant := msgs[1].(map[string]any)
	blocks := assistant["content"].([]any)
	if assistant["role"] != "assistant" || len(blocks) != 2 {
		t.Fatalf("assistant message = %v", assistant)
	}
	toolUse := blocks[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "tu_1" || toolUse["name"] != "create_issue" {
		t.Errorf("tool_use = %v", toolUse)
	}
	if input := toolUse["input"].(map[string]any); input["title"] != "x" {
		t.Errorf("tool_use input = %v", input)
	}
	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "tu_1" || result["content"] != `{"url":"u"}` {
		t.Errorf("tool_result = %v", result)
	}
	if _, present := result["is_error"]; present {
		t.Errorf("is_error should be omitted for an ok result: %v", result)
	}
}

func TestOpenAICompatCodec_ToolCallRoundTrip(t *testing.T) {
	codec := OpenAICompatCodec{}
	reply, err := codec.DecodeResponse([]byte(openAICreateIssueReply))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if reply.Stop != StopToolUse || len(reply.Turns) != 1 {
		t.Fatalf("reply = %+v", reply)
	}
	call := reply.Turns[0].Body.ToolCall
	if call == nil || call.Name != "create_issue" || call.CallID != "call_1" || string(call.Args) != `{"title":"x"}` {
		t.Fatalf("tool call = %+v", call)
	}

	turns := []Turn{{Body: TextBody("task")}}
	turns = append(turns, stampAsAgent(reply.Turns)...)
	turns = append(turns, Turn{Sender: agent, Body: ToolResultBody(ToolResult{CallID: "call_1", OK: false, Content: json.RawMessage(`"nope"`)})})

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: "https://openrouter.example/api/v1", Token: "sk", Style: "openai-compat"}
	httpReq, _ := codec.EncodeRequest(resolved, "m", ModelRequest{Agent: agent, Turns: turns})
	if got := httpReq.URL.String(); got != "https://openrouter.example/api/v1/chat/completions" {
		t.Errorf("url = %s", got)
	}

	body := encodedBody(t, codec, resolved, ModelRequest{System: "sys", Agent: agent, Turns: turns, Tools: issueTools})
	msgs := body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %v", msgs)
	}
	assistant := msgs[2].(map[string]any)
	if assistant["role"] != "assistant" || assistant["content"] != nil {
		t.Errorf("assistant = %v, want null content with tool_calls", assistant)
	}
	tc := assistant["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_1" || tc["type"] != "function" {
		t.Errorf("tool_call = %v", tc)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "nope" {
		t.Errorf("tool message = %v", tool)
	}
	tools := body["tools"].([]any)
	fn := tools[1].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "create_issue" || fn["parameters"].(map[string]any)["type"] != "object" {
		t.Errorf("tool def = %v", fn)
	}
	if _, leaked := fn["kind"]; leaked {
		t.Errorf("kind must not be sent to the provider: %v", fn)
	}
}

func TestOpenAICompatCodec_InvalidArgumentsKeptAsString(t *testing.T) {
	reply, err := OpenAICompatCodec{}.DecodeResponse([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"not json"}}]},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(reply.Turns[0].Body.ToolCall.Args); got != `"not json"` {
		t.Errorf("args = %s", got)
	}
}

func TestCodecFor(t *testing.T) {
	cases := []struct {
		resolved rm.ResolveModelResult
		want     Codec
	}{
		{rm.ResolveModelResult{Kind: rm.ModelKindLocal}, AnthropicCodec{}},
		{rm.ResolveModelResult{Kind: rm.ModelKindRemote, Style: "anthropic"}, AnthropicCodec{}},
		{rm.ResolveModelResult{Kind: rm.ModelKindRemote, Style: "openai-compat"}, OpenAICompatCodec{}},
	}
	for _, tc := range cases {
		got, err := CodecFor(tc.resolved)
		if err != nil || got != tc.want {
			t.Errorf("CodecFor(%+v) = %T, %v", tc.resolved, got, err)
		}
	}
	if _, err := CodecFor(rm.ResolveModelResult{Kind: rm.ModelKindRemote, Style: "decisions"}); err == nil {
		t.Error("decisions style must have no loop codec")
	}
}

func TestTurn_JSONShape(t *testing.T) {
	turn := Turn{ID: "t1", Sender: "agent-1", Parent: "brief-0", Body: ToolCallBody(ToolCall{Name: "f", Args: json.RawMessage(`{}`), CallID: "c"})}
	got, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"t1","sender":"agent-1","parent":"brief-0","at":"0001-01-01T00:00:00Z","body":{"type":"tool_call","tool_call":{"name":"f","args":{},"call_id":"c"}}}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
