// Package billing contains ONR's self-hosted usage ledger. It deliberately has
// no dependency on an external billing SaaS: Redis stores the immutable usage
// log and the account totals are updated atomically with each log entry.
package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r9s-ai/open-next-router/pkg/controlplane"
	"github.com/shopspring/decimal"
)

const amountScale int64 = 1_000_000

type Config struct {
	Enabled       bool
	Currency      string
	InitialCredit string
}

type UsageEvent struct {
	RequestID    string
	AccessKeyID  string
	AccountID    string
	SubjectType  string
	SubjectID    string
	Provider     string
	Model        string
	API          string
	Status       int
	Stream       bool
	OccurredAt   time.Time
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	TotalTokens  int64
	Amount       string
	Currency     string
	PricingModel string
}

type Account struct {
	AccountID     string `json:"account_id"`
	Currency      string `json:"currency"`
	Credit        string `json:"credit"`
	Spent         string `json:"spent"`
	Balance       string `json:"balance"`
	CreditMicros  int64  `json:"credit_micros"`
	SpentMicros   int64  `json:"spent_micros"`
	BalanceMicros int64  `json:"balance_micros"`
}

type Ledger struct {
	controlPlane *controlplane.Client
	cfg          Config
}

func New(controlPlane *controlplane.Client, cfg Config) (*Ledger, error) {
	if !cfg.Enabled {
		return &Ledger{controlPlane: controlPlane, cfg: cfg}, nil
	}
	if controlPlane == nil {
		return nil, errors.New("local billing requires Redis")
	}
	if strings.TrimSpace(cfg.Currency) == "" {
		cfg.Currency = "CNY"
	}
	return &Ledger{controlPlane: controlPlane, cfg: cfg}, nil
}

func (l *Ledger) Enabled() bool { return l != nil && l.cfg.Enabled }

func (l *Ledger) Currency() string {
	if l == nil || strings.TrimSpace(l.cfg.Currency) == "" {
		return "CNY"
	}
	return strings.ToUpper(strings.TrimSpace(l.cfg.Currency))
}

func amountMicros(value string) (int64, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || amount.IsNegative() {
		return 0, fmt.Errorf("amount must be a non-negative decimal")
	}
	scaled := amount.Mul(decimal.NewFromInt(amountScale)).IntPart()
	if !decimal.NewFromInt(scaled).Equal(amount.Mul(decimal.NewFromInt(amountScale))) {
		return 0, fmt.Errorf("amount supports at most 6 decimal places")
	}
	return scaled, nil
}

func formatMicros(value int64) string {
	return decimal.New(value, -6).StringFixed(6)
}

func (l *Ledger) ProvisionAccount(ctx context.Context, accountID string) error {
	if !l.Enabled() {
		return nil
	}
	credit, err := amountMicros(l.cfg.InitialCredit)
	if err != nil {
		return err
	}
	if err := l.controlPlane.InitializeLocalBillingAccount(ctx, accountID, l.Currency(), credit); err != nil {
		return err
	}
	return l.controlPlane.SetInitialLocalBillingCredit(ctx, accountID, "onr:initial-credit:"+strings.TrimSpace(accountID), l.Currency(), credit)
}

func (l *Ledger) Record(ctx context.Context, in UsageEvent) error {
	if !l.Enabled() {
		return nil
	}
	if strings.TrimSpace(in.RequestID) == "" || strings.TrimSpace(in.AccountID) == "" {
		return errors.New("request_id and account_id are required")
	}
	amount, err := amountMicros(in.Amount)
	if err != nil {
		return err
	}
	when := in.OccurredAt
	if when.IsZero() {
		when = time.Now().UTC()
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		currency = l.Currency()
	}
	event := controlplane.LocalBillingEvent{
		ID: in.RequestID, RequestID: in.RequestID, AccessKeyID: in.AccessKeyID,
		AccountID: in.AccountID, SubjectType: in.SubjectType, SubjectID: in.SubjectID,
		Provider: in.Provider, Model: in.Model, API: in.API, Status: in.Status,
		Stream: in.Stream, OccurredAt: when.Unix(), InputTokens: in.InputTokens,
		OutputTokens: in.OutputTokens, CachedTokens: in.CachedTokens, TotalTokens: in.TotalTokens,
		AmountMicros: amount, Currency: strings.ToUpper(currency), PricingModel: in.PricingModel,
	}
	return l.controlPlane.RecordLocalBillingEvent(ctx, event)
}

func (l *Ledger) ReadAccount(ctx context.Context, accountID string) (Account, error) {
	value, err := l.controlPlane.ReadLocalBillingAccount(ctx, accountID)
	if err != nil {
		return Account{}, err
	}
	return Account{AccountID: value.AccountID, Currency: value.Currency,
		Credit: formatMicros(value.CreditMicros), Spent: formatMicros(value.SpentMicros), Balance: formatMicros(value.BalanceMicros),
		CreditMicros: value.CreditMicros, SpentMicros: value.SpentMicros, BalanceMicros: value.BalanceMicros}, nil
}

func (l *Ledger) ReadEvents(ctx context.Context, accountID string, start, end time.Time, limit int) ([]controlplane.LocalBillingEvent, error) {
	return l.controlPlane.ReadLocalBillingEvents(ctx, accountID, start.Unix(), end.Unix(), limit)
}

func (l *Ledger) Adjust(ctx context.Context, accountID, idempotencyKey, amount string, credit bool) error {
	micros, err := amountMicros(amount)
	if err != nil || micros == 0 {
		return errors.New("amount must be a positive decimal")
	}
	if !credit {
		micros = -micros
	}
	return l.controlPlane.AdjustLocalBillingAccount(ctx, accountID, idempotencyKey, micros, l.Currency())
}
