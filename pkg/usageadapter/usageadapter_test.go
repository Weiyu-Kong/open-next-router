package usageadapter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQueryValidate(t *testing.T) {
	now := time.Now()
	if err := (Query{StartTime: now, EndTime: now.Add(time.Hour)}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Query{StartTime: now, EndTime: now.Add(-time.Hour)}).Validate(); err == nil {
		t.Fatal("expected invalid range")
	}
	if err := (Query{StartTime: now, EndTime: now.Add(32 * 24 * time.Hour)}).Validate(); err == nil {
		t.Fatal("expected range limit")
	}
}

func TestRecordValidate(t *testing.T) {
	record := Record{Provider: "openai", RequestID: "req-1", OccurredAt: time.Now()}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Record{{RequestID: "req-1", OccurredAt: time.Now()}, {Provider: "openai", OccurredAt: time.Now()}, {Provider: "openai", RequestID: "req-1"}} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("expected invalid record: %+v", invalid)
		}
	}
}

func TestHTTPJSONAdapterBuildsExplicitRequest(t *testing.T) {
	var got *http.Request
	adapter := HTTPJSONAdapter{ProviderName: "openai", Endpoint: "https://usage.example.test/v1/usage?tenant=t1", Headers: map[string]string{"Authorization": "Bearer server-secret"}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[{"provider":"openai","request_id":"req-1","occurred_at":"2026-08-27T00:00:00Z"}]`)), Header: make(http.Header)}, nil
	})}}
	query := Query{StartTime: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), InternalKey: "key-internal", RequestIDs: []string{"req-1"}}
	records, err := adapter.Fetch(context.Background(), query)
	if err != nil || len(records) != 1 {
		t.Fatalf("Fetch=(%+v,%v)", records, err)
	}
	if got == nil || got.Header.Get("Authorization") != "Bearer server-secret" || got.URL.Query().Get("internal_key") != "key-internal" || got.URL.Query().Get("request_id") != "req-1" {
		t.Fatalf("unexpected request: %+v", got)
	}
}
