// Package usageadapter defines the explicit provider usage reconciliation
// boundary. Provider-specific HTTP/API knowledge belongs in implementations
// of Adapter, not in the proxy execution engine.
package usageadapter

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Query struct {
	StartTime   time.Time
	EndTime     time.Time
	InternalKey string
	RequestIDs  []string
}

func (q Query) Validate() error {
	if q.StartTime.IsZero() || q.EndTime.IsZero() || !q.EndTime.After(q.StartTime) {
		return errors.New("usage query requires an increasing start and end time")
	}
	if q.EndTime.Sub(q.StartTime) > 31*24*time.Hour {
		return errors.New("usage query range cannot exceed 31 days")
	}
	return nil
}

type Record struct {
	Provider   string
	RequestID  string
	API        string
	Model      string
	OccurredAt time.Time
	Status     int
	Stream     bool
	Usage      map[string]any
	Metadata   map[string]any
}

func (r Record) Validate() error {
	if strings.TrimSpace(r.Provider) == "" {
		return errors.New("provider usage record requires provider")
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return errors.New("provider usage record requires request id")
	}
	if r.OccurredAt.IsZero() {
		return errors.New("provider usage record requires occurred at")
	}
	return nil
}

type Adapter interface {
	Provider() string
	Fetch(context.Context, Query) ([]Record, error)
}
