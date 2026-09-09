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
		"qwen3.8-max", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus", "qwen3.6-flash",
		"deepseek-v4-flash", "deepseek-v4-pro", "kimi-k3", "minimax-m3", "glm-5.3", "glm-5.2",
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
		wantProviders := []string{"ctyun"}
		if model.ID == "deepseek-v4-flash" || model.ID == "glm-5.3" {
			wantProviders = []string{"taotoken", "ctyun"}
		}
		if !reflect.DeepEqual(route.Providers, wantProviders) {
			t.Fatalf("model %q providers=%v want=%v", model.ID, route.Providers, wantProviders)
		}
	}
}

func TestRepositoryCatalogMatchesPublicPrices(t *testing.T) {
	type price struct{ input, output, cache string }
	want := map[string]price{
		"qwen3.8-max":       {"12", "36", "1.5"},
		"qwen3.7-max":       {"12", "36", "2.4"},
		"qwen3.7-plus":      {"2", "8", "0.4"},
		"qwen3.6-plus":      {"2", "12", ""},
		"qwen3.6-flash":     {"1.2", "7.2", ""},
		"deepseek-v4-flash": {"3", "9", "0.1"},
		"deepseek-v4-pro":   {"9", "27", "0.3"},
		"kimi-k3":           {"20", "100", "2"},
		"minimax-m3":        {"2.1", "8.4", "0.42"},
		"glm-5.3":           {"8", "28", "2"},
		"glm-5.2":           {"8", "28", "2"},
	}
	catalog, err := Load(filepath.Join("..", "..", "config", "models.catalog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models() {
		got := price{model.Pricing.Input, model.Pricing.Output, model.Pricing.CacheHit}
		expected, found := want[model.ID]
		if !found {
			t.Fatalf("model %q is missing from public prices", model.ID)
		}
		if got != expected {
			t.Fatalf("model %q pricing=%+v want %+v", model.ID, got, expected)
		}
		if model.Pricing.Billing != "TOKENS" || model.Pricing.Unit != "CNY / 1M tokens" || model.Pricing.Source != "public" {
			t.Fatalf("model %q pricing metadata=%+v", model.ID, model.Pricing)
		}
	}
}

func TestRepositoryCtyunMappingsMatchProviderDSL(t *testing.T) {
	want := map[string]string{
		"qwen3.8-max":       "3bbfba16dd6e4fed90af61eee76db87f",
		"qwen3.7-max":       "547f9804945c492a9d536bcbff15fb1a",
		"qwen3.7-plus":      "98df9efe26894003be658e1e70ff0105",
		"qwen3.6-plus":      "3fd7b6be3bea4f66adc276af23b5c52b",
		"qwen3.6-flash":     "c7aca8b6434e4a448e9e3ace7815b74f",
		"deepseek-v4-flash": "c60c9aec5710499dafae8e4f395698d8",
		"deepseek-v4-pro":   "20e81bd57e7a4be281e5d0ef0afc93d8",
		"kimi-k3":           "76ab208968e0414b927a9a6a97dfc3e1",
		"minimax-m3":        "62cc400ee35e43808b1688fcbc9c3e88",
		"glm-5.3":           "e8e2511658054053a7e56e950d80f0e4",
		"glm-5.2":           "fc59cc3375d54264b1b6011e46959ca4",
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

func TestRepositoryTaoTokenMappingsMatchProviderDSL(t *testing.T) {
	want := map[string]string{
		"qwen3.8-max": "None", "qwen3.7-max": "None", "qwen3.7-plus": "None",
		"qwen3.6-plus": "None", "qwen3.6-flash": "None",
		"deepseek-v4-flash": "deepseek-v4-flash", "deepseek-v4-pro": "None",
		"kimi-k3": "None", "minimax-m3": "None", "glm-5.3": "glm-5.3-flash", "glm-5.2": "None",
	}
	catalog, err := Load(filepath.Join("..", "..", "config", "models.catalog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models() {
		mapping := model.Providers["taotoken"]
		expected := want[model.ID]
		supported := expected != "None"
		if mapping.Supported != supported || mapping.ModelID != expected {
			t.Fatalf("model %q TaoToken mapping=%+v want supported=%v id=%q", model.ID, mapping, supported, expected)
		}
	}

	provider, err := dslconfig.ValidateProviderFile(filepath.Join("..", "..", "config", "providers", "taotoken.conf"))
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, match := range provider.Request.Matches {
		if match.API != "chat.completions" && match.API != "responses" {
			continue
		}
		matched++
		for publicID, upstreamID := range want {
			got := strings.Trim(match.Transform.ModelMap.Map[publicID], `"`)
			if got != upstreamID {
				t.Fatalf("api=%s stream=%v model_map %q=%q want %q", match.API, match.Stream, publicID, got, upstreamID)
			}
		}
	}
	if matched != 4 {
		t.Fatalf("TaoToken matches=%d want 4", matched)
	}
}
