package usageadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const DefaultOpenRouterGenerationEndpoint = "https://openrouter.ai/api/v1/generation"

// OpenRouterGenerationAdapter reads authoritative per-generation details from
// OpenRouter. The API has no time-range listing operation, so callers must
// provide the upstream generation IDs captured from gateway responses.
type OpenRouterGenerationAdapter struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

func (OpenRouterGenerationAdapter) Provider() string { return "openrouter" }

func (a OpenRouterGenerationAdapter) Fetch(ctx context.Context, query Query) ([]Record, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}
	requestIDs := uniqueNonEmpty(query.RequestIDs)
	if len(requestIDs) == 0 {
		return nil, errors.New("OpenRouter generation usage requires request IDs")
	}
	if len(requestIDs) > 100 {
		return nil, errors.New("OpenRouter generation usage accepts at most 100 request IDs")
	}
	if strings.TrimSpace(a.APIKey) == "" {
		return nil, errors.New("OpenRouter generation usage requires a server-side API key")
	}
	endpoint := strings.TrimSpace(a.Endpoint)
	if endpoint == "" {
		endpoint = DefaultOpenRouterGenerationEndpoint
	}
	baseURL, err := url.Parse(endpoint)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("OpenRouter generation endpoint must be an absolute URL")
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	records := make([]Record, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		record, err := a.fetchGeneration(ctx, client, baseURL, requestID)
		if err != nil {
			return nil, err
		}
		if record.OccurredAt.Before(query.StartTime) || record.OccurredAt.After(query.EndTime) {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func (a OpenRouterGenerationAdapter) fetchGeneration(ctx context.Context, client *http.Client, baseURL *url.URL, requestID string) (Record, error) {
	endpoint := *baseURL
	query := endpoint.Query()
	query.Set("id", requestID)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Record{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.APIKey))
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return Record{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
		return Record{}, fmt.Errorf("OpenRouter generation API returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data openRouterGeneration `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return Record{}, fmt.Errorf("decode OpenRouter generation response: %w", err)
	}
	return payload.Data.record(requestID)
}

type openRouterGeneration struct {
	ID                     string      `json:"id"`
	Model                  string      `json:"model"`
	CreatedAt              string      `json:"created_at"`
	Streamed               bool        `json:"streamed"`
	Cancelled              bool        `json:"cancelled"`
	ProviderName           string      `json:"provider_name"`
	TokensPrompt           json.Number `json:"tokens_prompt"`
	TokensCompletion       json.Number `json:"tokens_completion"`
	NativeTokensPrompt     json.Number `json:"native_tokens_prompt"`
	NativeTokensCompletion json.Number `json:"native_tokens_completion"`
	NativeTokensReasoning  json.Number `json:"native_tokens_reasoning"`
	TotalCost              json.Number `json:"total_cost"`
}

func (g openRouterGeneration) record(requestID string) (Record, error) {
	if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.ID) != requestID {
		return Record{}, fmt.Errorf("OpenRouter generation response ID does not match request %q", requestID)
	}
	occurredAt, err := parseProviderTime(g.CreatedAt)
	if err != nil {
		return Record{}, fmt.Errorf("OpenRouter generation %q has invalid created_at", requestID)
	}
	usage := map[string]any{}
	putJSONNumber(usage, "input_tokens", g.TokensPrompt)
	putJSONNumber(usage, "output_tokens", g.TokensCompletion)
	putJSONNumber(usage, "native_input_tokens", g.NativeTokensPrompt)
	putJSONNumber(usage, "native_output_tokens", g.NativeTokensCompletion)
	putJSONNumber(usage, "reasoning_tokens", g.NativeTokensReasoning)
	metadata := map[string]any{"usage_source": "openrouter_generation"}
	if value := strings.TrimSpace(g.ProviderName); value != "" {
		metadata["upstream_provider"] = value
	}
	if value := strings.TrimSpace(g.TotalCost.String()); value != "" {
		metadata["provider_cost"] = value
	}
	status := http.StatusOK
	if g.Cancelled {
		status = 499
	}
	record := Record{
		Provider:   "openrouter",
		RequestID:  requestID,
		API:        "chat.completions",
		Model:      strings.TrimSpace(g.Model),
		OccurredAt: occurredAt,
		Status:     status,
		Stream:     g.Streamed,
		Usage:      usage,
		Metadata:   metadata,
	}
	return record, record.Validate()
}

func uniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func putJSONNumber(target map[string]any, key string, value json.Number) {
	if text := strings.TrimSpace(value.String()); text != "" {
		target[key] = text
	}
}
