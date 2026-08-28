package ctyun

import "encoding/json"

// AxisTotal sums a monitor response axis, accepting both documented field names.
func AxisTotal(axis any) float64 {
	m, ok := axis.(map[string]any)
	if !ok {
		return 0
	}
	values, ok := m["y"].([]any)
	if !ok {
		values, _ = m["yAxis"].([]any)
	}
	var total float64
	for _, value := range values {
		switch v := value.(type) {
		case float64:
			total += v
		case json.Number:
			n, _ := v.Float64()
			total += n
		}
	}
	return total
}

// ReportTotals extracts aggregate call and token totals from a Ctyun response.
func ReportTotals(calls, tokens map[string]any) (requestCount, inputTokens, outputTokens, totalTokens float64) {
	if obj, ok := calls["returnObj"].(map[string]any); ok {
		requestCount = AxisTotal(obj["total"])
	}
	if obj, ok := tokens["returnObj"].(map[string]any); ok {
		inputTokens = AxisTotal(obj["inputTokens"])
		outputTokens = AxisTotal(obj["outputTokens"])
		totalTokens = AxisTotal(obj["totalTokens"])
	}
	return
}
