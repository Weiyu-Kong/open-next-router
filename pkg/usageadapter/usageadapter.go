// Package usageadapter defines the explicit provider usage reconciliation
// boundary. Provider-specific HTTP/API knowledge belongs in implementations
// of Adapter, not in the proxy execution engine.
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
	"time"
)

type Query struct {
	StartTime   time.Time
	EndTime     time.Time
	InternalKey string
	RequestIDs  []string
}

func (q Query) Validate() error {
	if q.StartTime.IsZero() || q.EndTime.IsZero() || !q.EndTime.After(q.StartTime) {
		return errors.New("usage query requires an increasing start and end time")
	}
	if q.EndTime.Sub(q.StartTime) > 31*24*time.Hour {
		return errors.New("usage query range cannot exceed 31 days")
	}
	return nil
}

type Record struct {
	Provider   string         `json:"provider"`
	RequestID  string         `json:"request_id"`
	API        string         `json:"api,omitempty"`
	Model      string         `json:"model,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
	Status     int            `json:"status,omitempty"`
	Stream     bool           `json:"stream,omitempty"`
	Usage      map[string]any `json:"usage,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func (r Record) Validate() error {
	if strings.TrimSpace(r.Provider) == "" {
		return errors.New("provider usage record requires provider")
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return errors.New("provider usage record requires request id")
	}
	if r.OccurredAt.IsZero() {
		return errors.New("provider usage record requires occurred at")
	}
	return nil
}

type Adapter interface {
	Provider() string
	Fetch(context.Context, Query) ([]Record, error)
}

// HTTPJSONAdapter is a provider-neutral transport adapter. The provider
// implementation supplies the endpoint, server-side headers, query mapping,
// and response decoder explicitly.
type HTTPJSONAdapter struct {
	ProviderName string
	Endpoint     string
	Headers      map[string]string
	Client       *http.Client
	Decode       func([]byte) ([]Record, error)
}

func (a HTTPJSONAdapter) Provider() string { return strings.TrimSpace(a.ProviderName) }

func (a HTTPJSONAdapter) Fetch(ctx context.Context, query Query) ([]Record, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if a.Provider() == "" || strings.TrimSpace(a.Endpoint) == "" {
		return nil, errors.New("HTTP usage adapter requires provider and endpoint")
	}
	endpoint, err := url.Parse(a.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("HTTP usage adapter endpoint must be an absolute URL")
	}
	values := endpoint.Query()
	values.Set("start_time", query.StartTime.UTC().Format(time.RFC3339))
	values.Set("end_time", query.EndTime.UTC().Format(time.RFC3339))
	if strings.TrimSpace(query.InternalKey) != "" {
		values.Set("internal_key", strings.TrimSpace(query.InternalKey))
	}
	for _, requestID := range query.RequestIDs {
		if strings.TrimSpace(requestID) != "" {
			values.Add("request_id", strings.TrimSpace(requestID))
		}
	}
	endpoint.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	for key, value := range a.Headers {
		req.Header.Set(key, value)
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("provider usage API returned HTTP %d", response.StatusCode)
	}
	decode := a.Decode
	if decode == nil {
		decode = func(body []byte) ([]Record, error) {
			var records []Record
			if err := json.Unmarshal(body, &records); err != nil {
				return nil, err
			}
			return records, nil
		}
	}
	records, err := decode(body)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return nil, err
		}
		if !strings.EqualFold(record.Provider, a.Provider()) {
			return nil, fmt.Errorf("provider usage record provider %q does not match adapter %q", record.Provider, a.Provider())
		}
	}
	return records, nil
}
