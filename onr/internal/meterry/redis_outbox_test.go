package meterry

import "testing"

func TestEventAccessKeyID(t *testing.T) {
	event := Event{RawJSON: map[string]any{"meta": map[string]any{"access_key_id": "key-a"}}}
	if got := eventAccessKeyID(event); got != "key-a" {
		t.Fatalf("eventAccessKeyID()=%q, want key-a", got)
	}
	if got := eventAccessKeyID(Event{}); got != "" {
		t.Fatalf("eventAccessKeyID(empty)=%q, want empty", got)
	}
}
