package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	sdk "github.com/meterry-com/meterry-go"
	"github.com/meterry-com/meterry-go/pkg/types"
	"github.com/r9s-ai/open-next-router/pkg/config"
	"github.com/r9s-ai/open-next-router/pkg/controlplane"
)

type provisioningBackend struct {
	failAt         string
	failed         bool
	accountCreated bool
	walletCreated  bool
	creditCalls    int
	creditsApplied map[string]int
}

func (b *provisioningBackend) fail(w http.ResponseWriter, operation string) bool {
	if b.failAt != operation || b.failed {
		return false
	}
	b.failed = true
	http.Error(w, "injected failure", http.StatusServiceUnavailable)
	return true
}

func (b *provisioningBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/accounts"):
		if b.fail(w, "list_accounts") {
			return
		}
		if b.accountCreated {
			_, _ = w.Write([]byte(`{"accounts":[{"id":"acct-a","name":"onr-access-key:key-a"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"accounts":[]}`))
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/accounts"):
		if b.fail(w, "create_account") {
			return
		}
		b.accountCreated = true
		_, _ = w.Write([]byte(`{"id":"acct-a","name":"onr-access-key:key-a"}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/account-subjects"):
		if b.fail(w, "bind_subject") {
			return
		}
		_, _ = w.Write([]byte(`{"id":"binding-a","account_id":"acct-a","subject_type":"api_key","subject_id":"subject-a"}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/wallets"):
		if b.fail(w, "list_wallets") {
			return
		}
		if b.walletCreated {
			_, _ = w.Write([]byte(`{"wallets":[{"id":"wallet-a","account_id":"acct-a","currency":"USD"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"wallets":[]}`))
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wallets"):
		if b.fail(w, "create_wallet") {
			return
		}
		b.walletCreated = true
		_, _ = w.Write([]byte(`{"id":"wallet-a","account_id":"acct-a","currency":"USD"}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wallets/credit"):
		if b.fail(w, "credit_before") {
			return
		}
		var request types.CreditWalletRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.creditCalls++
		if b.creditsApplied == nil {
			b.creditsApplied = map[string]int{}
		}
		if b.creditsApplied[request.IdempotencyKey] == 0 {
			b.creditsApplied[request.IdempotencyKey] = 1
		}
		if b.fail(w, "credit_after") {
			return
		}
		_, _ = w.Write([]byte(`{"wallet":{"id":"wallet-a","account_id":"acct-a","currency":"USD"},"ledger_entry":{"currency":"USD","amount":"100","idempotency_key":"onr:initial-credit:key-a"}}`))
	default:
		http.NotFound(w, r)
	}
}

func newProvisioningService(t *testing.T, backend http.Handler) (*Service, *controlplane.Client) {
	t.Helper()
	meterryServer := httptest.NewServer(backend)
	t.Cleanup(meterryServer.Close)
	redisServer := miniredis.RunT(t)
	cp, err := controlplane.New(controlplane.Config{Addr: "redis://" + redisServer.Addr(), KeyPrefix: "test", AccessKeyHashSecret: "secret", OperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cp.Close() })
	meterryClient, err := sdk.NewClient(sdk.Config{BaseURL: meterryServer.URL, APIKey: "server-secret", HTTPClient: meterryServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Meterry.Enabled = true
	cfg.Meterry.ProjectID = "project-a"
	cfg.Meterry.InitialCredit = "100"
	cfg.Meterry.BalanceEnforcement.Currency = "USD"
	return &Service{cfg: cfg, cp: cp, meterry: meterryClient}, cp
}

func TestAccessKeyProvisioningRecoversFromPartialFailures(t *testing.T) {
	for _, failure := range []string{"list_accounts", "create_account", "bind_subject", "list_wallets", "create_wallet", "credit_before", "credit_after"} {
		t.Run(failure, func(t *testing.T) {
			backend := &provisioningBackend{failAt: failure}
			service, cp := newProvisioningService(t, backend)
			secret, err := service.CreateAccessKey(t.Context(), CreateAccessKeyInput{Name: "key-a", SubjectType: "api_key", SubjectID: "subject-a", AccountID: "account-a"})
			if err == nil || secret == "" {
				t.Fatalf("initial create=(secret=%q, err=%v), want one-time secret and error", secret, err)
			}
			record, getErr := cp.GetAccessKeyRecord(t.Context(), "key-a")
			if getErr != nil || record == nil || record.Status != "pending" || record.Provisioning != "failed" || record.ProvisioningError == "" {
				t.Fatalf("failed record=(%+v,%v)", record, getErr)
			}
			if authenticated, lookupErr := cp.LookupAccessKey(t.Context(), secret); lookupErr != nil || authenticated != nil {
				t.Fatalf("pending key authentication=(%+v,%v), want nil,nil", authenticated, lookupErr)
			}
			if err := service.ProvisionAccessKey(t.Context(), "key-a"); err != nil {
				t.Fatalf("retry provisioning: %v", err)
			}
			record, getErr = cp.GetAccessKeyRecord(t.Context(), "key-a")
			if getErr != nil || record == nil || record.Status != "active" || record.Provisioning != "ready" || record.ProvisioningError != "" || record.MeterryAccountID != "acct-a" {
				t.Fatalf("ready record=(%+v,%v)", record, getErr)
			}
			if authenticated, lookupErr := cp.LookupAccessKey(t.Context(), secret); lookupErr != nil || authenticated == nil {
				t.Fatalf("active key authentication=(%+v,%v)", authenticated, lookupErr)
			}
			if len(backend.creditsApplied) != 1 || backend.creditsApplied["onr:initial-credit:key-a"] != 1 {
				t.Fatalf("credits applied=%v", backend.creditsApplied)
			}
			if failure == "credit_after" && backend.creditCalls != 2 {
				t.Fatalf("credit calls=%d, want retry with same idempotency key", backend.creditCalls)
			}
		})
	}
}

func TestListAccessKeyMeterSummaries(t *testing.T) {
	var billTimezones []string
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
			var request types.UsageBillQueryRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode bill request: %v", err)
			}
			billTimezones = append(billTimezones, request.Timezone)
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
	if _, err := service.QueryAccessKeyBills(t.Context(), controlplane.AccessKeyRecord{SubjectType: "api_key", SubjectID: "subject-a", AccountID: "acct-meterry"}, time.Now().Add(-time.Hour).Unix(), time.Now().Unix(), "Asia/Shanghai", 100); err != nil {
		t.Fatal(err)
	}
	if len(billTimezones) != 2 || billTimezones[0] != "UTC" || billTimezones[1] != "Asia/Shanghai" {
		t.Fatalf("bill timezones=%v", billTimezones)
	}
	if service.BillingCurrency() != "USD" {
		t.Fatalf("billing currency=%q", service.BillingCurrency())
	}
}
