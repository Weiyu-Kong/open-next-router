# ONR Meter Gateway Development Plan

## 1. Product Goal

Build ONR into a multi-provider API relay and meter portal. A customer receives
one Access Key and uses it both as the API credential and as the portal login
credential. The customer does not need, and the MVP must not introduce, a
separate local user entity.

The complete customer journey is:

```text
Administrator creates an Access Key and grants initial credit
    -> customer logs in to the meter portal with that Access Key
    -> customer calls the ONR base URL with OpenAI-compatible requests
    -> ONR selects an allowed upstream provider and hides its credentials
    -> request usage and cost are charged to the Access Key account
    -> customer views balance, requests, and model usage by time range
```

The MVP is complete only when all of the following capabilities work together:

1. ONR can relay supported API requests to multiple providers without exposing
   provider credentials, internal key names, or private upstream URLs.
2. Every billable request is attributed to the authenticated Access Key and is
   recorded idempotently in Meterry.
3. Providers that expose delayed usage APIs can be polled for a bounded time
   range and internal upstream key, then reconciled without double charging.
4. Administrators can create, rotate, revoke, credit, debit, and inspect Access
   Keys.
5. A customer can log in with only an Access Key and view only that key's
   balance, request history, and model usage for hourly, daily, and weekly
   periods.

## 2. Fixed Product Decisions

### 2.1 Access Key Is the Account Credential

- One active Access Key represents one customer/account in the MVP.
- No separate `User` entity or username/password login is required.
- The plaintext Access Key may be accepted only at API authentication and portal
  login boundaries. It must not be stored or returned after creation.
- `account_id` is the stable billing identity and maps to one Meterry account.
- `subject_type` and `subject_id` are the stable Meterry usage subject. Balance
  checks, direct usage events, reconciliation events, and user queries must use
  the same mapping.
- A second active Access Key for the same account is rejected. Key rotation must
  preserve the account, wallet, usage history, and balance.

### 2.2 Initial Credit and Balance Behavior

- Every newly provisioned Access Key receives a configurable initial credit and
  currency.
- Meterry is authoritative for accounts, wallets, credits, debits, usage,
  limits, and bills.
- Initial credit uses a deterministic idempotency key so retries cannot grant it
  twice.
- Redis stores access control, sessions, non-secret mappings, caches, and the
  billing outbox. Redis is not an authoritative wallet or ledger.
- The pre-request balance check is best effort. ONR does not reserve estimated
  cost before a request and does not provide a strict hard stop at zero.
- A small temporary overspend is explicitly allowed when the remaining balance
  is low or concurrent requests are in flight.

### 2.3 DSL-Driven Gateway Behavior

- Runtime protocol conversion and provider behavior must be selected by atomic
  directives in `config/providers/*.conf`.
- `onr/internal/proxy` remains an execution engine and must not infer behavior
  from provider names, paths, API names, or model names.
- Provider/model access is restricted by the authenticated Access Key policy and
  explicit model routes. A client-supplied provider cannot bypass that policy.
- Any new DSL directive requires parser support, validation, documentation in
  `DSL_SYNTAX.md`, semantic tests, and provider-directory validation.

### 2.4 Information Isolation

- The browser and gateway client must never receive upstream API keys, internal
  key names, Meterry credentials, Redis credentials, or private upstream URLs.
- User APIs derive account and subject scope from the authenticated server-side
  Access Key session. Caller-supplied account, subject, or Access Key scope is
  never trusted.
- Provider dimensions are available to administrators. User responses suppress
  provider and internal-key dimensions unless a later product decision exposes
  a deliberately sanitized provider label.
- Upstream errors must be normalized so credentials, private URLs, internal
  headers, and stack traces cannot leak.

## 3. Target Request and Accounting Flow

```text
Authenticate Access Key
    -> load account, subject, allowlists, and route policy
    -> perform best-effort balance check
    -> resolve an explicitly configured model route and allowed provider
    -> relay request using a server-owned upstream credential
    -> parse response usage according to provider DSL
    -> calculate cost from configured pricing
    -> enqueue an idempotent billing event with request_id + access_key_id
    -> Meterry updates the authoritative account ledger
```

For delayed provider usage:

```text
Scheduled bounded provider query
    -> identify the server-owned upstream key
    -> normalize provider records to the common usage contract
    -> correlate by provider + request_id
    -> enqueue an idempotent reconciliation event
    -> Meterry applies only the missing charge or correction
```

Required dimensions for an internal usage record:

- `request_id` and billing idempotency key
- `account_id`, `access_key_id`, `subject_type`, and `subject_id`
- requested and resolved model
- provider and server-owned upstream key identity
- API operation and request status
- input, output, cache, and provider-specific token metrics when available
- actual provider cost and customer charged amount
- event source (`direct` or `provider_reconciliation`)
- occurrence, ingestion, and reconciliation timestamps

The upstream key identity is internal-only and must not appear in user APIs.

## 4. Functional Acceptance Criteria

### 4.1 Relay

- OpenAI-compatible chat completions and responses requests work through the ONR
  base URL for explicitly configured providers.
- Streaming and non-streaming usage are captured according to DSL semantics.
- Access Key provider/model/route restrictions are enforced before forwarding.
- Revoked, disabled, or expired keys cannot call the gateway.
- User-visible responses and errors contain no upstream secrets or private
  routing information.

### 4.2 Accounting and Reconciliation

- A successful billable request creates one Meterry charge for the correct
  Access Key account.
- Retries of the same billing event do not create duplicate charges.
- Failed asynchronous events remain pending until acknowledged or dead-lettered.
- The portal exposes an Access Key-scoped freshness timestamp and pending event
  count, never the global queue state.
- At least one concrete provider usage API adapter is configured and verified.
- Direct response usage and delayed provider usage reconciliation cannot charge
  the same usage twice.

### 4.3 Administration

- Create an Access Key with initial credit, currency, model/provider allowlists,
  and route policy.
- Retry partially completed provisioning without duplicating the account,
  wallet, or initial credit.
- List, inspect, rotate, disable, and revoke an Access Key.
- Credit or debit its Meterry wallet with an administrator-supplied idempotency
  key.
- View cross-account balance, requests, charges, model usage, provider usage,
  failed requests, and provisioning health.

### 4.4 Customer Portal

- Log in and log out using only the Access Key.
- Show account identity, balance, currency, initial credit/limit state, and data
  freshness.
- Show request history without provider URL, provider credential, or internal
  key fields.
- Show request count, input/output/cache tokens, and charged amount grouped by
  model.
- Support 24-hour, 7-day, and 30-day windows with hour/day/week buckets.
- Support a bounded custom time range and explicit timezone.
- Revoking the Access Key invalidates both API access and existing portal
  sessions.

## 5. Implementation Status

### Stage 1: Authenticated Billing Principal - Complete

- [x] Propagate `AccessKeyID`, `AccountID`, `SubjectType`, `SubjectID`, and
  `RoutePolicyID` through authentication and billing metadata.
- [x] Preserve compatibility for legacy file and Redis key records.
- [x] Use the authenticated subject for balance checks and billing events.

### Stage 2: Access Policy and Explicit Routing - Complete for MVP

- [x] Persist provider/model allowlists and route policy on Access Keys.
- [x] Enforce allowlists in OpenAI-style and Gemini handlers.
- [x] Enforce route policy against explicit model routes.
- [x] Prevent provider overrides from escaping the configured model route.
- [ ] Add weighted routing, health-aware fallback, and administrator-managed
  route policy editing if these are required beyond the current MVP.

### Stage 3: Access Key Provisioning and Admin Balance Controls - Implemented,
Production Verification Pending

- [x] Add configurable initial credit and currency.
- [x] Deterministically provision or reuse the Meterry account and wallet.
- [x] Grant initial credit idempotently.
- [x] Persist non-sensitive Meterry identifiers and provisioning state.
- [x] Reject a second active key for an account atomically in Redis.
- [x] Preserve account and wallet identity during key rotation.
- [x] Add retryable provisioning and administrator credit/debit operations.
- [x] Add administrator balance/limit snapshots.
- [ ] Add failure-injection tests for every partial provisioning boundary.
- [ ] Verify provisioning and retries against a production-compatible Meterry
  deployment.

### Stage 4: Access Key Login and User Read APIs - Implemented, Integration
Verification Pending

- [x] Add separate Access Key login, logout, profile, and server-side sessions.
- [x] Add login throttling, generic auth errors, expiry, and revocation
  revalidation.
- [x] Add scoped balance, limits, usage, bills, and request-history APIs.
- [x] Derive every query scope from the authenticated Access Key record.
- [x] Support hour/day/week buckets and bounded 24-hour, 7-day, and 30-day
  ranges.
- [ ] Include explicit currency metadata consistently in usage and bill
  responses.
- [ ] Verify the read APIs against a production-compatible Meterry deployment.

### Stage 5: Customer and Administrator Views - Mostly Complete

- [x] Add `/user` Access Key login and logout screens.
- [x] Show balance, model usage, bills, and sanitized request history.
- [x] Add shared 24-hour, 7-day, and 30-day selectors.
- [x] Add administrator cross-account meter summaries and Billing-page views.
- [x] Expose per-key ingestion freshness and pending billing events in the user
  portal through session-scoped `/api/user/freshness`.
- [x] Increment pending state when a keyed billing event is enqueued and clear it
  atomically when the event is acknowledged or moved to dead letter.
- [ ] Add bounded custom start/end timestamps and explicit timezone selection.
- [ ] Confirm all empty, loading, partial-failure, and narrow-screen states.

### Stage 6: Provider Usage APIs and Reconciliation - Framework Complete,
Concrete Integration Pending

- [x] Define a provider-neutral bounded usage adapter contract.
- [x] Add a generic server-configured HTTP JSON adapter.
- [x] Normalize provider records and enqueue provider-scoped idempotent Meterry
  reconciliation events.
- [ ] Define secure configuration for provider usage endpoints, credentials,
  internal key mapping, cursor/pagination, and polling windows.
- [ ] Implement and test at least one concrete provider adapter against its real
  response and pagination behavior.
- [ ] Persist polling checkpoints and overlap windows so delayed records are
  recovered safely.
- [ ] Prove direct usage and provider polling do not double charge.
- [ ] Add reconciliation status, lag, failures, and retry visibility for
  administrators.

### Stage 7: End-to-End and Production Readiness - Pending

- [x] Run complete root-module and independent `onr-core` unit/integration test
  suites with local Redis and HTTP test doubles.
- [ ] Run a real Redis + Meterry + ONR + provider deployment journey.
- [ ] Verify chat completions, responses, streaming, and non-streaming billing.
- [ ] Verify account isolation with at least two Access Keys.
- [ ] Verify initial-credit retry idempotency and key rotation continuity.
- [ ] Verify revocation blocks gateway requests and existing portal sessions.
- [ ] Verify delayed usage reconciliation and no duplicate charge.
- [ ] Audit gateway errors and all user API payloads for secret/provider leakage.
- [ ] Document deployment configuration, migration, backup, recovery, and
  operational rollback procedures.

## 6. Next Execution Order

Work proceeds in small stages, and this file is updated after each completed
stage. Each completed stage receives a dedicated Git commit.

1. Add consistent currency metadata and finish custom range/timezone behavior.
2. Add provisioning failure-injection tests.
3. Configure and verify the first concrete provider usage API integration.
4. Add durable reconciliation checkpoints, lag visibility, and no-double-charge
   tests.
5. Execute the full real-service journey and close the production-readiness
   acceptance criteria.

## 7. Verification Record

- 2026-08-27: the root module and independent `onr-core` module passed complete
  `go test ./...` suites with Go 1.26.6 and `/tmp` Go caches. Coverage included
  Redis/miniredis, Meterry/httptest, provider DSL validation, proxy behavior,
  admin Web APIs, usage extraction, and usage adapters.
- 2026-08-28: the root module passed `go test ./...` after per-Access-Key
  freshness tracking was added. Focused tests also verified Access Key isolation,
  successful acknowledgement, dead-letter cleanup, and the user endpoint.
- A live Meterry/Redis/provider deployment has not yet been verified. Local test
  doubles do not establish external service compatibility or complete Stage 7.

## 8. Explicitly Out of Scope for the MVP

- A separate local user identity, email, password, organization, or membership
  system.
- Multiple simultaneously active customer keys sharing one account.
- Atomic cost reservation or strict zero-balance enforcement.
- A second authoritative wallet or ledger inside ONR.
- Implicit provider compatibility rules in proxy code.
- User selection or disclosure of private provider credentials and internal key
  identities.
