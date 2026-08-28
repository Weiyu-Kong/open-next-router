package usageadapter

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestOpenRouterGenerationAdapterDecodesGenerationDetails(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer server-secret" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		id := r.URL.Query().Get("id")
		requested = append(requested, id)
		w.Header().Set("Content-Type", "application/json")
		if id == "gen-outside" {
			_, _ = w.Write([]byte(`{"data":{"id":"gen-outside","created_at":"2026-08-25T12:00:00Z","model":"openai/gpt-4o-mini","tokens_prompt":1,"tokens_completion":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"gen-1","created_at":"2026-08-28T11:30:00.123Z","model":"openai/gpt-4o-mini","provider_name":"OpenAI","streamed":true,"cancelled":false,"tokens_prompt":12,"tokens_completion":8,"native_tokens_prompt":13,"native_tokens_completion":9,"native_tokens_reasoning":2,"total_cost":0.00125}}`))
	}))
	defer server.Close()

	adapter := OpenRouterGenerationAdapter{Endpoint: server.URL + "/api/v1/generation", APIKey: "server-secret", Client: server.Client()}
	records, err := adapter.Fetch(t.Context(), Query{
		StartTime:  time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
		RequestIDs: []string{"gen-1", "gen-outside", "gen-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%+v", records)
	}
	record := records[0]
	if record.Provider != "openrouter" || record.RequestID != "gen-1" || record.Model != "openai/gpt-4o-mini" || !record.Stream || record.Status != http.StatusOK {
		t.Fatalf("record=%+v", record)
	}
	if record.Usage["input_tokens"] != "12" || record.Usage["output_tokens"] != "8" || record.Usage["reasoning_tokens"] != "2" || record.Metadata["provider_cost"] != "0.00125" {
		t.Fatalf("usage=%v metadata=%v", record.Usage, record.Metadata)
	}
	sort.Strings(requested)
	if !reflect.DeepEqual(requested, []string{"gen-1", "gen-outside"}) {
		t.Fatalf("requested=%v", requested)
	}
}

func TestOpenRouterGenerationAdapterRejectsMissingOrMismatchedIDs(t *testing.T) {
	adapter := OpenRouterGenerationAdapter{APIKey: "secret"}
	query := Query{StartTime: time.Now().Add(-time.Hour), EndTime: time.Now()}
	if _, err := adapter.Fetch(t.Context(), query); err == nil {
		t.Fatal("expected request IDs to be required")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"different-id","created_at":"2026-08-28T11:30:00Z"}}`))
	}))
	defer server.Close()
	adapter.Endpoint = server.URL
	adapter.Client = server.Client()
	query.StartTime = time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	query.EndTime = query.StartTime.Add(24 * time.Hour)
	query.RequestIDs = []string{"gen-1"}
	if _, err := adapter.Fetch(t.Context(), query); err == nil {
		t.Fatal("expected response ID mismatch")
	}
}
