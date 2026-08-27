package meterry

import (
	"encoding/json"
	"testing"
)

func TestNewEventContainsOnlyBillingMetadata(t *testing.T) {
	e := NewEvent("rid_1", "openai", "chat.completions", "gpt-4.1-mini", true, 200, "upstream", map[string]any{"input_tokens": 12}, "api_key", "key_1", "client", map[string]any{"input_tokens": map[string]any{"unit_price": "1.0"}}, "access-key-1", "account-1", "standard-user")
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || string(b) == "{}" {
		t.Fatalf("unexpected event JSON: %s", b)
	}
	if e.RawJSON["request_body"] != nil {
		t.Fatalf("event must not contain request body")
	}
	if e.IdempotencyKey != "onr:rid_1" {
		t.Fatalf("idempotency key = %q", e.IdempotencyKey)
	}
	if e.SubjectType != "api_key" || e.SubjectID != "key_1" {
		t.Fatalf("subject = %s/%s", e.SubjectType, e.SubjectID)
	}
	if got := e.RawJSON["meta"].(map[string]any)["access_key_id"]; got != "access-key-1" {
		t.Fatalf("access_key_id = %#v", got)
	}
	if got := e.RawJSON["meta"].(map[string]any)["account_id"]; got != "account-1" {
		t.Fatalf("account_id = %#v", got)
	}
	if got := e.RawJSON["meta"].(map[string]any)["route_policy_id"]; got != "standard-user" {
		t.Fatalf("route_policy_id = %#v", got)
	}
	if got := e.RawJSON["usage"].(map[string]any)["prompt_tokens"]; got != 12 {
		t.Fatalf("prompt_tokens alias = %#v", got)
	}
}
