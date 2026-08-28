package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"
)

func testReconciliationCandidate(now time.Time) ReconciliationCandidate {
	return ReconciliationCandidate{
		Provider:          " OpenRouter ",
		InternalKeyID:     "primary",
		ProviderRequestID: "generation-1",
		ONRRequestID:      "onr-request-1",
		AccessKeyID:       "customer-key-1",
		AccountID:         "account-1",
		SubjectType:       "api_key",
		SubjectID:         "subject-1",
		RoutePolicyID:     "standard",
		API:               "chat.completions",
		Model:             "model-1",
		OccurredAt:        now.Add(-time.Second),
	}
}

func TestReconciliationCandidateLeaseRetryAndAck(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id, created, err := c.EnqueueReconciliationCandidate(ctx, testReconciliationCandidate(now))
	if err != nil || !created || len(id) != 64 {
		t.Fatalf("enqueue=(%q,%v,%v)", id, created, err)
	}
	duplicate := testReconciliationCandidate(now)
	duplicate.AccountID = "must-not-overwrite"
	duplicateID, duplicateCreated, err := c.EnqueueReconciliationCandidate(ctx, duplicate)
	if err != nil || duplicateCreated || duplicateID != id {
		t.Fatalf("duplicate enqueue=(%q,%v,%v)", duplicateID, duplicateCreated, err)
	}

	claimed, err := c.ClaimReconciliationCandidates(ctx, "worker-a", now.Add(time.Second), time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim=(%+v,%v)", claimed, err)
	}
	if claimed[0].Provider != "openrouter" || claimed[0].LeaseOwner != "worker-a" || claimed[0].AccountID != "account-1" {
		t.Fatalf("claimed candidate=%+v", claimed[0])
	}
	if claimed[0].LeaseToken == "" {
		t.Fatal("claim did not issue a lease token")
	}
	firstLeaseToken := claimed[0].LeaseToken
	other, err := c.ClaimReconciliationCandidates(ctx, "worker-b", now.Add(2*time.Second), time.Minute, 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("concurrent claim=(%+v,%v), want empty", other, err)
	}
	if ok, err := c.RetryReconciliationCandidate(ctx, id, "wrong-token", "not_found", now.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("non-owner retry=(%v,%v)", ok, err)
	}
	if ok, err := c.RetryReconciliationCandidate(ctx, id, claimed[0].LeaseToken, "not_ready", now.Add(2*time.Minute)); err != nil || !ok {
		t.Fatalf("owner retry=(%v,%v)", ok, err)
	}
	stored, err := c.GetReconciliationCandidate(ctx, id)
	if err != nil || stored == nil || stored.Attempts != 1 || stored.LastErrorCode != "not_ready" || stored.LeaseOwner != "" {
		t.Fatalf("stored after retry=(%+v,%v)", stored, err)
	}
	if pending, err := c.ReconciliationPending(ctx); err != nil || pending != 1 {
		t.Fatalf("pending=(%d,%v)", pending, err)
	}
	claimed, err = c.ClaimReconciliationCandidates(ctx, "worker-b", now.Add(3*time.Minute), time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("retry claim=(%+v,%v)", claimed, err)
	}
	if claimed[0].LeaseToken == firstLeaseToken {
		t.Fatal("reclaimed candidate reused its previous lease token")
	}
	if ok, err := c.AckReconciliationCandidate(ctx, id, firstLeaseToken); err != nil || ok {
		t.Fatalf("non-owner ack=(%v,%v)", ok, err)
	}
	if ok, err := c.AckReconciliationCandidate(ctx, id, claimed[0].LeaseToken); err != nil || !ok {
		t.Fatalf("owner ack=(%v,%v)", ok, err)
	}
	if stored, err := c.GetReconciliationCandidate(ctx, id); err != nil || stored != nil {
		t.Fatalf("candidate after ack=(%+v,%v)", stored, err)
	}
	if pending, err := c.ReconciliationPending(ctx); err != nil || pending != 0 {
		t.Fatalf("pending after ack=(%d,%v)", pending, err)
	}
}

func TestReconciliationCandidateExpiredLeaseCanBeReclaimed(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, _, err := c.EnqueueReconciliationCandidate(ctx, testReconciliationCandidate(now)); err != nil {
		t.Fatal(err)
	}
	first, err := c.ClaimReconciliationCandidates(ctx, "worker-a", now.Add(time.Second), time.Minute, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=(%+v,%v)", first, err)
	}
	second, err := c.ClaimReconciliationCandidates(ctx, "worker-b", now.Add(2*time.Minute), time.Minute, 1)
	if err != nil || len(second) != 1 || second[0].LeaseOwner != "worker-b" {
		t.Fatalf("expired lease claim=(%+v,%v)", second, err)
	}
}

func TestReconciliationCandidateRejectsUnsafeFailureDetails(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id, _, err := c.EnqueueReconciliationCandidate(ctx, testReconciliationCandidate(now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimReconciliationCandidates(ctx, "worker-a", now.Add(time.Second), time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"HTTP 500: secret=value", strings.Repeat("x", 65)} {
		if ok, err := c.RetryReconciliationCandidate(ctx, id, "wrong-token", value, now.Add(time.Minute)); err == nil || ok {
			t.Fatalf("unsafe error code %q accepted: ok=%v err=%v", value, ok, err)
		}
	}
}

func TestReconciliationCandidateRejectsForgedID(t *testing.T) {
	c := newTestClient(t)
	candidate := testReconciliationCandidate(time.Now().UTC())
	candidate.ID = strings.Repeat("0", 64)
	if _, _, err := c.EnqueueReconciliationCandidate(context.Background(), candidate); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("forged candidate ID error=%v", err)
	}
}
