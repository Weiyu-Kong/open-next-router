package billing

type BalanceSnapshot struct {
	AccountID        string `json:"account_id"`
	Currency         string `json:"currency"`
	Balance          string `json:"balance"`
	AvailableBalance string `json:"available_balance"`
}

type LimitsSnapshot struct {
	AccountID   string `json:"account_id"`
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Currency    string `json:"currency"`
}

type UsageRow struct {
	Dimensions map[string]string `json:"dimensions"`
	Measures   map[string]any    `json:"measures"`
}

type UsageResponse struct {
	Rows []UsageRow `json:"rows"`
}

type EventRecord struct {
	OccurredAt      int64             `json:"occurred_at"`
	ExternalEventID string            `json:"external_event_id"`
	Labels          map[string]string `json:"labels,omitempty"`
	Metrics         map[string]any    `json:"metrics,omitempty"`
}

type EventsResponse struct {
	UsageEvents []EventRecord `json:"usage_events"`
}

type BillMetric struct {
	Quantity int64  `json:"quantity"`
	Amount   string `json:"amount"`
}

type BillRow struct {
	Dimensions   map[string]string     `json:"dimensions"`
	RequestCount int64                 `json:"request_count"`
	Metrics      map[string]BillMetric `json:"metrics"`
	Amount       string                `json:"amount"`
}

type BillsResponse struct {
	Rows    []BillRow `json:"rows"`
	HasMore bool      `json:"has_more"`
}

type LedgerEntry struct {
	AccountID      string `json:"account_id"`
	Currency       string `json:"currency"`
	Operation      string `json:"operation"`
	Amount         string `json:"amount"`
	BalanceAfter   string `json:"balance_after"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      int64  `json:"created_at"`
}
