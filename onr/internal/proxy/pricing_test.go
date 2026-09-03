package proxy

import (
	"testing"

	"github.com/r9s-ai/open-next-router/onr-core/pkg/dslmeta"
	"github.com/r9s-ai/open-next-router/onr-core/pkg/pricing"
)

func TestComputeCostUsesPublicModelBeforeDSLMapping(t *testing.T) {
	resolver, err := pricing.LoadResolver("../../../config/price.ctyun.yaml", "")
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
