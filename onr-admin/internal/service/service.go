package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/r9s-ai/open-next-router/onr-core/pkg/keystore"
	"github.com/r9s-ai/open-next-router/pkg/billing"
	"github.com/r9s-ai/open-next-router/pkg/config"
	"github.com/r9s-ai/open-next-router/pkg/controlplane"
	"github.com/r9s-ai/open-next-router/pkg/modelcatalog"
	"github.com/shopspring/decimal"
)

type CreateAccessKeyInput struct {
	Name, SubjectType, SubjectID    string
	AccountID, RoutePolicyID        string
	AllowedProviders, AllowedModels string
	ProviderKeyBindings             map[string]string
	ExpiresAt                       *time.Time
	Metadata                        map[string]string
}

type UpdateAccessKeyRoutingInput struct {
	AllowedProviders    string
	AllowedModels       string
	ProviderKeyBindings map[string]string
	ExpectedVersion     int64
}

type WalletAdjustmentInput struct {
	Amount         string
	Currency       string
	Reason         string
	IdempotencyKey string
}

type UserUsageQuery struct {
	BucketSize string
	Timezone   string
	StartTime  int64
	EndTime    int64
	Metrics    []string
	GroupBy    []string
	Measures   []string
	Limit      int
}

func (s *Service) BillingCurrency() string {
	if s == nil || s.cfg == nil {
		return "USD"
	}
	currency := strings.TrimSpace(s.cfg.Billing.Currency)
	if currency == "" {
		return "USD"
	}
	return currency
}

type AccessKeyMeterSummary struct {
	AccessKeyID string                   `json:"access_key_id"`
	Status      string                   `json:"status"`
	Balance     *billing.BalanceSnapshot `json:"balance,omitempty"`
	Usage       []billing.UsageRow       `json:"usage,omitempty"`
	Bills       []billing.BillRow        `json:"bills,omitempty"`
	Error       string                   `json:"error,omitempty"`
}

type MigrationReport struct {
	Total, WouldMigrate, Migrated int
	Conflicts, Skipped            []string
}

type Overview struct {
	RedisEnabled, RedisReachable              bool
	RedisError, KeyPrefix, AccessKeyMode      string
	BillingEnabled                            bool
	Pending, DeadLetter                       int64
	BillingError, ConsumerGroup, ConsumerName string
	MaxAttempts                               int
	Currency                                  string
	RefreshedAt                               time.Time
}

type Service struct {
	cfg    *config.Config
	cp     *controlplane.Client
	ledger *billing.Ledger
}

func New(cfgPath string) (*Service, error) {
	cfg, err := config.Load(strings.TrimSpace(cfgPath))
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg}
	if !cfg.Redis.Enabled {
		return s, nil
	}
	s.cp, err = controlplane.New(controlplane.Config{Addr: cfg.Redis.Addr, Username: cfg.Redis.Username, Password: cfg.Redis.Password, TLS: cfg.Redis.TLS, KeyPrefix: cfg.Redis.KeyPrefix, OperationTimeout: time.Duration(cfg.Redis.OperationTimeoutMs) * time.Millisecond, AccessKeyHashSecret: cfg.Redis.AccessKeyHashSecret, BillingStream: cfg.Redis.BillingStream, BillingConsumerGroup: cfg.Redis.BillingConsumerGroup, BillingConsumerName: cfg.Redis.BillingConsumerName, BillingMaxAttempts: cfg.Redis.BillingMaxAttempts})
	if err != nil {
		return nil, fmt.Errorf("init Redis control plane: %w", err)
	}
	if cfg.Billing.Enabled {
		s.ledger, err = billing.New(s.cp, billing.Config{Enabled: true, Currency: cfg.Billing.Currency, InitialCredit: cfg.Billing.InitialCredit})
		if err != nil {
			_ = s.cp.Close()
			return nil, fmt.Errorf("init local billing: %w", err)
		}
		if err := s.initializeExistingLocalAccounts(context.Background()); err != nil {
			_ = s.cp.Close()
			return nil, fmt.Errorf("initialize local billing accounts: %w", err)
		}
	}
	return s, nil
}

func (s *Service) initializeExistingLocalAccounts(ctx context.Context) error {
	if s.ledger == nil || !s.ledger.Enabled() || s.cp == nil {
		return nil
	}
	records, err := s.cp.ListAccessKeyRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Status != "active" && record.Status != "pending" {
			continue
		}
		if strings.TrimSpace(record.AccountID) == "" {
			continue
		}
		if err := s.ledger.ProvisionAccount(ctx, record.AccountID); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Close() error {
	if s == nil || s.cp == nil {
		return nil
	}
	return s.cp.Close()
}
func (s *Service) Config() *config.Config {
	if s == nil {
		return nil
	}
	return s.cfg
}

func (s *Service) ModelCatalog() (*modelcatalog.Catalog, error) {
	if s == nil || s.cfg == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	return modelcatalog.Load(s.cfg.Models.CatalogFile)
}
func (s *Service) Client() *controlplane.Client {
	if s == nil {
		return nil
	}
	return s.cp
}
func (s *Service) ListAccessKeys(ctx context.Context) ([]controlplane.AccessKeyRecord, error) {
	if s.cp == nil {
		return nil, fmt.Errorf("redis access-key management is disabled")
	}
	v, e := s.cp.ListAccessKeyRecords(ctx)
	for i := range v {
		v[i].AllowedModels = nil
	}
	sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	return v, e
}
func (s *Service) GetAccessKey(ctx context.Context, name string) (*controlplane.AccessKeyRecord, error) {
	if s.cp == nil {
		return nil, fmt.Errorf("redis access-key management is disabled")
	}
	record, err := s.cp.GetAccessKeyRecord(ctx, name)
	if record != nil {
		record.AllowedModels = nil
	}
	return record, err
}

func (s *Service) UpdateAccessKeyRouting(ctx context.Context, name string, in UpdateAccessKeyRoutingInput) (*controlplane.AccessKeyRecord, error) {
	if s.cp == nil {
		return nil, fmt.Errorf("redis access-key management is disabled")
	}
	rec, err := s.cp.GetAccessKeyRecord(ctx, strings.TrimSpace(name))
	if err != nil || rec == nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("access key not found")
	}
	if rec.Version != in.ExpectedVersion {
		return nil, controlplane.ErrAccessKeyVersionConflict
	}
	providers := parseCommaList(in.AllowedProviders, true)
	bindings := normalizeProviderKeyBindings(in.ProviderKeyBindings)
	if len(providers) > 0 {
		allowed := make(map[string]struct{}, len(providers))
		for _, provider := range providers {
			allowed[provider] = struct{}{}
		}
		for provider := range bindings {
			if _, ok := allowed[provider]; !ok {
				return nil, fmt.Errorf("provider key binding %q is not in allowed providers", provider)
			}
		}
	}
	rec.AllowedProviders = providers
	// Access Keys are intentionally model-unrestricted. Model availability is
	// controlled by the provider-neutral catalog and provider routing, not by
	// per-key model allowlists.
	rec.AllowedModels = nil
	rec.ProviderKeyBindings = bindings
	if err := s.cp.UpdateAccessKeyRouting(ctx, *rec, in.ExpectedVersion); err != nil {
		return nil, err
	}
	rec.Version = in.ExpectedVersion + 1
	return rec, nil
}

// AuthenticateAccessKey resolves an active Access Key for the user portal.
// The plaintext secret is only used at this boundary and is never returned.
func (s *Service) AuthenticateAccessKey(ctx context.Context, secret string) (*controlplane.AccessKeyRecord, error) {
	if s.cp == nil {
		return nil, fmt.Errorf("redis access-key authentication is disabled")
	}
	return s.cp.LookupAccessKey(ctx, secret)
}
func (s *Service) CreateAccessKey(ctx context.Context, in CreateAccessKeyInput) (string, error) {
	if s.cp == nil {
		return "", fmt.Errorf("redis access-key management is disabled")
	}
	secret, e := controlplane.NewAccessKeySecret()
	if e != nil {
		return "", e
	}
	accountID := strings.TrimSpace(in.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(in.SubjectID)
	}
	rec := controlplane.AccessKeyRecord{
		Name:             in.Name,
		SecretHash:       s.cp.HashAccessKey(secret),
		Status:           "pending",
		SubjectType:      in.SubjectType,
		SubjectID:        in.SubjectID,
		AccountID:        accountID,
		RoutePolicyID:    strings.TrimSpace(in.RoutePolicyID),
		AllowedProviders: parseCommaList(in.AllowedProviders, true),
		// Access Keys do not impose per-model restrictions.
		AllowedModels:       nil,
		ProviderKeyBindings: normalizeProviderKeyBindings(in.ProviderKeyBindings),
		ExpiresAt:           in.ExpiresAt,
		Metadata:            in.Metadata,
		Provisioning:        "pending",
	}
	if e = s.cp.CreateAccessKey(ctx, rec); e != nil {
		return "", e
	}
	if s.ledger != nil && s.ledger.Enabled() {
		if e = s.ledger.ProvisionAccount(ctx, accountID); e != nil {
			s.recordLocalProvisioningFailure(ctx, rec, e)
			return secret, e
		}
		rec.Status = "active"
		rec.Provisioning = "ready"
		if e = s.cp.PutAccessKey(ctx, rec); e != nil {
			return secret, e
		}
		return secret, nil
	}
	rec.Status = "active"
	rec.Provisioning = "ready"
	if e = s.cp.PutAccessKey(ctx, rec); e != nil {
		return secret, e
	}
	return secret, nil
}

func (s *Service) recordLocalProvisioningFailure(ctx context.Context, rec controlplane.AccessKeyRecord, cause error) {
	rec.Status = "pending"
	rec.Provisioning = "failed"
	rec.ProvisioningError = "local billing provisioning failed"
	_ = s.cp.PutAccessKey(ctx, rec)
}

func (s *Service) EnsureAccessKey(ctx context.Context, in CreateAccessKeyInput) (*controlplane.AccessKeyRecord, bool, error) {
	if s.cp == nil {
		return nil, false, fmt.Errorf("redis access-key management is disabled")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, false, fmt.Errorf("access key name is required")
	}
	existing, err := s.cp.GetAccessKeyRecord(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.Status == "pending" {
			if err := s.ProvisionAccessKey(ctx, name); err != nil {
				return nil, false, err
			}
			existing, err = s.cp.GetAccessKeyRecord(ctx, name)
			if err != nil {
				return nil, false, err
			}
		}
		if existing == nil || existing.Status != "active" {
			return nil, false, fmt.Errorf("access key %q is not active", name)
		}
		return existing, false, nil
	}
	if _, err := s.CreateAccessKey(ctx, in); err != nil {
		created, readErr := s.cp.GetAccessKeyRecord(ctx, name)
		if readErr != nil {
			return nil, false, readErr
		}
		if created == nil {
			return nil, false, err
		}
		if created.Status == "pending" {
			if provisionErr := s.ProvisionAccessKey(ctx, name); provisionErr != nil {
				return nil, false, provisionErr
			}
			created, readErr = s.cp.GetAccessKeyRecord(ctx, name)
			if readErr != nil {
				return nil, false, readErr
			}
		}
		if created == nil || created.Status != "active" {
			return nil, false, err
		}
		return created, true, nil
	}
	created, err := s.cp.GetAccessKeyRecord(ctx, name)
	if err != nil {
		return nil, true, err
	}
	if created == nil {
		return nil, true, fmt.Errorf("access key %q not found after creation", name)
	}
	if created.Status == "pending" {
		if err := s.ProvisionAccessKey(ctx, name); err != nil {
			return nil, true, err
		}
		created, err = s.cp.GetAccessKeyRecord(ctx, name)
		if err != nil {
			return nil, true, err
		}
	}
	if created.Status != "active" {
		return nil, true, fmt.Errorf("access key %q is not active", name)
	}
	return created, true, nil
}

func normalizeProviderKeyBindings(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for provider, keyName := range input {
		provider = strings.ToLower(strings.TrimSpace(provider))
		keyName = strings.TrimSpace(keyName)
		if provider != "" && keyName != "" {
			out[provider] = keyName
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ProvisionAccessKey retries local ledger account initialization without
// issuing a new secret or granting initial credit twice.
func (s *Service) ProvisionAccessKey(ctx context.Context, name string) error {
	if s.cp == nil {
		return fmt.Errorf("redis access-key management is disabled")
	}
	rec, err := s.cp.GetAccessKeyRecord(ctx, strings.TrimSpace(name))
	if err != nil || rec == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("access key not found")
	}
	if rec.Status != "pending" {
		return fmt.Errorf("access key provisioning status is %q", rec.Provisioning)
	}
	if s.ledger != nil && s.ledger.Enabled() {
		if err := s.ledger.ProvisionAccount(ctx, rec.AccountID); err != nil {
			return err
		}
		rec.Status, rec.Provisioning, rec.ProvisioningError = "active", "ready", ""
		return s.cp.PutAccessKey(ctx, *rec)
	}
	return fmt.Errorf("local billing is disabled")
}

func (s *Service) RevokeAccessKey(ctx context.Context, name string) error {
	if s.cp == nil {
		return fmt.Errorf("redis access-key management is disabled")
	}
	return s.cp.RevokeAccessKey(ctx, name)
}

func (s *Service) AdjustAccessKeyBalance(ctx context.Context, name, operation string, in WalletAdjustmentInput) (billing.LedgerEntry, error) {
	var zero billing.LedgerEntry
	if s.ledger != nil && s.ledger.Enabled() {
		rec, err := s.cp.GetAccessKeyRecord(ctx, strings.TrimSpace(name))
		if err != nil || rec == nil {
			if err != nil {
				return zero, err
			}
			return zero, fmt.Errorf("access key not found")
		}
		credit := strings.EqualFold(strings.TrimSpace(operation), "credit")
		if !credit && !strings.EqualFold(strings.TrimSpace(operation), "debit") {
			return zero, fmt.Errorf("operation must be credit or debit")
		}
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return zero, fmt.Errorf("idempotency_key is required")
		}
		if err := s.ledger.Adjust(ctx, rec.AccountID, in.IdempotencyKey, in.Amount, credit); err != nil {
			return zero, err
		}
		account, err := s.ledger.ReadAccount(ctx, rec.AccountID)
		if err != nil {
			return zero, err
		}
		amount, _ := decimal.NewFromString(in.Amount)
		if !credit {
			amount = amount.Neg()
		}
		op := "debit"
		if credit {
			op = "credit"
		}
		return billing.LedgerEntry{AccountID: rec.AccountID, Currency: account.Currency, Operation: op, Amount: amount.String(), BalanceAfter: account.Balance, IdempotencyKey: in.IdempotencyKey, CreatedAt: time.Now().Unix()}, nil
	}
	return zero, fmt.Errorf("local billing is disabled")
}

func (s *Service) ReadAccessKeyBalance(ctx context.Context, rec controlplane.AccessKeyRecord) (*billing.BalanceSnapshot, error) {
	if s.ledger == nil || !s.ledger.Enabled() {
		return nil, fmt.Errorf("local billing is disabled")
	}
	account, err := s.ledger.ReadAccount(ctx, rec.AccountID)
	if err != nil {
		return nil, err
	}
	return &billing.BalanceSnapshot{AccountID: account.AccountID, Currency: account.Currency, Balance: account.Balance, AvailableBalance: account.Balance}, nil
}

func (s *Service) ReadAccessKeyLimits(ctx context.Context, rec controlplane.AccessKeyRecord) (*billing.LimitsSnapshot, error) {
	if s.ledger == nil || !s.ledger.Enabled() {
		return nil, fmt.Errorf("local billing is disabled")
	}
	account, err := s.ledger.ReadAccount(ctx, rec.AccountID)
	if err != nil {
		return nil, err
	}
	return &billing.LimitsSnapshot{AccountID: account.AccountID, SubjectType: rec.SubjectType, SubjectID: rec.SubjectID, Currency: account.Currency}, nil
}

func (s *Service) QueryAccessKeyUsage(ctx context.Context, rec controlplane.AccessKeyRecord, in UserUsageQuery) (*billing.UsageResponse, error) {
	return s.queryLocalUsage(ctx, rec, in)
}

func (s *Service) QueryAccessKeyEvents(ctx context.Context, rec controlplane.AccessKeyRecord, startTime, endTime int64, limit int) (*billing.EventsResponse, error) {
	if s.ledger != nil && s.ledger.Enabled() {
		events, err := s.ledger.ReadEvents(ctx, rec.AccountID, time.Unix(startTime, 0), time.Unix(endTime, 0), limit)
		if err != nil {
			return nil, err
		}
		rows := make([]billing.EventRecord, 0, len(events))
		for _, event := range events {
			metrics := map[string]any{"input_tokens": event.InputTokens, "output_tokens": event.OutputTokens, "cached_tokens": event.CachedTokens, "total_tokens": event.TotalTokens, "amount": decimal.New(event.AmountMicros, -6).StringFixed(6), "currency": event.Currency}
			rows = append(rows, billing.EventRecord{OccurredAt: event.OccurredAt, ExternalEventID: event.RequestID, Labels: map[string]string{"model": event.Model}, Metrics: metrics})
		}
		return &billing.EventsResponse{UsageEvents: rows}, nil
	}
	return nil, fmt.Errorf("local billing is disabled")
}

func (s *Service) QueryAccessKeyBills(ctx context.Context, rec controlplane.AccessKeyRecord, startTime, endTime int64, timezone string, limit int) (*billing.BillsResponse, error) {
	return s.queryLocalBills(ctx, rec, startTime, endTime, limit)
}

func localBucket(ts int64, size, timezone string) string {
	loc, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		loc = time.UTC
	}
	d := time.Unix(ts, 0).In(loc)
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1d", "day", "days":
		return d.Format("2006-01-02")
	case "7d", "week", "weeks":
		weekday := int(d.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		return d.AddDate(0, 0, -(weekday - 1)).Format("2006-01-02")
	default:
		return d.Format("2006-01-02T15:00:00-07:00")
	}
}

func (s *Service) localEvents(ctx context.Context, rec controlplane.AccessKeyRecord, start, end int64, limit int) ([]controlplane.LocalBillingEvent, error) {
	return s.ledger.ReadEvents(ctx, rec.AccountID, time.Unix(start, 0), time.Unix(end, 0), limit)
}

func (s *Service) queryLocalUsage(ctx context.Context, rec controlplane.AccessKeyRecord, in UserUsageQuery) (*billing.UsageResponse, error) {
	events, err := s.localEvents(ctx, rec, in.StartTime, in.EndTime, in.Limit)
	if err != nil {
		return nil, err
	}
	type aggregate struct{ input, output, cached, amount, count int64 }
	groups := map[string]*aggregate{}
	for _, event := range events {
		key := localBucket(event.OccurredAt, in.BucketSize, in.Timezone) + "\x00" + event.Model
		if groups[key] == nil {
			groups[key] = &aggregate{}
		}
		g := groups[key]
		g.input += event.InputTokens
		g.output += event.OutputTokens
		g.cached += event.CachedTokens
		g.amount += event.AmountMicros
		g.count++
	}
	rows := make([]billing.UsageRow, 0, len(groups))
	for key, g := range groups {
		parts := strings.SplitN(key, "\x00", 2)
		rows = append(rows, billing.UsageRow{Dimensions: map[string]string{"bucket": parts[0], "model": parts[1]}, Measures: map[string]any{
			"prompt_tokens": g.input, "completion_tokens": g.output, "cached_tokens": g.cached, "quantity": g.input + g.output + g.cached, "amount": decimal.New(g.amount, -6).StringFixed(6), "usage_event_count": g.count,
		}})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Dimensions["bucket"] == rows[j].Dimensions["bucket"] {
			return rows[i].Dimensions["model"] < rows[j].Dimensions["model"]
		}
		return rows[i].Dimensions["bucket"] < rows[j].Dimensions["bucket"]
	})
	return &billing.UsageResponse{Rows: rows}, nil
}

func (s *Service) queryLocalBills(ctx context.Context, rec controlplane.AccessKeyRecord, start, end int64, limit int) (*billing.BillsResponse, error) {
	events, err := s.localEvents(ctx, rec, start, end, limit)
	if err != nil {
		return nil, err
	}
	type aggregate struct{ input, output, cached, amount, count int64 }
	groups := map[string]*aggregate{}
	for _, event := range events {
		if groups[event.Model] == nil {
			groups[event.Model] = &aggregate{}
		}
		g := groups[event.Model]
		g.input += event.InputTokens
		g.output += event.OutputTokens
		g.cached += event.CachedTokens
		g.amount += event.AmountMicros
		g.count++
	}
	rows := make([]billing.BillRow, 0, len(groups))
	for model, g := range groups {
		rows = append(rows, billing.BillRow{Dimensions: map[string]string{"model": model}, RequestCount: g.count, Amount: decimal.New(g.amount, -6).StringFixed(6), Metrics: map[string]billing.BillMetric{"prompt_tokens": {Quantity: g.input}, "completion_tokens": {Quantity: g.output}, "cached_tokens": {Quantity: g.cached}}})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Dimensions["model"] < rows[j].Dimensions["model"] })
	return &billing.BillsResponse{Rows: rows}, nil
}

func (s *Service) BillingPendingForAccessKey(ctx context.Context, accessKeyID string) (int64, error) {
	if s.ledger != nil && s.ledger.Enabled() {
		return 0, nil
	}
	if s.cp == nil {
		return 0, fmt.Errorf("redis billing state is disabled")
	}
	accessKeyID = strings.TrimSpace(accessKeyID)
	if accessKeyID == "" {
		return 0, fmt.Errorf("access key ID is required")
	}
	return s.cp.BillingPendingForAccessKey(ctx, accessKeyID)
}

// ListAccessKeyMeterSummaries provides an administrator-only cross-account
// view. A failure for one account is returned on that row so the remaining
// accounts stay visible.
func (s *Service) ListAccessKeyMeterSummaries(ctx context.Context, startTime, endTime int64) ([]AccessKeyMeterSummary, error) {
	records, err := s.ListAccessKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccessKeyMeterSummary, 0, len(records))
	for _, rec := range records {
		row := AccessKeyMeterSummary{AccessKeyID: rec.Name, Status: rec.Status}
		if rec.Status != "active" {
			out = append(out, row)
			continue
		}
		row.Balance, err = s.ReadAccessKeyBalance(ctx, rec)
		if err != nil {
			row.Error = "balance unavailable"
			out = append(out, row)
			continue
		}
		// Provider and internal key are routing dimensions, not customer billing
		// identities. Keep administrator summaries aligned with the user view so
		// manual provider changes do not split one model's history.
		usage, usageErr := s.QueryAccessKeyUsage(ctx, rec, UserUsageQuery{StartTime: startTime, EndTime: endTime, Metrics: []string{"prompt_tokens", "completion_tokens", "cached_tokens"}, GroupBy: []string{"model"}, Measures: []string{"quantity", "usage_event_count"}, Limit: 1000})
		if usageErr != nil {
			row.Error = "usage unavailable"
		} else {
			row.Usage = usage.Rows
		}
		bills, billErr := s.QueryAccessKeyBills(ctx, rec, startTime, endTime, "UTC", 1000)
		if billErr != nil {
			if row.Error == "" {
				row.Error = "billing unavailable"
			}
		} else {
			row.Bills = bills.Rows
		}
		out = append(out, row)
	}
	return out, nil
}
func (s *Service) RotateAccessKey(ctx context.Context, name string) (string, error) {
	if s.cp == nil {
		return "", fmt.Errorf("redis access-key management is disabled")
	}
	return s.cp.RotateAccessKey(ctx, name)
}
func (s *Service) SubjectState(ctx context.Context, t, id string) (controlplane.SubjectState, error) {
	if s.cp == nil {
		return controlplane.SubjectState{}, fmt.Errorf("redis is disabled")
	}
	return s.cp.GetSubjectState(ctx, t, id)
}
func (s *Service) RedisPing(ctx context.Context) error {
	if s.cp == nil {
		return fmt.Errorf("redis is disabled")
	}
	return s.cp.Ping(ctx)
}
func (s *Service) BillingStats(ctx context.Context) (int64, int64, error) {
	if s.cp == nil {
		return 0, 0, fmt.Errorf("redis is disabled")
	}
	return s.cp.BillingStats(ctx)
}
func (s *Service) Overview(ctx context.Context) Overview {
	o := Overview{RefreshedAt: time.Now()}
	if s == nil || s.cfg == nil {
		return o
	}
	c := s.cfg
	o.RedisEnabled = c.Redis.Enabled
	o.KeyPrefix = c.Redis.KeyPrefix
	o.AccessKeyMode = c.Redis.AccessKeyMode
	o.BillingEnabled = c.Billing.Enabled
	o.Currency = s.BillingCurrency()
	o.ConsumerGroup = c.Redis.BillingConsumerGroup
	o.MaxAttempts = c.Redis.BillingMaxAttempts
	o.ConsumerName = c.Redis.BillingConsumerName
	if s.cp != nil {
		o.ConsumerName = s.cp.BillingConsumerName()
		if e := s.cp.Ping(ctx); e != nil {
			o.RedisError = e.Error()
		} else {
			o.RedisReachable = true
		}
		o.Pending, o.DeadLetter, o.BillingError = safeBilling(ctx, s.cp, c.Billing.Enabled)
	}
	return o
}
func safeBilling(ctx context.Context, cp *controlplane.Client, enabled bool) (int64, int64, string) {
	if !enabled {
		return 0, 0, ""
	}
	p, d, e := cp.BillingStats(ctx)
	if e != nil {
		return 0, 0, e.Error()
	}
	return p, d, ""
}
func parseCommaList(raw string, lower bool) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if lower {
			value = strings.ToLower(value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (s *Service) MigrateAccessKeys(ctx context.Context, path string, dry bool) (MigrationReport, error) {
	var r MigrationReport
	if s.cp == nil {
		return r, fmt.Errorf("redis access-key management is disabled")
	}
	ks, e := keystore.Load(strings.TrimSpace(path))
	if e != nil {
		return r, e
	}
	r.Total = len(ks.AccessKeys())
	for _, k := range ks.AccessKeys() {
		rec := controlplane.AccessKeyRecord{
			Name:        k.Name,
			SecretHash:  s.cp.HashAccessKey(k.Value),
			Status:      "active",
			SubjectType: "api_key",
			SubjectID:   k.Name,
			AccountID:   k.Name,
			Metadata:    map[string]string{"comment": k.Comment},
		}
		old, e := s.cp.GetAccessKeyRecord(ctx, rec.Name)
		if e != nil {
			return r, e
		}
		if old != nil && old.SecretHash != rec.SecretHash {
			r.Conflicts = append(r.Conflicts, rec.Name)
			continue
		}
		if old != nil {
			r.Skipped = append(r.Skipped, rec.Name)
			continue
		}
		if dry {
			r.WouldMigrate++
			continue
		}
		if e = s.cp.CreateAccessKey(ctx, rec); e != nil {
			return r, e
		}
		r.Migrated++
	}
	return r, nil
}

// MigrateConfiguredAccessKeys reads only the keys file selected by the trusted
// ONR configuration. Web callers must not be able to turn this into an
// arbitrary filesystem read by supplying a path in an HTTP request.
func (s *Service) MigrateConfiguredAccessKeys(ctx context.Context, dry bool) (MigrationReport, error) {
	if s == nil || s.cfg == nil {
		return MigrationReport{}, fmt.Errorf("admin service is not configured")
	}
	return s.MigrateAccessKeys(ctx, s.cfg.Keys.File, dry)
}
