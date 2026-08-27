package usageadapter

import (
	"testing"
	"time"
)

func TestQueryValidate(t *testing.T) {
	now := time.Now()
	if err := (Query{StartTime: now, EndTime: now.Add(time.Hour)}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Query{StartTime: now, EndTime: now.Add(-time.Hour)}).Validate(); err == nil {
		t.Fatal("expected invalid range")
	}
	if err := (Query{StartTime: now, EndTime: now.Add(32 * 24 * time.Hour)}).Validate(); err == nil {
		t.Fatal("expected range limit")
	}
}

func TestRecordValidate(t *testing.T) {
	record := Record{Provider: "openai", RequestID: "req-1", OccurredAt: time.Now()}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Record{{RequestID: "req-1", OccurredAt: time.Now()}, {Provider: "openai", OccurredAt: time.Now()}, {Provider: "openai", RequestID: "req-1"}} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("expected invalid record: %+v", invalid)
		}
	}
}
