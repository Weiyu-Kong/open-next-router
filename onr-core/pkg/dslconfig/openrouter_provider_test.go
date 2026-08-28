package dslconfig

import (
	"path/filepath"
	"testing"
)

func TestValidateProviderFile_OpenRouterRequestIDJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "config", "providers", "openrouter.conf")
	pf, err := ValidateProviderFile(path)
	if err != nil {
		t.Fatalf("ValidateProviderFile(%q): %v", path, err)
	}
	if pf.Observability.UpstreamRequestIDJSON == nil {
		t.Fatal("expected OpenRouter upstream request ID JSON rule")
	}
	if got := pf.Observability.UpstreamRequestIDJSON.Path; got != "$.id" {
		t.Fatalf("path=%q want $.id", got)
	}
}
