package service

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/meterry-com/meterry-go"
	"github.com/meterry-com/meterry-go/pkg/types"
	"github.com/r9s-ai/open-next-router/onr-core/pkg/keystore"
	"github.com/r9s-ai/open-next-router/pkg/config"
	"github.com/r9s-ai/open-next-router/pkg/controlplane"
	"github.com/shopspring/decimal"
)

type CreateAccessKeyInput struct {
	Name, SubjectType, SubjectID    string
	AccountID, RoutePolicyID        string
	AllowedProviders, AllowedModels string
	ExpiresAt                       *time.Time
	Metadata                        map[string]string
}

type WalletAdjustmentInput struct {
	Amount         string
	Currency       string
	Reason         string
	IdempotencyKey string
}

type UserUsageQuery struct {
	BucketSize string
	StartTime  int64
	EndTime    int64
	Metrics    []string
	GroupBy    []string
	Measures   []string
	Limit      int
}

type MigrationReport struct {
	Total, WouldMigrate, Migrated int
	Conflicts, Skipped            []string
}

type Overview struct {
	RedisEnabled, RedisReachable                        bool
	RedisError, KeyPrefix, AccessKeyMode                string
	MeterryEnabled, MeterryConfigured, MeterryReachable bool
	MeterryError, ProjectID, ExtractorRuleSet           string
	Pending, DeadLetter                                 int64
	BillingError, ConsumerGroup, ConsumerName           string
	MaxAttempts                                         int
	FailureMode                                         string
	BalanceCacheTTL, NegativeCacheTTL                   time.Duration
	RefreshedAt                                         time.Time
}

type Service struct {
	cfg     *config.Config
	cp      *controlplane.Client
	meterry *sdk.Client
}

func New(cfgPath string) (*Service, error) {
	cfg, err := config.Load(strings.TrimSpace(cfgPath))
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg}
	if cfg.Meterry.Enabled && strings.TrimSpace(cfg.Meterry.BaseURL) != "" && strings.TrimSpace(cfg.Meterry.APIKey) != "" {
		s.meterry, err = sdk.NewClient(sdk.Config{BaseURL: cfg.Meterry.BaseURL, APIKey: cfg.Meterry.APIKey, HTTPClient: &http.Client{Timeout: 10 * time.Second}})
		if err != nil {
			return nil, fmt.Errorf("init Meterry client: %w", err)
		}
	}
	if !cfg.Redis.Enabled {
		return s, nil
	}
	s.cp, err = controlplane.New(controlplane.Config{Addr: cfg.Redis.Addr, Username: cfg.Redis.Username, Password: cfg.Redis.Password, TLS: cfg.Redis.TLS, KeyPrefix: cfg.Redis.KeyPrefix, OperationTimeout: time.Duration(cfg.Redis.OperationTimeoutMs) * time.Millisecond, AccessKeyHashSecret: cfg.Redis.AccessKeyHashSecret, BillingStream: cfg.Redis.BillingStream, BillingConsumerGroup: cfg.Redis.BillingConsumerGroup, BillingConsumerName: cfg.Redis.BillingConsumerName, BillingMaxAttempts: cfg.Redis.BillingMaxAttempts})
	if err != nil {
		return nil, fmt.Errorf("init Redis control plane: %w", err)
	}
	return s, nil
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
	sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	return v, e
}
func (s *Service) GetAccessKey(ctx context.Context, name string) (*controlplane.AccessKeyRecord, error) {
	if s.cp == nil {
		return nil, fmt.Errorf("redis access-key management is disabled")
	}
	return s.cp.GetAccessKeyRecord(ctx, name)
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
		AllowedModels:    parseCommaList(in.AllowedModels, false),
		ExpiresAt:        in.ExpiresAt,
		Metadata:         in.Metadata,
		Provisioning:     "pending",
	}
	if e = s.cp.CreateAccessKey(ctx, rec); e != nil {
		return "", e
	}
	if !s.cfg.Meterry.Enabled {
		rec.Status = "active"
		rec.Provisioning = "disabled"
		if e = s.cp.PutAccessKey(ctx, rec); e != nil {
			return "", e
		}
		return secret, nil
	}
	if s.meterry == nil {
		return "", fmt.Errorf("Meterry provisioning is not configured; retry the pending access key after configuration")
	}
	if e = s.finishAccessKeyProvisioning(ctx, rec); e != nil {
		return "", e
	}
	return secret, nil
}

// ProvisionAccessKey retries a pending Meterry setup without issuing a new
// secret. It is safe to call repeatedly because account, wallet, and credit
// operations use deterministic identities and idempotency keys.
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
	return s.finishAccessKeyProvisioning(ctx, *rec)
}

func (s *Service) finishAccessKeyProvisioning(ctx context.Context, rec controlplane.AccessKeyRecord) error {
	accountID, err := s.provisionMeterry(ctx, rec)
	if err != nil {
		return err
	}
	rec.MeterryAccountID = accountID
	rec.AccountID = accountID
	rec.Provisioning = "ready"
	rec.Status = "active"
	return s.cp.PutAccessKey(ctx, rec)
}

func (s *Service) provisionMeterry(ctx context.Context, rec controlplane.AccessKeyRecord) (string, error) {
	if s.meterry == nil {
		return "", fmt.Errorf("Meterry is not configured")
	}
	name := "onr-access-key:" + strings.TrimSpace(rec.Name)
	accounts, err := s.meterry.Manager.ListAccountsForProject(ctx, s.cfg.Meterry.ProjectID)
	if err != nil {
		return "", fmt.Errorf("list Meterry accounts: %w", err)
	}
	var account *types.Account
	for i := range accounts {
		if accounts[i].Name == name {
			account = &accounts[i]
			break
		}
	}
	if account == nil {
		account, err = s.meterry.Manager.CreateAccountForProject(ctx, s.cfg.Meterry.ProjectID, types.CreateAccountRequest{Name: name, PrimarySubjectType: rec.SubjectType, PrimarySubjectID: rec.SubjectID})
		if err != nil {
			return "", fmt.Errorf("create Meterry account: %w", err)
		}
	}
	accountID := strings.TrimSpace(account.ID)
	if accountID == "" {
		return "", fmt.Errorf("Meterry account has no id")
	}
	if _, err := s.meterry.Manager.BindSubjectForProject(ctx, s.cfg.Meterry.ProjectID, types.BindAccountSubjectRequest{AccountID: accountID, SubjectType: rec.SubjectType, SubjectID: rec.SubjectID}); err != nil {
		return "", fmt.Errorf("bind Meterry subject: %w", err)
	}
	currency := strings.TrimSpace(s.cfg.Meterry.BalanceEnforcement.Currency)
	if currency == "" {
		currency = "USD"
	}
	wallets, err := s.meterry.Manager.ListWalletsForProject(ctx, s.cfg.Meterry.ProjectID, sdk.ListWalletsRequest{AccountID: accountID})
	if err != nil {
		return "", fmt.Errorf("list Meterry wallets: %w", err)
	}
	if len(wallets) == 0 {
		if _, err := s.meterry.Manager.CreateWalletForProject(ctx, s.cfg.Meterry.ProjectID, types.CreateWalletRequest{AccountID: accountID, Currency: currency}); err != nil {
			return "", fmt.Errorf("create Meterry wallet: %w", err)
		}
	}
	amountText := strings.TrimSpace(s.cfg.Meterry.InitialCredit)
	if amountText == "" || amountText == "0" {
		return accountID, nil
	}
	amount, err := decimal.NewFromString(amountText)
	if err != nil || amount.IsNegative() {
		return "", fmt.Errorf("invalid Meterry initial_credit")
	}
	_, err = s.meterry.Manager.CreditWalletForProject(ctx, s.cfg.Meterry.ProjectID, types.CreditWalletRequest{AccountID: accountID, Currency: currency, Amount: amount, SourceType: "onr_access_key_initial_credit", SourceID: rec.Name, IdempotencyKey: "onr:initial-credit:" + rec.Name})
	if err != nil {
		return "", fmt.Errorf("credit Meterry initial balance: %w", err)
	}
	return accountID, nil
}
func (s *Service) RevokeAccessKey(ctx context.Context, name string) error {
	if s.cp == nil {
		return fmt.Errorf("redis access-key management is disabled")
	}
	return s.cp.RevokeAccessKey(ctx, name)
}

func (s *Service) AdjustAccessKeyBalance(ctx context.Context, name, operation string, in WalletAdjustmentInput) (types.WalletLedgerEntry, error) {
	var zero types.WalletLedgerEntry
	if s.cp == nil || s.meterry == nil {
		return zero, fmt.Errorf("Meterry wallet management is not configured")
	}
	rec, err := s.cp.GetAccessKeyRecord(ctx, strings.TrimSpace(name))
	if err != nil || rec == nil {
		if err != nil {
			return zero, err
		}
		return zero, fmt.Errorf("access key not found")
	}
	accountID := strings.TrimSpace(rec.MeterryAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(rec.AccountID)
	}
	if accountID == "" {
		return zero, fmt.Errorf("access key has no Meterry account")
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(in.Amount))
	if err != nil || !amount.IsPositive() {
		return zero, fmt.Errorf("amount must be a positive decimal")
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		currency = strings.TrimSpace(s.cfg.Meterry.BalanceEnforcement.Currency)
	}
	if currency == "" {
		currency = "USD"
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" {
		return zero, fmt.Errorf("idempotency_key is required")
	}
	source := strings.TrimSpace(in.Reason)
	if source == "" {
		source = "administrator adjustment"
	}
	request := types.CreditWalletRequest{AccountID: accountID, Currency: currency, Amount: amount, SourceType: "onr_admin_adjustment", SourceID: source, IdempotencyKey: key}
	if strings.EqualFold(strings.TrimSpace(operation), "credit") {
		response, err := s.meterry.Manager.CreditWalletForProject(ctx, s.cfg.Meterry.ProjectID, request)
		if err != nil {
			return zero, fmt.Errorf("credit Meterry wallet: %w", err)
		}
		return response.LedgerEntry, nil
	}
	if strings.EqualFold(strings.TrimSpace(operation), "debit") {
		response, err := s.meterry.Manager.DebitWalletForProject(ctx, s.cfg.Meterry.ProjectID, types.DebitWalletRequest{AccountID: accountID, Currency: currency, Amount: amount, SourceType: "onr_admin_adjustment", SourceID: source, IdempotencyKey: key})
		if err != nil {
			return zero, fmt.Errorf("debit Meterry wallet: %w", err)
		}
		return response.LedgerEntry, nil
	}
	return zero, fmt.Errorf("operation must be credit or debit")
}

func (s *Service) ReadAccessKeyBalance(ctx context.Context, rec controlplane.AccessKeyRecord) (*types.VirtualWalletAmountSnapshot, error) {
	if s.meterry == nil {
		return nil, fmt.Errorf("Meterry is not configured")
	}
	accountID := strings.TrimSpace(rec.MeterryAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(rec.AccountID)
	}
	if accountID == "" {
		return nil, fmt.Errorf("access key has no Meterry account")
	}
	currency := strings.TrimSpace(s.cfg.Meterry.BalanceEnforcement.Currency)
	if currency == "" {
		currency = "USD"
	}
	return s.meterry.Manager.ReadVirtualWalletAmountForProject(ctx, s.cfg.Meterry.ProjectID, types.ReadVirtualWalletRequest{AccountID: accountID, Currency: currency})
}

func (s *Service) ReadAccessKeyLimits(ctx context.Context, rec controlplane.AccessKeyRecord) (*types.VirtualWalletLimitsSnapshot, error) {
	if s.meterry == nil {
		return nil, fmt.Errorf("Meterry is not configured")
	}
	currency := strings.TrimSpace(s.cfg.Meterry.BalanceEnforcement.Currency)
	if currency == "" {
		currency = "USD"
	}
	accountID := strings.TrimSpace(rec.MeterryAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(rec.AccountID)
	}
	return s.meterry.Manager.ReadVirtualWalletLimitsForProject(ctx, s.cfg.Meterry.ProjectID, types.ReadVirtualWalletRequest{AccountID: accountID, SubjectType: rec.SubjectType, SubjectID: rec.SubjectID, Currency: currency})
}

func (s *Service) QueryAccessKeyUsage(ctx context.Context, rec controlplane.AccessKeyRecord, in UserUsageQuery) (*types.UsageAnalyticsQueryResponse, error) {
	if s.meterry == nil {
		return nil, fmt.Errorf("Meterry is not configured")
	}
	return s.meterry.Query.UsageForProject(ctx, s.cfg.Meterry.ProjectID, types.UsageAnalyticsQueryRequest{BucketSize: in.BucketSize, SubjectType: rec.SubjectType, SubjectID: rec.SubjectID, Metrics: in.Metrics, StartTime: in.StartTime, EndTime: in.EndTime, GroupBy: in.GroupBy, Measures: in.Measures, Limit: in.Limit})
}

func (s *Service) QueryAccessKeyEvents(ctx context.Context, rec controlplane.AccessKeyRecord, startTime, endTime int64, limit int) (*types.ListUsageEventLogsResponse, error) {
	if s.meterry == nil {
		return nil, fmt.Errorf("Meterry is not configured")
	}
	return s.meterry.Query.ListProjectUsageEvents(ctx, s.cfg.Meterry.ProjectID, types.ListUsageEventLogsRequest{SubjectType: rec.SubjectType, SubjectID: rec.SubjectID, StartTime: startTime, EndTime: endTime, Limit: limit})
}

func (s *Service) QueryAccessKeyBills(ctx context.Context, rec controlplane.AccessKeyRecord, startTime, endTime int64, limit int) (*types.UsageBillQueryResponse, error) {
	if s.meterry == nil {
		return nil, fmt.Errorf("Meterry is not configured")
	}
	accountID := strings.TrimSpace(rec.MeterryAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(rec.AccountID)
	}
	return s.meterry.Query.BillForProject(ctx, s.cfg.Meterry.ProjectID, types.UsageBillQueryRequest{
		Timezone: "UTC", BillingAccountID: accountID, SubjectType: rec.SubjectType, SubjectID: rec.SubjectID,
		Metrics: []string{"prompt_tokens", "completion_tokens", "cached_tokens"}, StartTime: startTime, EndTime: endTime,
		GroupBy: []string{"model"}, Limit: limit,
	})
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
	o.MeterryEnabled = c.Meterry.Enabled
	o.MeterryConfigured = strings.TrimSpace(c.Meterry.BaseURL) != "" && strings.TrimSpace(c.Meterry.ProjectID) != "" && strings.TrimSpace(c.Meterry.APIKey) != ""
	o.ProjectID = redact(c.Meterry.ProjectID)
	o.ExtractorRuleSet = redact(c.Meterry.ExtractorRuleSet)
	o.ConsumerGroup = c.Redis.BillingConsumerGroup
	o.MaxAttempts = c.Redis.BillingMaxAttempts
	o.ConsumerName = c.Redis.BillingConsumerName
	o.FailureMode = c.Meterry.BalanceEnforcement.FailureMode
	o.BalanceCacheTTL = c.Meterry.BalanceCacheTTL()
	o.NegativeCacheTTL = c.Meterry.BalanceNegativeCacheTTL()
	if s.cp != nil {
		o.ConsumerName = s.cp.BillingConsumerName()
		if e := s.cp.Ping(ctx); e != nil {
			o.RedisError = e.Error()
		} else {
			o.RedisReachable = true
		}
		o.Pending, o.DeadLetter, o.BillingError = safeBilling(ctx, s.cp, c.Meterry.Enabled)
	}
	if o.MeterryEnabled && o.MeterryConfigured {
		o.MeterryReachable, o.MeterryError = meterryReachable(ctx, c.Meterry.BaseURL)
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
func meterryReachable(ctx context.Context, base string) (bool, string) {
	req, e := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimRight(base, "/"), nil)
	if e != nil {
		return false, e.Error()
	}
	resp, e := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if e != nil {
		return false, e.Error()
	}
	_ = resp.Body.Close()
	return true, ""
}
func redact(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 10 {
		return v
	}
	return v[:6] + "..." + v[len(v)-4:]
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
