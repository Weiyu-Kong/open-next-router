package billing

import (
	"context"
	"fmt"
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

func TestLedgerReadEventsReturnsNewestWithinLimit(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	if err := l.ProvisionAccount(ctx, "account-b"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		event := UsageEvent{
			RequestID:  fmt.Sprintf("req-%d", i),
			AccountID:  "account-b",
			Model:      "glm-5.3",
			TotalTokens: int64(i),
			Amount:     "0.001",
			OccurredAt: time.Unix(int64(i), 0),
		}
		if err := l.Record(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := l.ReadEvents(ctx, "account-b", time.Unix(0, 0), time.Unix(10, 0), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].RequestID != "req-2" || events[1].RequestID != "req-3" {
		t.Fatalf("events=%+v, want newest two in ascending order", events)
	}
}
