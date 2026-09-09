package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestResolverComputeWithOverrides(t *testing.T) {
	dir := t.TempDir()
	pricePath := filepath.Join(dir, "price.yaml")
	overridesPath := filepath.Join(dir, "price_overrides.yaml")
	priceYAML := `
version: v1
unit: usd_per_1m_tokens
entries:
  - provider: openai
    model: gpt-4o-mini
    cost:
      input: 0.15
      output: 0.60
      cache_read: 0.08
`
	overridesYAML := `
version: v1
providers:
  openai:
    multiplier: 2
    models:
      gpt-4o-mini:
        cost:
          output: 0.7
channels:
  "openai/key1":
    multiplier: 1.5
    models:
      gpt-4o-mini:
        cost:
          input: 0.2
`
	if err := os.WriteFile(pricePath, []byte(priceYAML), 0o600); err != nil {
		t.Fatalf("write price: %v", err)
	}
	if err := os.WriteFile(overridesPath, []byte(overridesYAML), 0o600); err != nil {
		t.Fatalf("write overrides: %v", err)
	}

	r, err := LoadResolver(pricePath, overridesPath)
	if err != nil {
		t.Fatalf("LoadResolver: %v", err)
	}
	if r == nil {
		t.Fatalf("resolver is nil")
	}
	c, ok := r.Compute("openai", "key1", "gpt-4o-mini", map[string]any{
		"input_tokens":       1000,
		"output_tokens":      500,
		"cache_read_tokens":  100,
		"cache_write_tokens": 50,
	})
	if !ok || c == nil {
		t.Fatalf("Compute failed")
	}
	// input override 0.2, output override 0.7, then multiplier 2 * 1.5 = 3
	if math.Abs(c.InputRate-0.6) > 1e-9 {
		t.Fatalf("input rate=%v want=0.6", c.InputRate)
	}
	if math.Abs(c.OutputRate-2.1) > 1e-9 {
		t.Fatalf("output rate=%v want=2.1", c.OutputRate)
	}
	if c.BillableInputTokens != 850 {
		t.Fatalf("billable_input=%d want=850", c.BillableInputTokens)
	}
	if c.TotalCost <= 0 {
		t.Fatalf("total cost=%v want > 0", c.TotalCost)
	}
}

func TestResolverComputeCNYRateUnit(t *testing.T) {
	dir := t.TempDir()
	pricePath := filepath.Join(dir, "price.yaml")
	priceYAML := `
version: v1
unit: cny_per_1m_tokens
entries:
  - provider: ctyun
    model: qwen3.8-max
    cost:
      input: 12
      cache_read: 1.5
      output: 36
`
	if err := os.WriteFile(pricePath, []byte(priceYAML), 0o600); err != nil {
		t.Fatalf("write price: %v", err)
	}
	r, err := LoadResolver(pricePath, "")
	if err != nil || r == nil {
		t.Fatalf("LoadResolver: resolver=%v err=%v", r, err)
	}
	c, ok := r.Compute("ctyun", "primary", "qwen3.8-max", map[string]any{
		"input_tokens":      1000000,
		"cache_read_tokens": 200000,
		"output_tokens":     500000,
	})
	if !ok || c == nil {
		t.Fatalf("Compute failed")
	}
	if c.Unit != "cny" || c.RateUnit != "cny_per_1m_tokens" {
		t.Fatalf("units=%q/%q want cny/cny_per_1m_tokens", c.Unit, c.RateUnit)
	}
	if math.Abs(c.TotalCost-27.9) > 1e-9 {
		t.Fatalf("total cost=%v want=27.9", c.TotalCost)
	}
}

func TestResolverComputeWildcardPublicPrice(t *testing.T) {
	dir := t.TempDir()
	pricePath := filepath.Join(dir, "price.yaml")
	priceYAML := `
version: v1
unit: cny_per_1m_tokens
entries:
  - provider: "*"
    model: glm-5.3
    cost:
      input: 8
      cache_read: 2
      output: 28
  - provider: ctyun
    model: glm-5.3
    cost:
      input: 9
      output: 30
`
	if err := os.WriteFile(pricePath, []byte(priceYAML), 0o600); err != nil {
		t.Fatalf("write price: %v", err)
	}
	r, err := LoadResolver(pricePath, "")
	if err != nil || r == nil {
		t.Fatalf("LoadResolver: resolver=%v err=%v", r, err)
	}

	tao, ok := r.Compute("taotoken", "key1", "glm-5.3", map[string]any{
		"input_tokens": 1000000, "output_tokens": 1000000,
	})
	if !ok || tao == nil || math.Abs(tao.TotalCost-36) > 1e-9 {
		t.Fatalf("TaoToken wildcard cost=%+v ok=%v want=36", tao, ok)
	}
	ctyun, ok := r.Compute("ctyun", "key1", "glm-5.3", map[string]any{
		"input_tokens": 1000000, "output_tokens": 1000000,
	})
	if !ok || ctyun == nil || math.Abs(ctyun.TotalCost-39) > 1e-9 {
		t.Fatalf("Ctyun exact cost=%+v ok=%v want=39", ctyun, ok)
	}
}

func TestRepositoryPublicPricesCoverEveryPublicModel(t *testing.T) {
	r, err := LoadResolver("../../../config/price.public.yaml", "")
	if err != nil || r == nil {
		t.Fatalf("LoadResolver: resolver=%v err=%v", r, err)
	}
	models := []string{
		"qwen3.8-max", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus", "qwen3.6-flash",
		"deepseek-v4-flash", "deepseek-v4-pro", "kimi-k3", "minimax-m3", "glm-5.3", "glm-5.2",
	}
	for _, model := range models {
		for _, provider := range []string{"ctyun", "taotoken"} {
			cost, ok := r.Compute(provider, "primary", model, map[string]any{"input_tokens": 1})
			if !ok || cost == nil {
				t.Fatalf("public price missing: provider=%s model=%s", provider, model)
			}
		}
	}
}

func TestLoadResolverMissingPriceFile(t *testing.T) {
	r, err := LoadResolver(filepath.Join(t.TempDir(), "missing.yaml"), "")
	if err != nil {
		t.Fatalf("LoadResolver err=%v", err)
	}
	if r != nil {
		t.Fatalf("resolver should be nil")
	}
}

func TestResolverComputeWithExtraUsageFieldCost(t *testing.T) {
	dir := t.TempDir()
	pricePath := filepath.Join(dir, "price.yaml")
	priceYAML := `
version: v1
unit: usd_per_1m_tokens
entries:
  - provider: openai
    model: gpt-4o-mini-tts
    cost:
      audio_tts_seconds: 0.015
`
	if err := os.WriteFile(pricePath, []byte(priceYAML), 0o600); err != nil {
		t.Fatalf("write price: %v", err)
	}

	r, err := LoadResolver(pricePath, "")
	if err != nil {
		t.Fatalf("LoadResolver: %v", err)
	}
	if r == nil {
		t.Fatalf("resolver is nil")
	}
	c, ok := r.Compute("openai", "key1", "gpt-4o-mini-tts", map[string]any{
		"audio_tts_seconds": 1.32,
		"total_tokens":      0,
	})
	if !ok || c == nil {
		t.Fatalf("Compute failed")
	}
	if math.Abs(c.TotalCost-0.0198) > 1e-9 {
		t.Fatalf("total cost=%v want=0.0198", c.TotalCost)
	}
	if c.InputCost != 0 || c.OutputCost != 0 || c.CacheReadCost != 0 || c.CacheWriteCost != 0 {
		t.Fatalf("unexpected token cost breakdown: %+v", c)
	}
}

func TestResolverComputeWithOverrideOnlyModelCost(t *testing.T) {
	dir := t.TempDir()
	pricePath := filepath.Join(dir, "price.yaml")
	overridesPath := filepath.Join(dir, "price_overrides.yaml")
	priceYAML := `
version: v1
unit: usd_per_1m_tokens
entries:
  - provider: openai
    model: gpt-4o-mini
    cost:
      input: 0.15
      output: 0.60
`
	overridesYAML := `
version: v1
providers:
  openai:
    models:
      gpt-4o-mini-tts:
        cost:
          audio_tts_seconds: 0.015
`
	if err := os.WriteFile(pricePath, []byte(priceYAML), 0o600); err != nil {
		t.Fatalf("write price: %v", err)
	}
	if err := os.WriteFile(overridesPath, []byte(overridesYAML), 0o600); err != nil {
		t.Fatalf("write overrides: %v", err)
	}

	r, err := LoadResolver(pricePath, overridesPath)
	if err != nil {
		t.Fatalf("LoadResolver: %v", err)
	}
	if r == nil {
		t.Fatalf("resolver is nil")
	}

	c, ok := r.Compute("openai", "key1", "gpt-4o-mini-tts", map[string]any{
		"audio_tts_seconds": 1.608,
	})
	if !ok || c == nil {
		t.Fatalf("Compute failed")
	}
	if math.Abs(c.TotalCost-0.02412) > 1e-9 {
		t.Fatalf("total cost=%v want=0.02412", c.TotalCost)
	}
	if got, want := c.Model, "gpt-4o-mini-tts"; got != want {
		t.Fatalf("model=%q want=%q", got, want)
	}
}
