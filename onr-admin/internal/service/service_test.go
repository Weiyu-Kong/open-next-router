package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	sdk "github.com/meterry-com/meterry-go"
	"github.com/r9s-ai/open-next-router/pkg/config"
	"github.com/r9s-ai/open-next-router/pkg/controlplane"
)

func TestListAccessKeyMeterSummaries(t *testing.T) {
	meterryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/wallets/realtime/amount"):
			if r.URL.Query().Get("account_id") != "acct-meterry" {
				t.Errorf("balance account_id=%q", r.URL.Query().Get("account_id"))
			}
			_, _ = w.Write([]byte(`{"account_id":"acct-meterry","currency":"USD","balance":"90","available_balance":"90"}`))
		case strings.HasSuffix(r.URL.Path, "/usage/query"):
			_, _ = w.Write([]byte(`{"rows":[{"dimensions":{"provider":"openai","model":"gpt-test"},"measures":{"quantity":"10"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/usage/bills/query"):
			_, _ = w.Write([]byte(`{"rows":[{"dimensions":{"provider":"openai","model":"gpt-test"},"request_count":1,"amount":"1.25","metrics":{}}],"has_more":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer meterryServer.Close()

	redisServer := miniredis.RunT(t)
	cp, err := controlplane.New(controlplane.Config{Addr: "redis://" + redisServer.Addr(), KeyPrefix: "test", AccessKeyHashSecret: "secret", OperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cp.Close() }()
	if err := cp.CreateAccessKey(t.Context(), controlplane.AccessKeyRecord{Name: "key-a", SecretHash: cp.HashAccessKey("plain"), Status: "active", SubjectType: "api_key", SubjectID: "subject-a", AccountID: "acct-meterry", MeterryAccountID: "acct-meterry"}); err != nil {
		t.Fatal(err)
	}
	meterryClient, err := sdk.NewClient(sdk.Config{BaseURL: meterryServer.URL, APIKey: "server-secret", HTTPClient: meterryServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Meterry.ProjectID = "project-a"
	cfg.Meterry.BalanceEnforcement.Currency = "USD"
	service := &Service{cfg: cfg, cp: cp, meterry: meterryClient}
	if _, err := cp.EnqueueBillingEventForAccessKey(t.Context(), []byte(`{"idempotency_key":"onr:req-a"}`), "key-a"); err != nil {
		t.Fatal(err)
	}
	if pending, err := service.BillingPendingForAccessKey(t.Context(), "key-a"); err != nil || pending != 1 {
		t.Fatalf("billing pending=(%d,%v)", pending, err)
	}

	rows, err := service.ListAccessKeyMeterSummaries(context.Background(), time.Now().Add(-24*time.Hour).Unix(), time.Now().Unix())
	if err != nil || len(rows) != 1 {
		t.Fatalf("summaries=(%+v,%v)", rows, err)
	}
	if rows[0].Balance == nil || rows[0].Balance.AvailableBalance.String() != "90" || len(rows[0].Usage) != 1 || len(rows[0].Bills) != 1 || rows[0].Error != "" {
		t.Fatalf("unexpected summary: %+v", rows[0])
	}
}
