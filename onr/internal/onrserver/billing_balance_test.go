package onrserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"

	"github.com/r9s-ai/open-next-router/onr/internal/auth"
	"github.com/r9s-ai/open-next-router/pkg/billing"
	"github.com/r9s-ai/open-next-router/pkg/config"
	"github.com/r9s-ai/open-next-router/pkg/controlplane"
)

func testBillingLedger(t *testing.T, initialCredit string) *billing.Ledger {
	t.Helper()
	server := miniredis.RunT(t)
	cp, err := controlplane.New(controlplane.Config{
		Addr:                "redis://" + server.Addr(),
		KeyPrefix:           "billing-balance-test",
		AccessKeyHashSecret: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cp.Close() })
	ledger, err := billing.New(cp, billing.Config{Enabled: true, Currency: "CNY", InitialCredit: initialCredit})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.ProvisionAccount(context.Background(), "account-a"); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func runBalanceCheck(t *testing.T, ledger *billing.Ledger) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	auth.MiddlewareWithResolver("", func(context.Context, string) (auth.AuthPrincipal, bool, error) {
		return auth.AuthPrincipal{AccessKeyID: "key-a", AccountID: "account-a"}, true, nil
	})(c)
	if !c.IsAborted() {
		enforceBillingBalance(&config.Config{Billing: config.LocalBillingConfig{Enabled: true}}, ledger, c, "X-Onr-Request-Id")
	}
	return recorder
}

func TestEnforceBillingBalanceRejectsExhaustedAccount(t *testing.T) {
	recorder := runBalanceCheck(t, testBillingLedger(t, "0"))
	if recorder.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); body == "" || !containsAll(body, "insufficient_balance", "access key balance is exhausted") {
		t.Fatalf("body=%s", body)
	}
}

func TestEnforceBillingBalanceAllowsPositiveAccount(t *testing.T) {
	recorder := runBalanceCheck(t, testBillingLedger(t, "1"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
