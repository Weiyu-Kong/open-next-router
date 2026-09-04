package modelcatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/r9s-ai/open-next-router/onr-core/pkg/dslconfig"
	"github.com/r9s-ai/open-next-router/onr-core/pkg/models"
)

func TestLoadCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(`models:
  - id: qwen3.8-max
    provider: Qwen
    pricing:
      input: "12"
      output: "36"
      cache_hit: "1.5"
      unit: "CNY / 1M tokens"
    providers:
      ctyun:
        supported: true
        model_id: provider-qwen
`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	models := catalog.Models()
	if len(models) != 1 || models[0].ID != "qwen3.8-max" {
		t.Fatalf("models=%+v", models)
	}
	if !models[0].Providers["ctyun"].Supported || models[0].Pricing.CacheHit != "1.5" {
		t.Fatalf("model=%+v", models[0])
	}
}

func TestRepositoryCatalogMatchesSelectableModels(t *testing.T) {
	want := []string{
		"claude-opus-5-fast", "claude-opus-5", "claude-sonnet-5", "claude-fable-5",
		"claude-opus-4.8-fast", "claude-opus-4.8", "claude-opus-4.7",
		"deepseek-v4-pro-0813", "deepseek-v4-flash-0731", "deepseek-v3.2",
		"gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash-lite",
		"minimax-m3", "kimi-k3", "kimi-k2.7-code", "kimi-k2.6",
		"gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.5-pro", "gpt-5.5",
		"qwen3.8-flash", "qwen3.8-27b", "qwen-image-3-pro", "qwen-image-3",
		"qwen3.8-max", "qwen3.7-flash", "qwen3.7-plus", "qwen3.6-flash",
		"qwen3.6-plus", "qwen3.6-27b", "qwen3-8b", "qwen3-coder-next",
		"glm-5.3-flash", "glm-5.3", "glm-5.2", "glm-5.1",
	}
	sort.Strings(want)
	catalog, err := Load(filepath.Join("..", "..", "config", "models.catalog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	gotCatalog := make([]string, 0, len(catalog.Models()))
	for _, model := range catalog.Models() {
		gotCatalog = append(gotCatalog, model.ID)
	}
	if !reflect.DeepEqual(gotCatalog, want) {
		t.Fatalf("catalog models do not match expected list\ngot=%v\nwant=%v", gotCatalog, want)
	}
	router, err := models.Load(filepath.Join("..", "..", "config", "models.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := router.Models(); !reflect.DeepEqual(got, want) {
		t.Fatalf("selectable models do not match expected list\ngot=%v\nwant=%v", got, want)
	}
	for _, model := range catalog.Models() {
		route, ok := router.RouteForModel(model.ID)
		if !ok {
			t.Fatalf("model %q is missing from selectable routes", model.ID)
		}
		ctyunSupported := model.Providers["ctyun"].Supported
		if ctyunSupported != reflect.DeepEqual(route.Providers, []string{"ctyun"}) {
			t.Fatalf("model %q Ctyun catalog support=%v route=%v", model.ID, ctyunSupported, route.Providers)
		}
	}
}

func TestRepositoryCatalogMatchesCtyunPrices(t *testing.T) {
	type price struct{ input, output, cache string }
	want := map[string]price{
		"deepseek-v4-flash-0731": {"1", "2", "0.2"},
		"deepseek-v3.2":          {"2", "3", ""},
		"minimax-m3":             {"0-512k: 2.1; 512k-1024k: 4.2", "0-512k: 8.4; 512k-1024k: 16.8", "0-512k: 0.42; 512k-1024k: 0.84"},
		"kimi-k3":                {"0-1024k: 20", "0-1024k: 100", "0-1024k: 2"},
		"kimi-k2.7-code":         {"6.5", "27", "1.3"},
		"kimi-k2.6":              {"6.5", "27", "1.3"},
		"qwen3.8-max":            {"12", "36", "1.5"},
		"qwen3.7-plus":           {"0-256k: 2; 256k-1024k: 6", "0-256k: 8; 256k-1024k: 24", "0-256k: 0.4; 256k-1024k: 1.2"},
		"qwen3.6-flash":          {"0-256k: 1.2; 256k-1024k: 4.8", "0-256k: 7.2; 256k-1024k: 28.8", ""},
		"qwen3.6-plus":           {"0-256k: 2; 256k-1M: 8", "0-256k: 12; 256k-1M: 48", ""},
		"qwen3.6-27b":            {"3", "18", ""},
		"qwen3-8b":               {"0.3", "0.6", ""},
		"qwen3-coder-next":       {"0-32k: 1; 32k-128k: 1.5; 128k-256k: 2.5", "0-32k: 4; 32k-128k: 6; 128k-256k: 10", ""},
		"glm-5.3":                {"8", "28", "2"},
		"glm-5.2":                {"8", "28", "2"},
		"glm-5.1":                {"0-32k: 6; 32k-10M: 8", "0-32k: 24; 32k-10M: 28", "0-32k: 1.3; 32k-10M: 2"},
	}
	catalog, err := Load(filepath.Join("..", "..", "config", "models.catalog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models() {
		got := price{model.Pricing.Input, model.Pricing.Output, model.Pricing.CacheHit}
		expected, found := want[model.ID]
		if !found {
			if got != (price{}) {
				t.Fatalf("model %q must have blank pricing, got %+v", model.ID, got)
			}
			continue
		}
		if got != expected {
			t.Fatalf("model %q pricing=%+v want %+v", model.ID, got, expected)
		}
		if model.Pricing.Billing != "TOKENS" || model.Pricing.Unit != "CNY / 1M tokens" || model.Pricing.Source != "ctyun" {
			t.Fatalf("model %q pricing metadata=%+v", model.ID, model.Pricing)
		}
	}
}

func TestRepositoryCtyunMappingsMatchProviderDSL(t *testing.T) {
	want := map[string]string{
		"deepseek-v4-pro-0813":   "20e81bd57e7a4be281e5d0ef0afc93d8",
		"deepseek-v4-flash-0731": "c60c9aec5710499dafae8e4f395698d8",
		"minimax-m3":             "62cc400ee35e43808b1688fcbc9c3e88",
		"kimi-k3":                "76ab208968e0414b927a9a6a97dfc3e1",
		"qwen3.8-max":            "3bbfba16dd6e4fed90af61eee76db87f",
		"qwen3.7-plus":           "98df9efe26894003be658e1e70ff0105",
		"qwen3.6-flash":          "c7aca8b6434e4a448e9e3ace7815b74f",
		"qwen3.6-plus":           "3fd7b6be3bea4f66adc276af23b5c52b",
		"glm-5.3":                "e8e2511658054053a7e56e950d80f0e4",
		"glm-5.2":                "fc59cc3375d54264b1b6011e46959ca4",
	}
	catalog, err := Load(filepath.Join("..", "..", "config", "models.catalog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models() {
		mapping := model.Providers["ctyun"]
		expected, supported := want[model.ID]
		if mapping.Supported != supported || mapping.ModelID != expected {
			t.Fatalf("model %q Ctyun mapping=%+v want supported=%v id=%q", model.ID, mapping, supported, expected)
		}
	}

	provider, err := dslconfig.ValidateProviderFile(filepath.Join("..", "..", "config", "providers", "ctyun.conf"))
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, match := range provider.Request.Matches {
		if match.API != "chat.completions" {
			continue
		}
		matched++
		for publicID, upstreamID := range want {
			got := strings.Trim(match.Transform.ModelMap.Map[publicID], `"`)
			if got != upstreamID {
				t.Fatalf("stream=%v model_map %q=%q want %q", match.Stream, publicID, got, upstreamID)
			}
		}
	}
	if matched != 2 {
		t.Fatalf("Ctyun chat matches=%d want 2", matched)
	}
}
