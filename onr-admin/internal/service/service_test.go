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
