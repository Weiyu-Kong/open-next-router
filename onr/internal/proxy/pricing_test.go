package proxy

import (
	"math"
	"testing"

	"github.com/r9s-ai/open-next-router/onr-core/pkg/dslmeta"
	"github.com/r9s-ai/open-next-router/onr-core/pkg/pricing"
)

func TestComputeCostUsesPublicModelBeforeDSLMapping(t *testing.T) {
	resolver, err := pricing.LoadResolver("../../../config/price.public.yaml", "")
	if err != nil || resolver == nil {
		t.Fatalf("LoadResolver: resolver=%v err=%v", resolver, err)
	}
	c := &Client{}
	c.SetPricingResolver(resolver)
	c.SetPricingEnabled(true)
	cost := c.computeCost(&dslmeta.Meta{
		OriginModelName: "qwen3.8-max",
		DSLModelMapped:  "3bbfba16dd6e4fed90af61eee76db87f",
	}, "ctyun", "primary", map[string]any{
		"input_tokens": 1000000,
	})
	if cost == nil {
		t.Fatalf("expected Ctyun cost")
	}
	if got := cost["cost_model"]; got != "qwen3.8-max" {
		t.Fatalf("cost_model=%v want public model", got)
	}
	if got := cost["cost_unit"]; got != "cny" {
		t.Fatalf("cost_unit=%v want cny", got)
	}
}

func TestComputeCostUsesUsageBreakdownWithoutDoubleCountingReasoning(t *testing.T) {
	resolver, err := pricing.LoadResolver("../../../config/price.public.yaml", "")
	if err != nil || resolver == nil {
		t.Fatalf("LoadResolver: resolver=%v err=%v", resolver, err)
	}
	c := &Client{}
	c.SetPricingResolver(resolver)
	c.SetPricingEnabled(true)
	cost := c.computeCost(&dslmeta.Meta{OriginModelName: "glm-5.3"}, "taotoken", "primary", map[string]any{
		"input_tokens": 67, "output_tokens": 91, "total_tokens": 158,
		"cache_read_tokens": 0, "reasoning_tokens": 42,
	})
	if cost == nil {
		t.Fatal("expected TaoToken public price")
	}
	// 67 input tokens at 8 CNY/M + 91 output tokens at 28 CNY/M.
	want := (67*8.0 + 91*28.0) / 1_000_000
	if got := cost["cost_total"].(float64); math.Abs(got-want) > 1e-12 {
		t.Fatalf("cost_total=%v want=%v", got, want)
	}
}

func TestComputeCostForCtyunDeepSeekPublicModel(t *testing.T) {
	resolver, err := pricing.LoadResolver("../../../config/price.ctyun.yaml", "")
	if err != nil || resolver == nil {
		t.Fatalf("LoadResolver: resolver=%v err=%v", resolver, err)
	}
	c := &Client{}
	c.SetPricingResolver(resolver)
	c.SetPricingEnabled(true)
	cost := c.computeCost(&dslmeta.Meta{
		OriginModelName: "deepseek-v4-flash",
		DSLModelMapped:  "c60c9aec5710499dafae8e4f395698d8",
	}, "ctyun", "primary", map[string]any{
		"input_tokens": 95, "output_tokens": 2008, "total_tokens": 2103,
		"cache_read_tokens": 0,
	})
	if cost == nil {
		t.Fatal("expected Ctyun DeepSeek cost")
	}
	want := (95*3.0 + 2008*9.0) / 1_000_000
	if got := cost["cost_total"].(float64); math.Abs(got-want) > 1e-12 {
		t.Fatalf("cost_total=%v want=%v", got, want)
	}
	if got := cost["cost_model"]; got != "deepseek-v4-flash" {
		t.Fatalf("cost_model=%v", got)
	}
}
