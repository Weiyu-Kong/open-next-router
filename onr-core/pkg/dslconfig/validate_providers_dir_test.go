package dslconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateProvidersDir_ConfigProviders(t *testing.T) {
	candidates := []string{
		filepath.Join("..", "..", "..", "config", "providers"),
		filepath.Join("..", "..", "config", "providers"),
	}
	for _, dir := range candidates {
		res, err := ValidateProvidersDir(dir)
		if err != nil {
			continue
		}
		if !containsLoadedProvider(res.LoadedProviders, "ctyun") {
			t.Fatalf("expected ctyun provider in %q, got %#v", dir, res.LoadedProviders)
		}
		if !containsLoadedProvider(res.LoadedProviders, "taotoken") {
			t.Fatalf("expected taotoken provider in %q, got %#v", dir, res.LoadedProviders)
		}
		if len(res.Warnings) != 0 {
			t.Fatalf("expected no warnings for %q, got %#v", dir, res.Warnings)
		}
		return
	}
	t.Fatalf("validate providers dir failed for all candidates: %v", candidates)
}

func TestValidateCtyunProviderSanitizesResponseModel(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "providers", "ctyun.conf")
	pf, err := ValidateProviderFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range pf.Response.Matches {
		if match.API != "chat.completions" {
			continue
		}
		found := false
		for _, op := range match.Response.JSONOps {
			if op.Op == "json_replace" && op.Path == "$.model" && op.ValueExpr == "$request.model" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ctyun %s response must replace upstream model with public request model", match.API)
		}
	}
}

func TestCtyunExpectedModelMappingsAreExplicit(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "providers", "ctyun.conf")
	pf, err := ValidateProviderFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deepseek-v4-pro", "deepseek-v4-flash", "minimax-m3", "kimi-k3", "qwen3.8-max", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-flash", "qwen3.6-plus", "glm-5.3", "glm-5.2"}
	for _, match := range pf.Request.Matches {
		if match.API != "chat.completions" {
			continue
		}
		for _, model := range want {
			if _, ok := match.Transform.ModelMap.Map[model]; !ok {
				t.Fatalf("ctyun %s stream=%v missing model_map for %q", match.API, match.Stream, model)
			}
		}
	}
}

func containsLoadedProvider(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestValidateProvidersDir_RejectsLegacyUsageDirectiveAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.conf")
	// #nosec G306 -- test data file.
	if err := os.WriteFile(path, []byte(`
syntax "next-router/0.1";

provider "demo" {
  defaults {
    upstream_config {
      base_url = "https://api.example.com";
    }
    metrics {
      usage_extract custom;
      input_tokens = $.usage.input_tokens;
      output_tokens = $.usage.output_tokens;
    }
  }
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := ValidateProvidersDir(dir); err == nil {
		t.Fatalf("expected legacy usage alias validation error")
	}
}

func TestValidateProvidersDir_RejectsLegacyBalanceDirectiveAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.conf")
	// #nosec G306 -- test data file.
	if err := os.WriteFile(path, []byte(`
syntax "next-router/0.1";

provider "demo" {
  defaults {
    upstream_config {
      base_url = "https://api.example.com";
    }
    balance {
      balance_mode custom;
      path "/v1/credits";
      balance_expr = $.data.total;
      used = $.data.used;
    }
  }
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := ValidateProvidersDir(dir); err == nil {
		t.Fatalf("expected legacy balance alias validation error")
	}
}

func TestValidateProvidersDir_NoDeprecatedDirectiveWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.conf")
	// #nosec G306 -- test data file.
	if err := os.WriteFile(path, []byte(`
syntax "next-router/0.1";

provider "demo" {
  defaults {
    upstream_config {
      base_url = "https://api.example.com";
    }
    metrics {
      usage_extract custom;
      input_tokens_expr = $.usage.input_tokens;
      output_tokens_expr = $.usage.output_tokens;
    }
    balance {
      balance_mode custom;
      path "/v1/credits";
      balance_expr = $.data.total;
      used_expr = $.data.used;
    }
  }
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res, err := ValidateProvidersDir(dir)
	if err != nil {
		t.Fatalf("ValidateProvidersDir: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %d: %#v", len(res.Warnings), res.Warnings)
	}
}

func TestValidateProvidersDir_GlobalUsageModeSharedAcrossProviders(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "providers")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "onr.conf"), []byte(`
syntax "next-router/0.1";

usage_mode "shared_tokens" {
  usage_extract custom;
  usage_fact input token path="$.usage.input_tokens";
  usage_fact output token path="$.usage.output_tokens";
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile onr.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openai.conf"), []byte(`
syntax "next-router/0.1";

provider "openai" {
  defaults {
    upstream_config { base_url = "https://api.openai.com"; }
    metrics { usage_extract shared_tokens; }
  }
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile openai: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anthropic.conf"), []byte(`
syntax "next-router/0.1";

provider "anthropic" {
  defaults {
    upstream_config { base_url = "https://api.anthropic.com"; }
    metrics { usage_extract shared_tokens; }
  }
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile anthropic: %v", err)
	}

	res, err := ValidateProvidersDir(dir)
	if err != nil {
		t.Fatalf("ValidateProvidersDir: %v", err)
	}
	if len(res.LoadedProviders) != 2 {
		t.Fatalf("expected 2 providers, got %#v", res.LoadedProviders)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", res.Warnings)
	}
}

func TestValidateProvidersDir_RejectsDuplicateGlobalUsageMode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "providers")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "onr.conf"), []byte(`
syntax "next-router/0.1";

usage_mode "shared_tokens" {
  usage_extract custom;
  usage_fact input token path="$.usage.input_tokens";
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile onr.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-b.conf"), []byte(`
syntax "next-router/0.1";

usage_mode "shared_tokens" {
  usage_extract custom;
  usage_fact output token path="$.usage.output_tokens";
}
`), 0o600); err != nil {
		t.Fatalf("WriteFile usage-b: %v", err)
	}

	if _, err := ValidateProvidersDir(dir); err == nil {
		t.Fatalf("expected duplicate usage_mode error")
	}
}
