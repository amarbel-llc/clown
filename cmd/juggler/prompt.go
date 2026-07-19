package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// promptUsage is printed on any `juggler prompt` usage error.
const promptUsage = "usage: juggler prompt <model> [--max-tokens N] [prompt-text...]"

// promptTimeout is a single budget spanning BOTH ResolveModel (which may
// block starting a fresh local llama-server instance) and the completion
// request itself (model generation can be slow) — deliberately one context
// rather than two, so a slow-starting local model doesn't get a second full
// timeout on top of the first.
const promptTimeout = 120 * time.Second

// anthropicMessage is one entry in an Anthropic Messages API request's
// "messages" array. v1 only ever sends a single user turn.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicRequest is the request body sent to POST /v1/messages.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

// anthropicContentBlock is one entry in a Messages API response's
// "content" array. Only "text" blocks are meaningful to this command;
// other block types (e.g. tool_use) are ignored.
type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicResponse is the subset of the Messages API response shape this
// command cares about.
type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
}

// openAICompatMessage is one entry in an OpenAI Chat Completions request's
// or response's "messages"/"message" field.
type openAICompatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAICompatRequest is the request body sent to POST /chat/completions.
type openAICompatRequest struct {
	Model     string                `json:"model"`
	MaxTokens int                   `json:"max_tokens"`
	Messages  []openAICompatMessage `json:"messages"`
}

// openAICompatChoice is one entry in a Chat Completions response's
// "choices" array. Message.Content is null on a tool-call-only response;
// json.Unmarshal leaves the (already zero-value) string field as "" in
// that case rather than erroring, which is the behavior this command wants.
type openAICompatChoice struct {
	Message      openAICompatMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

// openAICompatResponse is the subset of the Chat Completions response
// shape this command cares about.
type openAICompatResponse struct {
	Choices []openAICompatChoice `json:"choices"`
}

// cmdPrompt resolves modelName via the juggler daemon's ResolveModel RPC
// and sends it a single prompt, printing the reply and exiting — a
// `claude -p`-shaped direct smoke path that exercises juggler's
// resolution + HTTP-call machinery without clown or claude-code in the
// loop. First positional arg is the model name (required); remaining
// positionals are joined with a space as the prompt text, or read from
// stdin when no prompt text is given on the command line.
func cmdPrompt(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, promptUsage)
		return 1
	}

	// --max-tokens is recognized in any position (before or after the model
	// name/prompt text), not just after the model name — a flag shouldn't
	// require a specific position relative to positional args.
	maxTokens := 256
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--max-tokens":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "juggler: --max-tokens requires an argument")
				return 1
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "juggler: --max-tokens: %v\n", err)
				return 1
			}
			maxTokens = n
			i++
		case strings.HasPrefix(a, "--max-tokens="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--max-tokens="))
			if err != nil {
				fmt.Fprintf(os.Stderr, "juggler: --max-tokens: %v\n", err)
				return 1
			}
			maxTokens = n
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, promptUsage)
		return 1
	}
	modelName := positional[0]
	promptWords := positional[1:]

	prompt := strings.Join(promptWords, " ")
	if prompt == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "juggler: prompt: reading stdin: %v\n", err)
			return 1
		}
		prompt = strings.TrimSuffix(string(data), "\n")
	}

	ctx, cancel := context.WithTimeout(context.Background(), promptTimeout)
	defer cancel()

	resolved, err := cli.ResolveModel(ctx, rm.ResolveModelParams{Name: modelName})
	if err != nil {
		fmt.Fprintf(os.Stderr, "juggler: prompt: resolve model %q: %v\n", modelName, err)
		return 1
	}

	reply, err := sendPrompt(ctx, http.DefaultClient, resolved, modelName, prompt, maxTokens)
	if err != nil {
		fmt.Fprintf(os.Stderr, "juggler: prompt: %v\n", err)
		return 1
	}
	fmt.Println(reply)
	return 0
}

// sendPrompt dispatches to the protocol-specific sender for the resolved
// endpoint and returns the reply text. Kept separate from cmdPrompt so
// the dispatch itself is unit-testable against an httptest.Server
// without a real juggler daemon in the loop.
//
// A local result is always Anthropic-Messages-shaped by construction
// (llama-cpp with /v1/messages support) and never sets Style, so it
// always takes the Anthropic path regardless of Style's zero value. A
// remote result's Style picks the protocol: "anthropic" or
// "openai-compat" are both supported; anything else is rejected before
// any HTTP call is attempted — sending an unrecognized-shape endpoint a
// guessed request would silently misbehave rather than error.
func sendPrompt(ctx context.Context, httpClient *http.Client, resolved rm.ResolveModelResult, modelName, prompt string, maxTokens int) (string, error) {
	switch {
	case resolved.Kind == rm.ModelKindLocal || resolved.Style == "anthropic":
		return sendAnthropicPrompt(ctx, httpClient, resolved, modelName, prompt, maxTokens)
	case resolved.Style == "openai-compat":
		return sendOpenAICompatPrompt(ctx, httpClient, resolved, modelName, prompt, maxTokens)
	default:
		return "", fmt.Errorf("style %q not yet supported (only anthropic-compatible endpoints)", resolved.Style)
	}
}

// sendAnthropicPrompt issues a single Anthropic Messages API completion
// request against the resolved endpoint and returns the concatenated
// text of the reply's "text"-type content blocks.
func sendAnthropicPrompt(ctx context.Context, httpClient *http.Client, resolved rm.ResolveModelResult, modelName, prompt string, maxTokens int) (string, error) {
	// x-api-key mirrors applyNamedProfile's local-instance dummy-auth
	// convention (cmd/clown/main.go): a local llama-server instance
	// doesn't check the key, but the header must still be present.
	token := "dummy"
	if resolved.Kind == rm.ModelKindRemote {
		token = resolved.Token
	}

	reqBody := anthropicRequest{
		Model:     modelName,
		MaxTokens: maxTokens,
		Messages:  []anthropicMessage{{Role: "user", Content: prompt}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(resolved.URL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response body from %s: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s returned status %d: %s", url, resp.StatusCode, string(body))
	}

	var out anthropicResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parsing response from %s: %w", url, err)
	}
	var sb strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return sb.String(), nil
}

// sendOpenAICompatPrompt issues a single OpenAI Chat Completions request
// against the resolved endpoint and returns the first choice's message
// content. Only reachable for a remote result (a local result is always
// Anthropic-shaped and never reaches this function), so resolved.Token
// is used directly rather than falling back to a dummy value.
//
// resolved.URL is stored including the "/v1" suffix for openai-compat
// entries (matching the OpenAI SDK's baseURL convention, e.g.
// "https://openrouter.ai/api/v1"), so this function appends only
// "/chat/completions" — unlike sendAnthropicPrompt, whose stored URL
// excludes "/v1" and appends "/v1/messages".
func sendOpenAICompatPrompt(ctx context.Context, httpClient *http.Client, resolved rm.ResolveModelResult, modelName, prompt string, maxTokens int) (string, error) {
	reqBody := openAICompatRequest{
		Model:     modelName,
		MaxTokens: maxTokens,
		Messages:  []openAICompatMessage{{Role: "user", Content: prompt}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(resolved.URL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Authorization", "Bearer "+resolved.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response body from %s: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s returned status %d: %s", url, resp.StatusCode, string(body))
	}

	var out openAICompatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parsing response from %s: %w", url, err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no choices in response from %s", url)
	}
	return out.Choices[0].Message.Content, nil
}
