package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// openRouterModelsURL is OpenRouter's model-listing endpoint (issue #195,
// docs/plans/2026-07-26-openrouter-model-picker-design.md Section 2).
const openRouterModelsURL = "https://openrouter.ai/api/v1/models"

// openRouterModel is the subset of OpenRouter's /models response fields the
// picker (openroutermodelpicker.go) actually renders. The real response
// carries many more fields (architecture, top_provider,
// supported_parameters, ...) — deliberately ignored.
type openRouterModel struct {
	ID          string
	ContextLen  int
	PromptPrice float64 // USD per token
	CompPrice   float64 // USD per token
	Description string  // raw, as returned by the API (markdown + entities)
}

type openRouterModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		Description   string `json:"description"`
		Pricing       struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

// fetchOpenRouterModels fetches the live model list, using token as a
// bearer credential. Mirrors internal/jugglermodels/download.go's HTTP
// pattern (context-aware request, timeout client, status check, wrapped
// errors) with a 5s timeout appropriate for an interactive prompt rather
// than a multi-GB download.
func fetchOpenRouterModels(ctx context.Context, token string) ([]openRouterModel, error) {
	return fetchOpenRouterModelsFrom(ctx, openRouterModelsURL, token)
}

// fetchOpenRouterModelsFrom is fetchOpenRouterModels with the base URL
// overridable, so tests can point it at an httptest.Server instead of the
// real API.
func fetchOpenRouterModelsFrom(ctx context.Context, url, token string) ([]openRouterModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var parsed openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]openRouterModel, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		m := openRouterModel{ID: d.ID, ContextLen: d.ContextLength, Description: d.Description}
		m.PromptPrice, _ = strconv.ParseFloat(d.Pricing.Prompt, 64)
		m.CompPrice, _ = strconv.ParseFloat(d.Pricing.Completion, 64)
		out = append(out, m)
	}
	return out, nil
}
