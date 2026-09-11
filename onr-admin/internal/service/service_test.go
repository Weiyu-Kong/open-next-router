package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/r9s-ai/open-next-router/pkg/config"
	"github.com/r9s-ai/open-next-router/pkg/controlplane"
)

func TestAccessKeyProvidersDefaultToGlobalPriority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte("provider_priority: [taotoken, ctyun]\nmodels: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Models.File = path
	s := &Service{cfg: cfg}
	if got := s.accessKeyProviders(""); !reflect.DeepEqual(got, []string{"taotoken", "ctyun"}) {
		t.Fatalf("providers=%v", got)
	}
	if got := s.accessKeyProviders("ctyun"); !reflect.DeepEqual(got, []string{"ctyun"}) {
		t.Fatalf("explicit providers=%v", got)
	}
}

func TestBatchCreateAccessKeysRejectsOversizedCount(t *testing.T) {
	results := (&Service{}).BatchCreateAccessKeys(context.Background(), BatchCreateAccessKeyInput{Count: 0})
	if len(results) != 1 || results[0].Error == "" {
		t.Fatalf("unexpected result: %#v", results)
	}
}

func TestCreateAccessKeyAssignsProviderKeyWhenBindingsAreEmpty(t *testing.T) {
	redisServer := miniredis.RunT(t)
	cp, err := controlplane.New(controlplane.Config{
		Addr:                "redis://" + redisServer.Addr(),
		KeyPrefix:           "onr-admin-service-test",
		AccessKeyHashSecret: "test-hash-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cp.Close() })

	s := &Service{
		cfg:           &config.Config{},
		cp:            cp,
		keyPoolNext:   map[string]int{},
		keyPoolNames:  map[string][]string{"ctyun": {"primary"}},
		keyPoolLoaded: true,
	}
	secret, err := s.CreateAccessKey(context.Background(), CreateAccessKeyInput{
		Name:                "empty-bindings",
		SubjectType:         "api_key",
		SubjectID:           "empty-bindings",
		AllowedProviders:    "ctyun",
		ProviderKeyBindings: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" {
		t.Fatal("secret is empty")
	}
	record, err := cp.GetAccessKeyRecord(context.Background(), "empty-bindings")
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("access key record is missing")
	}
	if got := record.ProviderKeyBindings["ctyun"]; got != "primary" {
		t.Fatalf("ctyun binding=%q want primary", got)
	}
}

func TestEventTotalTokensDoesNotDoubleCountCachedTokens(t *testing.T) {
	event := controlplane.LocalBillingEvent{
		InputTokens:  100,
		OutputTokens: 20,
		CachedTokens: 80,
		TotalTokens:  120,
	}
	if got := eventTotalTokens(event); got != 120 {
		t.Fatalf("total tokens=%d want 120", got)
	}
	event.TotalTokens = 0
	if got := eventTotalTokens(event); got != 120 {
		t.Fatalf("fallback total tokens=%d want 120", got)
	}
}

func TestFilterAccessKeyEventsFiltersBeforeApplyingLimit(t *testing.T) {
	events := []controlplane.LocalBillingEvent{
		{RequestID: "a-1", AccessKeyID: "key-a"},
		{RequestID: "b-1", AccessKeyID: "key-b"},
		{RequestID: "a-2", AccessKeyID: "key-a"},
		{RequestID: "b-2", AccessKeyID: "key-b"},
		{RequestID: "a-3", AccessKeyID: "key-a"},
	}
	got := filterAccessKeyEvents(events, "key-a", 2)
	if len(got) != 2 || got[0].RequestID != "a-2" || got[1].RequestID != "a-3" {
		t.Fatalf("events=%+v want latest two events for key-a", got)
	}
	all := filterAccessKeyEvents(events, "key-a", 0)
	if len(all) != 3 {
		t.Fatalf("unlimited events=%d want 3", len(all))
	}
}
