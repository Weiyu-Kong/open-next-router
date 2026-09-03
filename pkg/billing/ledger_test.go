package billing

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/r9s-ai/open-next-router/pkg/controlplane"
)

func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	server := miniredis.RunT(t)
	cp, err := controlplane.New(controlplane.Config{Addr: "redis://" + server.Addr(), KeyPrefix: "test", AccessKeyHashSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cp.Close() })
	l, err := New(cp, Config{Enabled: true, Currency: "CNY", InitialCredit: "10"})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLedgerProvisionRecordAndIdempotency(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	if err := l.ProvisionAccount(ctx, "account-a"); err != nil {
		t.Fatal(err)
	}
	if err := l.ProvisionAccount(ctx, "account-a"); err != nil {
		t.Fatal(err)
	}
	event := UsageEvent{RequestID: "req-1", AccessKeyID: "key-a", AccountID: "account-a", Provider: "ctyun", Model: "qwen3.8-max", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, Amount: "0.001728", OccurredAt: time.Unix(100, 0)}
	if err := l.Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, event); err != controlplane.ErrLocalBillingDuplicate {
		t.Fatalf("duplicate error=%v", err)
	}
	account, err := l.ReadAccount(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	if account.Credit != "10.000000" || account.Spent != "0.001728" || account.Balance != "9.998272" {
		t.Fatalf("account=%+v", account)
	}
	events, err := l.ReadEvents(ctx, "account-a", time.Unix(0, 0), time.Unix(200, 0), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
