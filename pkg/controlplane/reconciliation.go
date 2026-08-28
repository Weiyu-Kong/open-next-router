package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReconciliationCandidate is the non-secret correlation state required to
// fetch one authoritative provider usage record. InternalKeyID is an opaque
// server-side key name, never the provider credential itself.
type ReconciliationCandidate struct {
	ID                string    `json:"id"`
	Provider          string    `json:"provider"`
	InternalKeyID     string    `json:"internal_key_id"`
	ProviderRequestID string    `json:"provider_request_id"`
	ONRRequestID      string    `json:"onr_request_id"`
	AccessKeyID       string    `json:"access_key_id"`
	AccountID         string    `json:"account_id"`
	SubjectType       string    `json:"subject_type"`
	SubjectID         string    `json:"subject_id"`
	RoutePolicyID     string    `json:"route_policy_id,omitempty"`
	API               string    `json:"api"`
	Model             string    `json:"model"`
	Stream            bool      `json:"stream"`
	OccurredAt        time.Time `json:"occurred_at"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	Attempts          int       `json:"attempts"`
	LastErrorCode     string    `json:"last_error_code,omitempty"`
	LeaseOwner        string    `json:"lease_owner,omitempty"`
	LeaseToken        string    `json:"lease_token,omitempty"`
	LeaseUntil        time.Time `json:"lease_until,omitempty"`
	NextAttemptAt     time.Time `json:"next_attempt_at"`
}

func (candidate *ReconciliationCandidate) normalize(now time.Time) error {
	candidate.Provider = strings.ToLower(strings.TrimSpace(candidate.Provider))
	candidate.InternalKeyID = strings.TrimSpace(candidate.InternalKeyID)
	candidate.ProviderRequestID = strings.TrimSpace(candidate.ProviderRequestID)
	candidate.ONRRequestID = strings.TrimSpace(candidate.ONRRequestID)
	candidate.AccessKeyID = strings.TrimSpace(candidate.AccessKeyID)
	candidate.AccountID = strings.TrimSpace(candidate.AccountID)
	candidate.SubjectType = strings.TrimSpace(candidate.SubjectType)
	candidate.SubjectID = strings.TrimSpace(candidate.SubjectID)
	candidate.RoutePolicyID = strings.TrimSpace(candidate.RoutePolicyID)
	candidate.API = strings.TrimSpace(candidate.API)
	candidate.Model = strings.TrimSpace(candidate.Model)
	if candidate.Provider == "" || candidate.InternalKeyID == "" || candidate.ProviderRequestID == "" || candidate.ONRRequestID == "" {
		return errors.New("reconciliation candidate requires provider, internal key ID, provider request ID, and ONR request ID")
	}
	if candidate.AccessKeyID == "" || candidate.AccountID == "" || candidate.SubjectType == "" || candidate.SubjectID == "" {
		return errors.New("reconciliation candidate requires Access Key, account, and subject scope")
	}
	if candidate.OccurredAt.IsZero() {
		return errors.New("reconciliation candidate occurred_at is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	expectedID := reconciliationCandidateID(candidate.Provider, candidate.InternalKeyID, candidate.ProviderRequestID)
	if candidate.ID != "" && candidate.ID != expectedID {
		return errors.New("reconciliation candidate ID does not match its provider identity")
	}
	candidate.ID = expectedID
	if candidate.CreatedAt.IsZero() {
		candidate.CreatedAt = now
	}
	candidate.UpdatedAt = now
	if candidate.NextAttemptAt.IsZero() {
		candidate.NextAttemptAt = now
	}
	return nil
}

func reconciliationCandidateID(provider, internalKeyID, providerRequestID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(provider)) + "\x00" + strings.TrimSpace(internalKeyID) + "\x00" + strings.TrimSpace(providerRequestID)))
	return hex.EncodeToString(sum[:])
}

func validateCandidateErrorCode(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("reconciliation error code is required")
	}
	if len(value) > 64 {
		return "", errors.New("reconciliation error code must be at most 64 bytes")
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return "", errors.New("reconciliation error code may contain only lowercase letters, digits, '.', '_', and '-'")
		}
	}
	return value, nil
}

func newReconciliationLeaseToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate reconciliation lease token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (c *Client) reconciliationCandidateKey(id string) string {
	return c.key("reconciliation", "candidate", id)
}

func (c *Client) reconciliationDueKey() string { return c.key("reconciliation", "due") }

// EnqueueReconciliationCandidate creates one stable candidate. Repeated calls
// for the same provider/internal-key/request identity return the existing ID
// and do not reset attempts or an active lease.
func (c *Client) EnqueueReconciliationCandidate(ctx context.Context, candidate ReconciliationCandidate) (string, bool, error) {
	now := time.Now().UTC()
	if err := candidate.normalize(now); err != nil {
		return "", false, err
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return "", false, err
	}
	const script = `
if redis.call('exists', KEYS[1]) == 1 then return 0 end
redis.call('set', KEYS[1], ARGV[1])
redis.call('zadd', KEYS[2], ARGV[2], ARGV[3])
return 1`
	created := false
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		result, evalErr := c.rdb.Eval(ctx, script, []string{c.reconciliationCandidateKey(candidate.ID), c.reconciliationDueKey()}, string(raw), candidate.NextAttemptAt.UnixMilli(), candidate.ID).Int64()
		created = result == 1
		return evalErr
	})
	return candidate.ID, created, err
}

// ClaimReconciliationCandidates atomically leases due candidates. Expired
// leases are represented by their due score and can be claimed again.
func (c *Client) ClaimReconciliationCandidates(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]ReconciliationCandidate, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || lease <= 0 || limit <= 0 {
		return nil, errors.New("claim requires owner, positive lease, and positive limit")
	}
	if limit > 100 {
		limit = 100
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	leaseToken, err := newReconciliationLeaseToken()
	if err != nil {
		return nil, err
	}
	const script = `
local ids = redis.call('zrangebyscore', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
local out = {}
for _, id in ipairs(ids) do
  local key = ARGV[6] .. id
  local raw = redis.call('get', key)
  if raw then
    local item = cjson.decode(raw)
    item.lease_owner = ARGV[3]
    item.lease_token = ARGV[4]
    item.lease_until = ARGV[7]
    item.updated_at = ARGV[8]
    raw = cjson.encode(item)
    redis.call('set', key, raw)
    redis.call('zadd', KEYS[1], ARGV[5], id)
    table.insert(out, raw)
  else
    redis.call('zrem', KEYS[1], id)
  end
end
return out`
	var raws []any
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		var evalErr error
		raws, evalErr = c.rdb.Eval(ctx, script, []string{c.reconciliationDueKey()}, now.UnixMilli(), limit, owner, leaseToken, now.Add(lease).UnixMilli(), c.key("reconciliation", "candidate")+":", now.Add(lease).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)).Slice()
		return evalErr
	})
	if err != nil {
		return nil, err
	}
	out := make([]ReconciliationCandidate, 0, len(raws))
	for _, value := range raws {
		var candidate ReconciliationCandidate
		if err := json.Unmarshal([]byte(fmt.Sprint(value)), &candidate); err != nil {
			return nil, fmt.Errorf("decode reconciliation candidate: %w", err)
		}
		out = append(out, candidate)
	}
	return out, nil
}

// AckReconciliationCandidate removes a candidate only when the caller owns its
// current lease.
func (c *Client) AckReconciliationCandidate(ctx context.Context, id, leaseToken string) (bool, error) {
	const script = `
local raw = redis.call('get', KEYS[1])
if not raw then return 0 end
local item = cjson.decode(raw)
if item.lease_token ~= ARGV[1] then return -1 end
redis.call('del', KEYS[1])
redis.call('zrem', KEYS[2], ARGV[2])
return 1`
	var result int64
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var evalErr error
		result, evalErr = c.rdb.Eval(ctx, script, []string{c.reconciliationCandidateKey(id), c.reconciliationDueKey()}, strings.TrimSpace(leaseToken), strings.TrimSpace(id)).Int64()
		return evalErr
	})
	return result == 1, err
}

// RetryReconciliationCandidate records a sanitized failure code and schedules
// the next attempt only when the caller owns the current lease.
func (c *Client) RetryReconciliationCandidate(ctx context.Context, id, leaseToken, errorCode string, nextAttempt time.Time) (bool, error) {
	errorCode, err := validateCandidateErrorCode(errorCode)
	if err != nil {
		return false, err
	}
	if nextAttempt.IsZero() {
		return false, errors.New("next reconciliation attempt time is required")
	}
	now := time.Now().UTC()
	const script = `
local raw = redis.call('get', KEYS[1])
if not raw then return 0 end
local item = cjson.decode(raw)
if item.lease_token ~= ARGV[1] then return -1 end
item.attempts = (item.attempts or 0) + 1
item.last_error_code = ARGV[2]
item.lease_owner = nil
item.lease_token = nil
item.lease_until = nil
item.next_attempt_at = ARGV[3]
item.updated_at = ARGV[4]
redis.call('set', KEYS[1], cjson.encode(item))
redis.call('zadd', KEYS[2], ARGV[5], ARGV[6])
return 1`
	var result int64
	err = c.withTimeout(ctx, func(ctx context.Context) error {
		var evalErr error
		result, evalErr = c.rdb.Eval(ctx, script, []string{c.reconciliationCandidateKey(id), c.reconciliationDueKey()}, strings.TrimSpace(leaseToken), errorCode, nextAttempt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), nextAttempt.UnixMilli(), strings.TrimSpace(id)).Int64()
		return evalErr
	})
	return result == 1, err
}

func (c *Client) GetReconciliationCandidate(ctx context.Context, id string) (*ReconciliationCandidate, error) {
	var raw string
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var getErr error
		raw, getErr = c.rdb.Get(ctx, c.reconciliationCandidateKey(id)).Result()
		return getErr
	})
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var candidate ReconciliationCandidate
	if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
		return nil, fmt.Errorf("decode reconciliation candidate: %w", err)
	}
	return &candidate, nil
}

func (c *Client) ReconciliationPending(ctx context.Context) (int64, error) {
	var count int64
	err := c.withTimeout(ctx, func(ctx context.Context) error {
		var countErr error
		count, countErr = c.rdb.ZCard(ctx, c.reconciliationDueKey()).Result()
		return countErr
	})
	return count, err
}
