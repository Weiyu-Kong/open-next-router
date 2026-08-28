package ctyun

import (
	"net/url"
	"strings"
	"testing"
)

func TestSignMatchesReferenceShape(t *testing.T) {
	h, err := Sign(SignInput{AccessKey: "ak", SecureKey: "sk", RequestID: "req", EOPDate: "20260828T120000Z", Body: []byte(`{"a":1}`), Query: url.Values{"modelId": {"m"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h["Eop-Authorization"], "ak Headers=ctyun-eop-request-id;eop-date Signature=") {
		t.Fatalf("authorization=%q", h["Eop-Authorization"])
	}
	if len(h["Eop-Authorization"]) <= 70 {
		t.Fatalf("signature missing: %q", h["Eop-Authorization"])
	}
}

func TestReportTotals(t *testing.T) {
	calls := map[string]any{"returnObj": map[string]any{"total": map[string]any{"yAxis": []any{2.0, 3.0}}}}
	tokens := map[string]any{"returnObj": map[string]any{"inputTokens": map[string]any{"y": []any{10.0}}, "outputTokens": map[string]any{"y": []any{4.0}}, "totalTokens": map[string]any{"y": []any{14.0}}}}
	if a, b, c, d := ReportTotals(calls, tokens); a != 5 || b != 10 || c != 4 || d != 14 {
		t.Fatalf("got %v %v %v %v", a, b, c, d)
	}
}
