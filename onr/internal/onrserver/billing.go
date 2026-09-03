package onrserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/r9s-ai/open-next-router/onr/internal/auth"
	"github.com/r9s-ai/open-next-router/onr/internal/proxy"
	"github.com/r9s-ai/open-next-router/pkg/billing"
	"github.com/r9s-ai/open-next-router/pkg/config"
)

func enqueueBillingEvent(cfg *config.Config, sink *billing.Ledger, c *gin.Context, res *proxy.Result) {
	if cfg == nil || sink == nil || !sink.Enabled() || c == nil || res == nil {
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		return
	}
	rid := strings.TrimSpace(c.GetString("X-Onr-Request-Id"))
	if rid == "" {
		rid = strings.TrimSpace(c.GetString("X-Request-Id"))
	}
	principal, _ := auth.PrincipalFromContext(c)
	accessKeyID := strings.TrimSpace(principal.AccessKeyID)
	accountID := strings.TrimSpace(principal.AccountID)
	subjectType := strings.TrimSpace(principal.SubjectType)
	if subjectType == "" {
		subjectType = "api_key"
	}
	subjectID := strings.TrimSpace(principal.SubjectID)
	if subjectID == "" {
		// Keep compatibility with callers/tests that set the legacy context key.
		subjectID = strings.TrimSpace(c.GetString("onr.auth_subject_id"))
	}
	if subjectID == "" {
		subjectID = accessKeyID
	}
	if subjectID == "" {
		return
	}
	usage := res.Usage
	amount := "0"
	cost := pricingHints(res.Cost)
	if value, ok := cost["cost_total"].(float64); ok {
		amount = fmt.Sprintf("%.6f", value)
	}
	if err := sink.Record(requestContext(c), billing.UsageEvent{
		RequestID: rid, AccessKeyID: accessKeyID, AccountID: accountID,
		SubjectType: subjectType, SubjectID: subjectID, Provider: res.Provider,
		Model: res.Model, API: res.API, Status: res.Status, Stream: res.Stream,
		InputTokens:  numberInt64(usage, "prompt_tokens", "input_tokens"),
		OutputTokens: numberInt64(usage, "completion_tokens", "output_tokens"),
		CachedTokens: numberInt64(usage, "cached_tokens", "cache_read_tokens"),
		TotalTokens:  numberInt64(usage, "total_tokens"), Amount: amount,
		Currency:     costString(res.Cost, "cost_unit", sink.Currency()),
		PricingModel: costString(res.Cost, "cost_model", res.Model),
	}); err != nil {
		// Billing is deliberately best-effort after a completed provider response.
		return
	}
}

func enforceBillingBalance(cfg *config.Config, sink *billing.Ledger, c *gin.Context, requestIDHeaderKey string) bool {
	if cfg == nil || sink == nil || c == nil || !sink.Enabled() || !cfg.Billing.Enabled {
		return true
	}
	// Local billing is post-response accounting. It deliberately does not
	// reserve an estimate or hard-stop low balances; concurrent requests may
	// overspend slightly and the ledger records the actual final amount.
	return true
}

func numberInt64(usage map[string]any, names ...string) int64 {
	for _, name := range names {
		if value, ok := usage[name]; ok {
			switch v := value.(type) {
			case int:
				return int64(v)
			case int64:
				return v
			case float64:
				return int64(v)
			}
		}
	}
	return 0
}

func costString(cost map[string]any, name, fallback string) string {
	if value, ok := cost[name].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func requestContext(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}

func pricingHints(cost map[string]any) map[string]any {
	if len(cost) == 0 {
		return map[string]any{}
	}
	unit := numberOr(cost["cost_rate_unit"], 1000000)
	currency := "USD"
	if v, ok := cost["cost_unit"].(string); ok && strings.TrimSpace(v) != "" {
		currency = strings.ToUpper(strings.TrimSpace(v))
	}
	out := map[string]any{}
	if value, ok := cost["cost_total"].(float64); ok {
		out["cost_total"] = value
	}
	for metric, key := range map[string]string{
		"input_tokens":       "price_input",
		"output_tokens":      "price_output",
		"cache_read_tokens":  "price_cache_read",
		"cache_write_tokens": "price_cache_write",
	} {
		if price, ok := cost[key]; ok && numberPositive(price) {
			out[metric] = map[string]any{"unit_price": price, "pricing_unit": unit, "currency": currency}
		}
	}
	if v, ok := out["input_tokens"]; ok {
		out["prompt_tokens"] = v
	}
	if v, ok := out["output_tokens"]; ok {
		out["completion_tokens"] = v
	}
	if v, ok := out["cache_read_tokens"]; ok {
		out["cached_tokens"] = v
	}
	return out
}

func numberPositive(v any) bool {
	switch n := v.(type) {
	case int:
		return n > 0
	case int64:
		return n > 0
	case float64:
		return n > 0
	default:
		return false
	}
}

func numberOr(v any, fallback int) any {
	if numberPositive(v) {
		return v
	}
	return fallback
}
