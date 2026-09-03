package controlplane

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Config struct {
	Addr                 string
	Username             string
	Password             string
	TLS                  bool
	KeyPrefix            string
	OperationTimeout     time.Duration
	AccessKeyHashSecret  string
	BillingStream        string
	BillingConsumerGroup string
	BillingConsumerName  string
	BillingMaxAttempts   int
}

type Client struct {
	rdb                *redis.Client
	prefix             string
	timeout            time.Duration
	hashSecret         []byte
	billingStream      string
	billingGroup       string
	billingConsumer    string
	billingMaxAttempts int
}

type AccessKeyRecord struct {
	Name                string            `json:"name"`
	SecretHash          string            `json:"secret_hash"`
	Status              string            `json:"status"`
	SubjectType         string            `json:"subject_type"`
	SubjectID           string            `json:"subject_id"`
	AccountID           string            `json:"account_id,omitempty"`
	Provisioning        string            `json:"provisioning,omitempty"`
	ProvisioningError   string            `json:"provisioning_error,omitempty"`
	RoutePolicyID       string            `json:"route_policy_id,omitempty"`
	AllowedProviders    []string          `json:"allowed_providers,omitempty"`
	AllowedModels       []string          `json:"allowed_models,omitempty"`
	ProviderKeyBindings map[string]string `json:"provider_key_bindings,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	ExpiresAt           *time.Time        `json:"expires_at,omitempty"`
	Version             int64             `json:"version"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

var ErrAccessKeyVersionConflict = errors.New("access key version conflict")

type SubjectState struct {
	Blocked       bool      `json:"blocked"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
	Version       int64     `json:"version"`
}

type BalanceCacheValue struct {
	Allowed   bool      `json:"allowed"`
	Balance   string    `json:"balance"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LocalBillingEvent is the normalized usage record stored by ONR's local
// billing ledger. AmountMicros is the configured currency amount multiplied by
// 1,000,000. The event is immutable after it is recorded.
type LocalBillingEvent struct {
	ID           string         `json:"id"`
	RequestID    string         `json:"request_id"`
	AccessKeyID  string         `json:"access_key_id"`
	AccountID    string         `json:"account_id"`
	SubjectType  string         `json:"subject_type,omitempty"`
	SubjectID    string         `json:"subject_id,omitempty"`
	Provider     string         `json:"provider"`
	Model        string         `json:"model"`
	API          string         `json:"api,omitempty"`
	Status       int            `json:"status"`
	Stream       bool           `json:"stream"`
	OccurredAt   int64          `json:"occurred_at"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	CachedTokens int64          `json:"cached_tokens"`
	TotalTokens  int64          `json:"total_tokens"`
	AmountMicros int64          `json:"amount_micros"`
	Currency     string         `json:"currency"`
	PricingModel string         `json:"pricing_model,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type LocalBillingAccount struct {
	AccountID     string `json:"account_id"`
	Currency      string `json:"currency"`
	CreditMicros  int64  `json:"credit_micros"`
	SpentMicros   int64  `json:"spent_micros"`
	BalanceMicros int64  `json:"balance_micros"`
}

var ErrLocalBillingDuplicate = errors.New("local billing event already recorded")

type StreamMessage struct {
	ID     string
	Values map[string]string
}

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, errors.New("redis address is required")
	}
	if strings.TrimSpace(cfg.KeyPrefix) == "" {
		cfg.KeyPrefix = "onr"
	}
	if cfg.OperationTimeout <= 0 {
		cfg.OperationTimeout = 500 * time.Millisecond
	}
	if cfg.BillingMaxAttempts <= 0 {
		cfg.BillingMaxAttempts = 10
	}
	if strings.TrimSpace(cfg.BillingConsumerName) == "" {
		host, _ := os.Hostname()
		cfg.BillingConsumerName = fmt.Sprintf("onr-%s-%d", strings.TrimSpace(host), os.Getpid())
	}
	options, err := redis.ParseURL(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("parse redis address: %w", err)
	}
	if options.Addr == "" {
		options.Addr = cfg.Addr
	}
	options.Username = cfg.Username
	options.Password = cfg.Password
	if cfg.TLS {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &Client{
		rdb:                redis.NewClient(options),
		prefix:             strings.Trim(strings.TrimSpace(cfg.KeyPrefix), ":"),
		timeout:            cfg.OperationTimeout,
		hashSecret:         []byte(cfg.AccessKeyHashSecret),
		billingStream:      strings.TrimSpace(cfg.BillingStream),
		billingGroup:       strings.TrimSpace(cfg.BillingConsumerGroup),
		billingConsumer:    strings.TrimSpace(cfg.BillingConsumerName),
		billingMaxAttempts: cfg.BillingMaxAttempts,
	}, nil
}

func (c *Client) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

func (c *Client) Ping(ctx context.Context) error {
	return c.withTimeout(ctx, func(ctx context.Context) error { return c.rdb.Ping(ctx).Err() })
}

func (c *Client) withTimeout(ctx context.Context, fn func(context.Context) error) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return fn(callCtx)
}

func (c *Client) key(parts ...string) string {
	values := []string{c.prefix}
	for _, part := range parts {
		values = append(values, strings.Trim(strings.TrimSpace(part), ":"))
	}
	return strings.Join(values, ":")
}

func (c *Client) accessKeyRecordKey(name string) string { return c.key("access_key", name) }
func (c *Client) accessKeyIndexKey() string             { return c.key("access_key", "index") }
func (c *Client) accessKeyAccountIndexKey() string      { return c.key("access_key", "account_index") }
func (c *Client) subjectKey(subjectType, subjectID string) string {
	return c.key("subject", subjectType, subjectID)
}
func (c *Client) balanceKey(subjectType, subjectID, currency string) string {
	return c.key("balance", subjectType, subjectID, currency)
}
func (c *Client) billingAccessKeyMapKey() string { return c.key(c.billingStream, "access-keys") }
func (c *Client) billingPendingKey() string      { return c.key(c.billingStream, "pending-by-access-key") }

func (c *Client) HashAccessKey(secret string) string {
	mac := hmac.New(sha256.New, c.hashSecret)
	_, _ = mac.Write([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Client) LookupAccessKey(ctx context.Context, secret string) (*AccessKeyRecord, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, nil
	}
	hash := c.HashAccessKey(strings.TrimSpace(secret))
	var name string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		name, err = c.rdb.HGet(ctx, c.accessKeyIndexKey(), hash).Result()
		return err
	})
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c.GetAccessKey(ctx, name)
}

func (c *Client) GetAccessKey(ctx context.Context, name string) (*AccessKeyRecord, error) {
	record, err := c.GetAccessKeyRecord(ctx, name)
	if err != nil || record == nil {
		return nil, err
	}
	if record.Status != "active" || (record.ExpiresAt != nil && time.Now().After(*record.ExpiresAt)) {
		return nil, nil
	}
	return record, nil
}

// GetAccessKeyRecord returns the complete record for administrative views,
// including revoked and expired keys.
func (c *Client) GetAccessKeyRecord(ctx context.Context, name string) (*AccessKeyRecord, error) {
	var raw string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		raw, err = c.rdb.Get(ctx, c.accessKeyRecordKey(name)).Result()
		return err
	})
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record AccessKeyRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, fmt.Errorf("decode Redis access key: %w", err)
	}
	if strings.TrimSpace(record.Name) == "" {
		record.Name = strings.TrimSpace(name)
	}
	record = normalizeAccessKeyRecord(record)
	return &record, nil
}

func (c *Client) PutAccessKey(ctx context.Context, record AccessKeyRecord) error {
	if strings.TrimSpace(record.Name) == "" || strings.TrimSpace(record.SecretHash) == "" {
		return errors.New("access key name and secret hash are required")
	}
	record = normalizeAccessKeyRecord(record)
	if record.Status == "" {
		record.Status = "active"
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	const putScript = `
local owner = redis.call('hget', KEYS[3], ARGV[5])
if ARGV[4] == 'active' and owner and owner ~= ARGV[3] then return -2 end
local previous = redis.call('get', KEYS[1])
if previous then
  local old = cjson.decode(previous)
  if old.secret_hash and old.secret_hash ~= ARGV[2] and redis.call('hget', KEYS[2], old.secret_hash) == ARGV[3] then
    redis.call('hdel', KEYS[2], old.secret_hash)
  end
  if old.account_id and old.account_id ~= ARGV[5] and redis.call('hget', KEYS[3], old.account_id) == ARGV[3] then
    redis.call('hdel', KEYS[3], old.account_id)
  end
end
redis.call('set', KEYS[1], ARGV[1])
redis.call('hset', KEYS[2], ARGV[2], ARGV[3])
if ARGV[4] == 'active' then
  redis.call('hset', KEYS[3], ARGV[5], ARGV[3])
elseif redis.call('hget', KEYS[3], ARGV[5]) == ARGV[3] then
  redis.call('hdel', KEYS[3], ARGV[5])
end
return 1`
	var result int64
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		result, err = c.rdb.Eval(ctx, putScript,
			[]string{c.accessKeyRecordKey(record.Name), c.accessKeyIndexKey(), c.accessKeyAccountIndexKey()},
			raw, record.SecretHash, record.Name, record.Status, record.AccountID,
		).Int64()
		return err
	})
	if err != nil {
		return err
	}
	if result == -2 {
		return fmt.Errorf("an active access key already exists for account %q", record.AccountID)
	}
	if result != 1 {
		return fmt.Errorf("unexpected access key update result %d", result)
	}
	return nil
}

// UpdateAccessKeyRouting atomically replaces only routing fields when the
// caller's record version still matches Redis. Billing identity and secret
// fields are copied from the stored record by the caller and remain unchanged.
func (c *Client) UpdateAccessKeyRouting(ctx context.Context, record AccessKeyRecord, expectedVersion int64) error {
	if strings.TrimSpace(record.Name) == "" || strings.TrimSpace(record.SecretHash) == "" {
		return errors.New("access key name and secret hash are required")
	}
	record = normalizeAccessKeyRecord(record)
	record.Version = expectedVersion + 1
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	const updateScript = `
local previous = redis.call('get', KEYS[1])
if not previous then return 0 end
local old = cjson.decode(previous)
if tonumber(old.version or 0) ~= tonumber(ARGV[2]) then return -1 end
redis.call('set', KEYS[1], ARGV[1])
return 1`
	var result int64
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		result, err = c.rdb.Eval(ctx, updateScript, []string{c.accessKeyRecordKey(record.Name)}, raw, expectedVersion).Int64()
		return err
	})
	if err != nil {
		return err
	}
	switch result {
	case 0:
		return fmt.Errorf("access key %q not found", record.Name)
	case -1:
		return ErrAccessKeyVersionConflict
	case 1:
		return nil
	default:
		return fmt.Errorf("unexpected access key routing update result %d", result)
	}
}

func (c *Client) CreateAccessKey(ctx context.Context, record AccessKeyRecord) error {
	if strings.TrimSpace(record.Name) == "" || strings.TrimSpace(record.SecretHash) == "" {
		return errors.New("access key name and secret hash are required")
	}
	record = normalizeAccessKeyRecord(record)
	if record.Status == "" {
		record.Status = "active"
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	const createScript = `
if redis.call('exists', KEYS[1]) == 1 then return 0 end
if redis.call('hexists', KEYS[2], ARGV[2]) == 1 then return -1 end
if ARGV[4] == 'active' and redis.call('hexists', KEYS[3], ARGV[5]) == 1 then return -2 end
redis.call('set', KEYS[1], ARGV[1])
redis.call('hset', KEYS[2], ARGV[2], ARGV[3])
if ARGV[4] == 'active' then redis.call('hset', KEYS[3], ARGV[5], ARGV[3]) end
return 1`
	var result int64
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		result, err = c.rdb.Eval(ctx, createScript,
			[]string{c.accessKeyRecordKey(record.Name), c.accessKeyIndexKey(), c.accessKeyAccountIndexKey()},
			raw, record.SecretHash, record.Name, record.Status, record.AccountID,
		).Int64()
		return err
	})
	if err != nil {
		return err
	}
	switch result {
	case 0:
		return fmt.Errorf("access key %q already exists", record.Name)
	case -1:
		return fmt.Errorf("access key secret is already associated with another key")
	case -2:
		return fmt.Errorf("an active access key already exists for account %q", record.AccountID)
	case 1:
		return nil
	default:
		return fmt.Errorf("unexpected access key creation result %d", result)
	}
}

func normalizeAccessKeyRecord(record AccessKeyRecord) AccessKeyRecord {
	record.Name = strings.TrimSpace(record.Name)
	record.Status = strings.TrimSpace(record.Status)
	record.SubjectType = strings.TrimSpace(record.SubjectType)
	record.SubjectID = strings.TrimSpace(record.SubjectID)
	record.AccountID = strings.TrimSpace(record.AccountID)
	record.RoutePolicyID = strings.TrimSpace(record.RoutePolicyID)
	record.AllowedProviders = normalizeAccessKeyValues(record.AllowedProviders, true)
	record.AllowedModels = normalizeAccessKeyValues(record.AllowedModels, false)
	bindings := make(map[string]string, len(record.ProviderKeyBindings))
	for provider, keyName := range record.ProviderKeyBindings {
		provider = strings.ToLower(strings.TrimSpace(provider))
		keyName = strings.TrimSpace(keyName)
		if provider != "" && keyName != "" {
			bindings[provider] = keyName
		}
	}
	if len(bindings) == 0 {
		record.ProviderKeyBindings = nil
	} else {
		record.ProviderKeyBindings = bindings
	}
	if record.SubjectID == "" {
		record.SubjectID = record.Name
	}
	if record.AccountID == "" {
		record.AccountID = record.SubjectID
	}
	return record
}

func normalizeAccessKeyValues(values []string, lower bool) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v == "" {
			continue
		}
		if lower {
			v = strings.ToLower(v)
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func (c *Client) RevokeAccessKey(ctx context.Context, name string) error {
	record, err := c.GetAccessKeyRecord(ctx, name)
	if err != nil || record == nil {
		return err
	}
	record.Status = "revoked"
	record.Version++
	return c.PutAccessKey(ctx, *record)
}

// ListAccessKeyRecords returns all records for administrative views.
func (c *Client) ListAccessKeyRecords(ctx context.Context) ([]AccessKeyRecord, error) {
	var names map[string]string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		names, err = c.rdb.HGetAll(ctx, c.accessKeyIndexKey()).Result()
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]AccessKeyRecord, 0, len(names))
	for _, name := range names {
		record, err := c.GetAccessKeyRecord(ctx, name)
		if err != nil {
			return nil, err
		}
		if record != nil {
			out = append(out, *record)
		}
	}
	return out, nil
}

func (c *Client) ListAccessKeys(ctx context.Context) ([]AccessKeyRecord, error) {
	var names map[string]string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		names, err = c.rdb.HGetAll(ctx, c.accessKeyIndexKey()).Result()
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]AccessKeyRecord, 0, len(names))
	for _, name := range names {
		record, err := c.GetAccessKey(ctx, name)
		if err != nil {
			return nil, err
		}
		if record != nil {
			out = append(out, *record)
		}
	}
	return out, nil
}

func (c *Client) RotateAccessKey(ctx context.Context, name string) (string, error) {
	record, err := c.GetAccessKeyRecord(ctx, name)
	if err != nil {
		return "", err
	}
	if record == nil {
		return "", fmt.Errorf("access key %q not found", name)
	}
	secret, err := NewAccessKeySecret()
	if err != nil {
		return "", err
	}
	oldHash := record.SecretHash
	record.SecretHash = c.HashAccessKey(secret)
	record.Version++
	raw, err := json.Marshal(*record)
	if err != nil {
		return "", err
	}
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		pipe := c.rdb.TxPipeline()
		pipe.HDel(ctx, c.accessKeyIndexKey(), oldHash)
		pipe.HSet(ctx, c.accessKeyIndexKey(), record.SecretHash, record.Name)
		pipe.Set(ctx, c.accessKeyRecordKey(record.Name), raw, 0)
		_, err := pipe.Exec(ctx)
		return err
	})
	return secret, err
}

func (c *Client) GetSubjectState(ctx context.Context, subjectType, subjectID string) (SubjectState, error) {
	var raw string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		raw, err = c.rdb.Get(ctx, c.subjectKey(subjectType, subjectID)).Result()
		return err
	})
	if errors.Is(err, redis.Nil) {
		return SubjectState{}, nil
	}
	if err != nil {
		return SubjectState{}, err
	}
	var state SubjectState
	return state, json.Unmarshal([]byte(raw), &state)
}

func (c *Client) SetSubjectState(ctx context.Context, subjectType, subjectID string, state SubjectState) error {
	state.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Set(ctx, c.subjectKey(subjectType, subjectID), raw, 0).Err()
	})
}

func (c *Client) MarkWebhook(ctx context.Context, eventID string, retention time.Duration) (bool, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return false, errors.New("webhook event id is required")
	}
	var marked bool
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		marked, err = c.rdb.SetNX(ctx, c.key("webhook", eventID), "1", retention).Result()
		return err
	})
	return marked, err
}

func (c *Client) GetBalanceCache(ctx context.Context, subjectType, subjectID, currency string) (BalanceCacheValue, bool, error) {
	var raw string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		raw, err = c.rdb.Get(ctx, c.balanceKey(subjectType, subjectID, currency)).Result()
		return err
	})
	if errors.Is(err, redis.Nil) {
		return BalanceCacheValue{}, false, nil
	}
	if err != nil {
		return BalanceCacheValue{}, false, err
	}
	var value BalanceCacheValue
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return BalanceCacheValue{}, false, err
	}
	return value, true, nil
}

func (c *Client) SetBalanceCache(ctx context.Context, subjectType, subjectID, currency string, value BalanceCacheValue, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Set(ctx, c.balanceKey(subjectType, subjectID, currency), raw, ttl).Err()
	})
}

func (c *Client) InvalidateBalanceCache(ctx context.Context, subjectType, subjectID, currency string) error {
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Del(ctx, c.balanceKey(subjectType, subjectID, currency)).Err()
	})
}

func (c *Client) EnqueueBillingEvent(ctx context.Context, payload []byte) (string, error) {
	return c.EnqueueBillingEventForAccessKey(ctx, payload, "")
}

func (c *Client) EnqueueBillingEventForAccessKey(ctx context.Context, payload []byte, accessKeyID string) (string, error) {
	var id string
	const enqueueScript = `
local id = redis.call('xadd', KEYS[1], '*', 'payload', ARGV[1])
if ARGV[2] ~= '' then
  redis.call('hset', KEYS[2], id, ARGV[2])
  redis.call('hincrby', KEYS[3], ARGV[2], 1)
end
return id`
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		result, err := c.rdb.Eval(ctx, enqueueScript, []string{c.key(c.billingStream), c.billingAccessKeyMapKey(), c.billingPendingKey()}, string(payload), strings.TrimSpace(accessKeyID)).Result()
		if err == nil {
			id = fmt.Sprint(result)
		}
		return err
	})
	return id, err
}

func (c *Client) EnsureBillingGroup(ctx context.Context) error {
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.XGroupCreateMkStream(ctx, c.key(c.billingStream), c.billingGroup, "0").Err()
	})
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func (c *Client) ReadBilling(ctx context.Context, count int, block time.Duration) ([]StreamMessage, error) {
	return c.readBilling(ctx, count, block, ">")
}

func (c *Client) ReadPendingBilling(ctx context.Context, count int) ([]StreamMessage, error) {
	return c.readBilling(ctx, count, 0, "0")
}

func (c *Client) readBilling(ctx context.Context, count int, block time.Duration, id string) ([]StreamMessage, error) {
	var messages []redis.XStream
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		messages, err = c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: c.billingGroup, Consumer: c.billingConsumer, Streams: []string{c.key(c.billingStream), id}, Count: int64(count), Block: block}).Result()
		return err
	})
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []StreamMessage
	for _, stream := range messages {
		for _, message := range stream.Messages {
			values := map[string]string{}
			for key, value := range message.Values {
				values[key] = fmt.Sprint(value)
			}
			out = append(out, StreamMessage{ID: message.ID, Values: values})
		}
	}
	return out, nil
}

func (c *Client) AckBilling(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	const ackScript = `
local total = 0
for i, id in ipairs(ARGV) do
  local acknowledged = redis.call('xack', KEYS[1], KEYS[2], id)
  total = total + acknowledged
  if acknowledged > 0 then
    local access_key = redis.call('hget', KEYS[3], id)
    if access_key then
      local pending = redis.call('hincrby', KEYS[4], access_key, -1)
      if pending <= 0 then redis.call('hdel', KEYS[4], access_key) end
      redis.call('hdel', KEYS[3], id)
    end
  end
end
return total`
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Eval(ctx, ackScript, []string{c.key(c.billingStream), c.billingGroup, c.billingAccessKeyMapKey(), c.billingPendingKey()}, ids).Err()
	})
}

func (c *Client) BillingPendingForAccessKey(ctx context.Context, accessKeyID string) (int64, error) {
	var pending int64
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		pending, err = c.rdb.HGet(ctx, c.billingPendingKey(), strings.TrimSpace(accessKeyID)).Int64()
		if errors.Is(err, redis.Nil) {
			pending, err = 0, nil
		}
		return err
	})
	return pending, err
}

func (c *Client) BillingMaxAttempts() int {
	if c == nil || c.billingMaxAttempts <= 0 {
		return 10
	}
	return c.billingMaxAttempts
}

// BillingConsumerName returns the effective consumer name used by this client.
func (c *Client) BillingConsumerName() string {
	if c == nil {
		return ""
	}
	return c.billingConsumer
}

func (c *Client) BillingStats(ctx context.Context) (pending, deadLetter int64, err error) {
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		summary, err := c.rdb.XPending(ctx, c.key(c.billingStream), c.billingGroup).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if summary != nil {
			pending = summary.Count
		}
		deadLetter, err = c.rdb.XLen(ctx, c.key(c.billingStream, "dead-letter")).Result()
		return err
	})
	return pending, deadLetter, err
}

func (c *Client) localBillingKey(parts ...string) string {
	values := []string{"billing"}
	values = append(values, parts...)
	return c.key(values...)
}

func (c *Client) localBillingAccountKey(accountID string) string {
	return c.localBillingKey("account", accountID)
}

func (c *Client) localBillingEventKey(eventID string) string {
	return c.localBillingKey("event", eventID)
}

// InitializeLocalBillingAccount creates an account once. Existing credit and
// spend values are never overwritten, so retries cannot grant initial credit
// twice.
func (c *Client) InitializeLocalBillingAccount(ctx context.Context, accountID, currency string, creditMicros int64) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || strings.TrimSpace(currency) == "" || creditMicros < 0 {
		return errors.New("invalid local billing account")
	}
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.HSetNX(ctx, c.localBillingAccountKey(accountID), "currency", strings.ToUpper(strings.TrimSpace(currency))).Err()
	})
}

// SetInitialLocalBillingCredit adds the initial credit only when the account
// has not been initialized before. The idempotency key is global to the
// account and makes retries safe.
func (c *Client) SetInitialLocalBillingCredit(ctx context.Context, accountID, idempotencyKey, currency string, creditMicros int64) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(idempotencyKey) == "" || creditMicros < 0 {
		return errors.New("invalid local billing credit")
	}
	const script = `
if redis.call('exists', KEYS[1]) == 1 then return 0 end
redis.call('set', KEYS[1], '1')
redis.call('hsetnx', KEYS[2], 'currency', ARGV[2])
redis.call('hincrby', KEYS[2], 'credit_micros', ARGV[1])
return 1`
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Eval(ctx, script, []string{c.localBillingKey("adjustment", idempotencyKey), c.localBillingAccountKey(accountID)}, creditMicros, strings.ToUpper(strings.TrimSpace(currency))).Err()
	})
}

// RecordLocalBillingEvent atomically deduplicates and records one usage event,
// updates the account totals, and indexes the event by account and time.
func (c *Client) RecordLocalBillingEvent(ctx context.Context, event LocalBillingEvent) error {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.AccountID) == "" || event.AmountMicros < 0 {
		return errors.New("invalid local billing event")
	}
	if event.OccurredAt <= 0 {
		event.OccurredAt = time.Now().Unix()
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	const script = `
if redis.call('exists', KEYS[1]) == 1 then return 0 end
redis.call('set', KEYS[1], ARGV[1])
redis.call('zadd', KEYS[3], ARGV[2], ARGV[3])
redis.call('hincrby', KEYS[2], 'spent_micros', ARGV[4])
redis.call('hincrby', KEYS[2], 'input_tokens', ARGV[5])
redis.call('hincrby', KEYS[2], 'output_tokens', ARGV[6])
redis.call('hincrby', KEYS[2], 'cached_tokens', ARGV[7])
redis.call('hincrby', KEYS[2], 'total_tokens', ARGV[8])
return 1`
	var result int64
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		result, err = c.rdb.Eval(ctx, script, []string{
			c.localBillingEventKey(event.ID), c.localBillingAccountKey(event.AccountID), c.localBillingKey("events", event.AccountID),
		}, string(raw), event.OccurredAt, event.ID, event.AmountMicros, event.InputTokens, event.OutputTokens, event.CachedTokens, event.TotalTokens).Int64()
		return err
	})
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrLocalBillingDuplicate
	}
	return nil
}

func (c *Client) ReadLocalBillingAccount(ctx context.Context, accountID string) (LocalBillingAccount, error) {
	var values map[string]string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		values, err = c.rdb.HGetAll(ctx, c.localBillingAccountKey(accountID)).Result()
		return err
	})
	if err != nil {
		return LocalBillingAccount{}, err
	}
	parse := func(name string) int64 { value, _ := strconv.ParseInt(values[name], 10, 64); return value }
	return LocalBillingAccount{AccountID: accountID, Currency: values["currency"], CreditMicros: parse("credit_micros"), SpentMicros: parse("spent_micros"), BalanceMicros: parse("credit_micros") - parse("spent_micros")}, nil
}

func (c *Client) AdjustLocalBillingAccount(ctx context.Context, accountID, idempotencyKey string, deltaMicros int64, currency string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(idempotencyKey) == "" || deltaMicros == 0 {
		return errors.New("invalid local billing adjustment")
	}
	const script = `
if redis.call('exists', KEYS[1]) == 1 then return 0 end
redis.call('set', KEYS[1], '1')
redis.call('hsetnx', KEYS[2], 'currency', ARGV[2])
if ARGV[3] == 'credit' then redis.call('hincrby', KEYS[2], 'credit_micros', ARGV[1])
else redis.call('hincrby', KEYS[2], 'spent_micros', ARGV[1]) end
return 1`
	operation := "debit"
	amount := deltaMicros
	if amount > 0 {
		operation = "credit"
	} else {
		amount = -amount
	}
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Eval(ctx, script, []string{c.localBillingKey("adjustment", idempotencyKey), c.localBillingAccountKey(accountID)}, amount, strings.ToUpper(strings.TrimSpace(currency)), operation).Err()
	})
}

func (c *Client) ReadLocalBillingEvents(ctx context.Context, accountID string, start, end int64, limit int) ([]LocalBillingEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var ids []string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		ids, err = c.rdb.ZRangeByScore(ctx, c.localBillingKey("events", accountID), &redis.ZRangeBy{Min: fmt.Sprint(start), Max: fmt.Sprint(end), Offset: 0, Count: int64(limit)}).Result()
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]LocalBillingEvent, 0, len(ids))
	for _, id := range ids {
		var raw string
		err := c.withTimeout(ctx, func(ctx context.Context) error {
			var err error
			raw, err = c.rdb.Get(ctx, c.localBillingEventKey(id)).Result()
			return err
		})
		if err != nil {
			return nil, err
		}
		var event LocalBillingEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}

func (c *Client) AutoClaimBilling(ctx context.Context, minIdle time.Duration, count int) ([]StreamMessage, error) {
	var messages []redis.XMessage
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		messages, _, err = c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: c.key(c.billingStream), Group: c.billingGroup, Consumer: c.billingConsumer, MinIdle: minIdle, Start: "0-0", Count: int64(count)}).Result()
		if isUnsupportedXAutoClaim(err) {
			messages, err = c.autoClaimBillingLegacy(ctx, minIdle, count)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return normalizeStreamMessages(messages), nil
}

func isUnsupportedXAutoClaim(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown command") && strings.Contains(message, "xautoclaim")
}

func (c *Client) autoClaimBillingLegacy(ctx context.Context, minIdle time.Duration, count int) ([]redis.XMessage, error) {
	if count <= 0 {
		count = 1
	}
	scanCount := int64(count * 10)
	if scanCount < 100 {
		scanCount = 100
	}
	pending, err := c.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: c.key(c.billingStream),
		Group:  c.billingGroup,
		Start:  "-",
		End:    "+",
		Count:  scanCount,
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	ids := make([]string, 0, count)
	for _, item := range pending {
		if item.Idle < minIdle {
			continue
		}
		ids = append(ids, item.ID)
		if len(ids) == count {
			break
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return c.rdb.XClaim(ctx, &redis.XClaimArgs{
		Stream:   c.key(c.billingStream),
		Group:    c.billingGroup,
		Consumer: c.billingConsumer,
		MinIdle:  minIdle,
		Messages: ids,
	}).Result()
}

func normalizeStreamMessages(messages []redis.XMessage) []StreamMessage {
	out := make([]StreamMessage, 0, len(messages))
	for _, message := range messages {
		values := map[string]string{}
		for key, value := range message.Values {
			values[key] = fmt.Sprint(value)
		}
		out = append(out, StreamMessage{ID: message.ID, Values: values})
	}
	return out
}

func (c *Client) IncrementBillingAttempt(ctx context.Context, streamID string) (int64, error) {
	var attempts int64
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		attempts, err = c.rdb.HIncrBy(ctx, c.key(c.billingStream, "attempts"), streamID, 1).Result()
		return err
	})
	return attempts, err
}

func (c *Client) DeadLetterBilling(ctx context.Context, streamID string, payload []byte) error {
	const deadLetterScript = `
redis.call('xadd', KEYS[1], '*', 'source_id', ARGV[1], 'payload', ARGV[2])
redis.call('xack', KEYS[2], KEYS[3], ARGV[1])
redis.call('xdel', KEYS[2], ARGV[1])
redis.call('hdel', KEYS[4], ARGV[1])
local access_key = redis.call('hget', KEYS[5], ARGV[1])
if access_key then
  local pending = redis.call('hincrby', KEYS[6], access_key, -1)
  if pending <= 0 then redis.call('hdel', KEYS[6], access_key) end
  redis.call('hdel', KEYS[5], ARGV[1])
end
return 1`
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Eval(ctx, deadLetterScript, []string{
			c.key(c.billingStream, "dead-letter"),
			c.key(c.billingStream),
			c.billingGroup,
			c.key(c.billingStream, "attempts"),
			c.billingAccessKeyMapKey(),
			c.billingPendingKey(),
		}, streamID, string(payload)).Err()
	})
}

func (c *Client) AcquireBalanceRefreshLock(ctx context.Context, subjectType, subjectID, currency, token string, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, errors.New("balance refresh lock token is required")
	}
	var acquired bool
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var err error
		acquired, err = c.rdb.SetNX(ctx, c.key("balance", "lock", subjectType, subjectID, currency), token, ttl).Result()
		return err
	})
	return acquired, err
}

func (c *Client) ReleaseBalanceRefreshLock(ctx context.Context, subjectType, subjectID, currency, token string) error {
	const releaseScript = `if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) else return 0 end`
	return c.withTimeout(ctx, func(ctx context.Context) error {
		return c.rdb.Eval(ctx, releaseScript, []string{c.key("balance", "lock", subjectType, subjectID, currency)}, token).Err()
	})
}

func NewAccessKeySecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "ak_" + base64.RawURLEncoding.EncodeToString(buf), nil
}
