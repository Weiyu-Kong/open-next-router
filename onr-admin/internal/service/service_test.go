package service

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/r9s-ai/open-next-router/pkg/config"
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
