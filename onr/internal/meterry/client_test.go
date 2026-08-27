package meterry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r9s-ai/open-next-router/pkg/usageadapter"
)

type testUsageAdapter struct{}

func (testUsageAdapter) Provider() string { return "openai" }
func (testUsageAdapter) Fetch(context.Context, usageadapter.Query) ([]usageadapter.Record, error) {
	return nil, nil
}

func TestClientSendsQueuedEvent(t *testing.T) {
	received := make(chan Event, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Errorf("decode event: %v", err)
		}
		received <- e
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c, err := New(Config{
		Enabled:          true,
		BaseURL:          srv.URL,
		ProjectID:        "proj",
		APIKey:           "secret",
		ExtractorRuleSet: "ers",
		OutboxDir:        t.TempDir(),
		RetryInterval:    5 * time.Millisecond,
		RequestTimeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEvent("rid_1", "openai", "chat.completions", "m", false, 200, "upstream", nil, "api_key", "k", "", nil, "", "", "")
	if err := c.Enqueue(e); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.IdempotencyKey != e.IdempotencyKey {
			t.Fatalf("idempotency key = %q", got.IdempotencyKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Meterry ingest")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryScopeValidation(t *testing.T) {
	if (QueryScope{}).valid() {
		t.Fatal("empty query scope must be invalid")
	}
	if !(QueryScope{AccountID: " acct_1 "}).valid() {
		t.Fatal("account query scope must be valid")
	}
	if !(QueryScope{SubjectType: "api_key", SubjectID: "key_1"}).valid() {
		t.Fatal("subject query scope must be valid")
	}
	if (QueryScope{SubjectType: "api_key"}).valid() {
		t.Fatal("partial subject query scope must be invalid")
	}
}

func TestReconcileProviderUsageValidatesBeforeAdapterWhenDisabled(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	count, err := c.ReconcileProviderUsage(context.Background(), testUsageAdapter{}, usageadapter.Query{}, "api_key", "key-1", "key-1", "account-1", "")
	if err != nil || count != 0 {
		t.Fatalf("reconcile disabled=(%d,%v), want 0,nil", count, err)
	}
}

func TestDisabledQueryMethodsRejectBeforeSDKCall(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.ReadBalance(ctx, QueryScope{AccountID: "acct_1"}, "USD"); err == nil {
		t.Fatal("ReadBalance should reject disabled client")
	}
	if _, err := c.ReadLimits(ctx, QueryScope{SubjectType: "api_key", SubjectID: "key_1"}, "USD"); err == nil {
		t.Fatal("ReadLimits should reject disabled client")
	}
	if _, err := c.QueryUsage(ctx, UsageQuery{QueryScope: QueryScope{AccountID: "acct_1"}}); err == nil {
		t.Fatal("QueryUsage should reject disabled client")
	}
	if _, err := c.QueryBills(ctx, UsageQuery{QueryScope: QueryScope{AccountID: "acct_1"}}, "UTC", ""); err == nil {
		t.Fatal("QueryBills should reject disabled client")
	}
	if _, err := c.QueryEvents(ctx, QueryScope{SubjectType: "api_key", SubjectID: "key_1"}, 0, 0, 10); err == nil {
		t.Fatal("QueryEvents should reject disabled client")
	}
	if _, err := c.QueryEventDetails(ctx, QueryScope{SubjectType: "api_key", SubjectID: "key_1"}, 0, 0, 10); err == nil {
		t.Fatal("QueryEventDetails should reject disabled client")
	}
}

func TestClientCheckBalance(t *testing.T) {
	requests := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"account_id":"acct_1","currency":"USD","balance":"0.01"}`))
	}))
	defer srv.Close()
	c, err := New(Config{
		Enabled:          true,
		BaseURL:          srv.URL,
		ProjectID:        "proj",
		APIKey:           "secret",
		ExtractorRuleSet: "ers",
		OutboxDir:        t.TempDir(),
		BalanceEnabled:   true,
		BalanceCurrency:  "USD",
		BalanceTimeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	allowed, err := c.CheckBalance(t.Context(), "api_key", "client-a")
	if err != nil || !allowed {
		t.Fatalf("CheckBalance=(%v,%v), want true,nil", allowed, err)
	}
	query := <-requests
	if !strings.Contains(query, "subject_type=api_key") || !strings.Contains(query, "subject_id=client-a") || !strings.Contains(query, "currency=USD") {
		t.Fatalf("unexpected query: %s", query)
	}
}

func TestClientCheckBalanceZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":"0"}`))
	}))
	defer srv.Close()
	c, err := New(Config{Enabled: true, BaseURL: srv.URL, ProjectID: "proj", APIKey: "secret", ExtractorRuleSet: "ers", OutboxDir: t.TempDir(), BalanceEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	allowed, err := c.CheckBalance(t.Context(), "api_key", "client-a")
	if err != nil || allowed {
		t.Fatalf("CheckBalance=(%v,%v), want false,nil", allowed, err)
	}
}

func TestClientCheckBalanceCachesAndCoalescesConcurrentRequests(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	continueRequest := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
			<-continueRequest
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":"1"}`))
	}))
	defer srv.Close()
	c, err := New(Config{
		Enabled:          true,
		BaseURL:          srv.URL,
		ProjectID:        "proj",
		APIKey:           "secret",
		ExtractorRuleSet: "ers",
		OutboxDir:        t.TempDir(),
		BalanceEnabled:   true,
		BalanceCacheTTL:  time.Minute,
		BalanceTimeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	const callers = 20
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, checkErr := c.CheckBalance(t.Context(), "api_key", "client-a")
			if checkErr != nil || !allowed {
				results <- fmt.Errorf("CheckBalance=(%v,%v)", allowed, checkErr)
				return
			}
			results <- nil
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for balance request")
	}
	close(continueRequest)
	wg.Wait()
	close(results)
	for checkErr := range results {
		if checkErr != nil {
			t.Fatal(checkErr)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Meterry requests=%d, want 1", got)
	}
	if allowed, checkErr := c.CheckBalance(t.Context(), "api_key", "client-a"); checkErr != nil || !allowed {
		t.Fatalf("cached CheckBalance=(%v,%v), want true,nil", allowed, checkErr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("cached Meterry requests=%d, want 1", got)
	}
}

func TestClientApplyWebhookInvalidatesBalanceCache(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":"1"}`))
	}))
	defer srv.Close()
	c, err := New(Config{
		Enabled:          true,
		BaseURL:          srv.URL,
		ProjectID:        "proj",
		APIKey:           "secret",
		ExtractorRuleSet: "ers",
		OutboxDir:        t.TempDir(),
		BalanceEnabled:   true,
		BalanceCacheTTL:  time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if allowed, checkErr := c.CheckBalance(t.Context(), "api_key", "client-a"); checkErr != nil || !allowed {
		t.Fatalf("initial CheckBalance=(%v,%v)", allowed, checkErr)
	}
	if allowed, checkErr := c.CheckBalance(t.Context(), "api_key", "client-a"); checkErr != nil || !allowed {
		t.Fatalf("cached CheckBalance=(%v,%v)", allowed, checkErr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("initial Meterry requests=%d, want 1", got)
	}
	if err := c.ApplyWebhook("evt-1", "wallet.balance_changed", "api_key", "client-a"); err != nil {
		t.Fatal(err)
	}
	if allowed, checkErr := c.CheckBalance(t.Context(), "api_key", "client-a"); checkErr != nil || !allowed {
		t.Fatalf("post-webhook CheckBalance=(%v,%v)", allowed, checkErr)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("post-webhook Meterry requests=%d, want 2", got)
	}
}
