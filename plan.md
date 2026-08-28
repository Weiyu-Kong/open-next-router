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

The word "credit" in this plan means the configured Meterry wallet allowance.
Usage is still reported in both provider-native dimensions (for example input,
output, cache, and reasoning tokens) and the configured charged currency. The
MVP does not maintain a second token quota ledger inside ONR.

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
- Administrative deletion is a soft-delete/revocation operation. It immediately
  blocks gateway and portal authentication but retains the non-secret key ID,
  account mapping, billing history, and audit trail. Physical ledger deletion is
  outside the Access Key lifecycle.

### 2.2 Initial Credit and Balance Behavior

- Every newly provisioned Access Key account receives a configurable initial
  credit and currency exactly once. Rotating its credential does not grant a
  second initial credit.
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

### 2.5 Provider Usage Source Modes

Each provider integration must explicitly select one accounting source mode in
server-side configuration. The proxy must not infer a mode from the provider
name, request path, API operation, or model.

- `direct`: usage returned by the proxied response is the billing source. No
  provider usage poll may create a second charge for that request.
- `authoritative`: ONR stores a reconciliation candidate and delays final
  charging until the provider usage API returns the authoritative record.
- `correction`: direct usage is charged first and a later provider record may
  apply only an explicit idempotent delta. This mode is not required for the
  MVP and must not be enabled until delta semantics are implemented and tested.

Provider usage APIs may expose either of these explicit query shapes:

1. Per-request detail, correlated by an upstream request/generation ID. The
   OpenRouter generation-detail adapter is the first concrete implementation.
2. Time-range listing, scoped by a server-owned internal upstream key identity,
   with pagination/cursor support when the provider offers it.

Credentials for both shapes remain server-side. Persisted candidates and user
payloads contain only opaque internal key identifiers, never plaintext provider
keys.

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
Persist reconciliation candidate with ONR and upstream request IDs
    -> scheduled bounded provider query or per-request detail query
    -> identify the opaque server-owned upstream key mapping
    -> normalize provider records to the common usage contract
    -> correlate by provider + upstream request ID + ONR request ID
    -> enqueue with the original ONR billing idempotency identity
    -> Meterry applies exactly one authoritative charge
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
- Provider adapters support their declared query shape only; unsupported
  pagination, streaming correlation, or time-list behavior fails validation or
  remains visibly pending rather than silently falling back.
- Direct response usage and delayed provider usage reconciliation cannot charge
  the same usage twice.

### 4.3 Administration

- Create an Access Key with initial credit, currency, model/provider allowlists,
  and route policy.
- Retry partially completed provisioning without duplicating the account,
  wallet, or initial credit.
- List, inspect, rotate, disable, and revoke an Access Key.
- Soft-delete an Access Key through revocation while retaining its billing and
  audit records.
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
- [x] Add local failure-injection tests for account lookup/creation, subject
  binding, wallet lookup/creation, initial credit, and lost credit responses.
- [x] Return the one-time secret with a `202 Accepted` pending result when
  provisioning fails after the Redis key is created, and expose a retry action.
- [x] Recheck one-active-key-per-account atomically when a pending key becomes
  active.
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
- [x] Include explicit configured currency metadata consistently in balance,
  limits, usage, bill, and request-history responses.
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
- [x] Add bounded custom start/end timestamps and explicit IANA timezone
  selection shared by usage, bills, and request history.
- [ ] Confirm all empty, loading, partial-failure, and narrow-screen states.

### Stage 6: Provider Usage APIs and Reconciliation - Correlation DSL and
Durable State Complete, Automatic Reconciliation Pending

- [x] Define a provider-neutral bounded usage adapter contract.
- [x] Add a generic server-configured HTTP JSON adapter.
- [x] Normalize provider records and enqueue provider-scoped idempotent Meterry
  reconciliation events.
- [x] Implement and test the OpenRouter generation-detail adapter against a
  realistic response fixture, including its explicit no-pagination limitation.
- [x] Add an explicit DSL directive for extracting the provider request ID from
  response JSON, including parsing, validation, documentation, semantic tests,
  and provider-directory validation. Initial support is non-streaming only
  unless SSE correlation is separately specified and tested.
- [x] Add secure runtime configuration for provider usage mode, endpoint,
  internal key mapping, cursor/pagination behavior, polling/lease intervals,
  and environment-sourced credentials.
- [x] Persist durable reconciliation candidates containing opaque upstream key
  identity, provider request ID, ONR request ID, account scope, attempts, lease,
  and retry state, but no provider secret.
- [ ] Persist polling checkpoints and overlap windows so delayed records are
  recovered safely.
- [ ] Add automatic polling with bounded retries, leases, backoff, dead-letter
  handling, and restart recovery.
- [ ] Prove authoritative mode suppresses direct charging and reuses the
  original ONR idempotency identity so provider polling cannot double charge.
- [ ] Add reconciliation status, lag, failures, and retry visibility for
  administrators.

### Stage 6C: Ctyun Provider Integration - In Progress

- [x] Confirm Ctyun inference uses Bearer APP_KEY and monitor APIs use EOP AK/SK.
- [x] Add server-only Ctyun credential fields and environment overrides to the
  key store; never commit the reference credentials.
- [x] Add explicit Access Key `provider_key_bindings` for selecting one Ctyun
  internal key without automatic provider switching.
- [x] Add the Ctyun EOP signer and monitor aggregate response normalization in
  `onr-core/pkg/ctyun`.
- [x] Add the Ctyun provider DSL with explicit chat model mapping and streaming
  usage inclusion.
- [x] Let administrators assign `provider_key_bindings` when creating an
  Access Key in the Web UI; only opaque internal key names are returned.
- [ ] Wire Ctyun monitor polling into the user/admin usage APIs. Its API returns
  time-bucket aggregates, not per-request records, so it must remain reporting
  data and must not create duplicate billing events.
- [ ] Run live Ctyun inference and monitor API acceptance with a local key
  binding, without exposing upstream credentials or URLs.

Implementation note: local unit tests and provider DSL validation pass. Redis
and HTTP integration suites require socket permissions unavailable in the
restricted execution sandbox, so live Ctyun/Redis/Meterry acceptance remains
open.

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

1. **Stage 6B - Secure reconciliation state.** Add validated authoritative-mode
   runtime configuration, environment-only provider credentials, and durable
   Redis candidate/lease/retry storage.
2. **Stage 6C - Ctyun provider integration.** Use explicit Ctyun model mappings,
   bind each Access Key to one server-owned Ctyun key for testing, and add the
   EOP monitor protocol without automatic fallback or provider replacement.
3. **Stage 6D - Automatic authoritative billing.** Suppress direct billing for
   explicitly authoritative providers, poll OpenRouter, charge with the original
   ONR idempotency identity, and prove retry/restart no-double-charge behavior.
4. **Stage 6E - Operations.** Add checkpoint/overlap behavior where supported,
   reconciliation lag/failure/dead-letter metrics, and administrator retry
   controls. Document OpenRouter's per-request/no-pagination limitation.
5. **Stage 7A - Real-service acceptance.** Run Redis + Meterry + ONR + provider
   journeys for chat completions, responses, streaming/non-streaming behavior,
   two-key isolation, initial credit, rotation, revocation, and reconciliation.
6. **Stage 7B - Production readiness.** Complete leakage audit, browser state
   verification, deployment/migration/backup/recovery/rollback documentation,
   and operational runbooks.

## 7. Requirement Traceability

| Product requirement | Implemented foundation | Remaining completion gate |
| --- | --- | --- |
| Relay multiple API providers behind one Access Key and ONR URL | Explicit provider DSL, model routes, Access Key policies, server-owned credentials, OpenAI-style and Gemini handlers | Real provider chat completions/responses and streaming/non-streaming acceptance; leakage audit |
| Meter every request to the Access Key | Authenticated billing principal, Meterry outbox, idempotent events, best-effort balance check | Automatic authoritative provider reconciliation and no-double-charge proof |
| Query provider usage by request or internal key/time range | Provider-neutral contract, generic HTTP adapter, OpenRouter per-generation adapter | Secure runtime wiring; durable polling; first time-range adapter when a selected provider exposes that API |
| Grant initial credit and allow small overspend | Idempotent Meterry account/wallet provisioning, configurable initial credit, no reservation | Production-compatible Meterry verification and concurrency journey |
| Administer Access Keys and balances | Create/list/inspect/rotate/disable/revoke, soft-delete semantics, credit/debit, meter summaries | Real-service lifecycle verification and reconciliation operations view |
| Access Key portal login and self-service usage views | Server-side sessions, balance/limits, sanitized requests, model usage, bills, freshness, hour/day/week/custom ranges and timezone | Real-service isolation test and empty/loading/error/mobile browser verification |

The first time-range provider adapter is required when the deployment selects a
provider that exposes such an API. OpenRouter's current generation-detail API
does not provide time-range listing, so it satisfies only the per-request query
shape and must not be documented as broader coverage.

## 8. Verification Record

- 2026-08-27: the root module and independent `onr-core` module passed complete
  `go test ./...` suites with Go 1.26.6 and `/tmp` Go caches. Coverage included
  Redis/miniredis, Meterry/httptest, provider DSL validation, proxy behavior,
  admin Web APIs, usage extraction, and usage adapters.
- 2026-08-28: the root module passed `go test ./...` after per-Access-Key
  freshness tracking was added. Focused tests also verified Access Key isolation,
  successful acknowledgement, dead-letter cleanup, and the user endpoint.
- 2026-08-28: the root module passed `go test ./...` after shared time-range,
  IANA timezone, and currency metadata support was added to all user meter
  queries. The embedded portal assets and JavaScript syntax were also checked.
- 2026-08-28: provisioning failure injection covered every Meterry boundary and
  a lost-success response from initial credit. The root module passed
  `go test ./...`; retries retained the one-time secret, activated the original
  key, and applied initial credit once.
- 2026-08-28: a concrete OpenRouter generation-detail usage adapter passed
  realistic HTTP response, server-owned authentication, request-ID integrity,
  bounded-window, and normalization tests. The root module passed `go test ./...`.
- 2026-08-28: Stage 6A added the explicit
  `upstream_request_id_json "$.id";` provider directive and configured it for
  OpenRouter. Parser/validation tests cover malformed and duplicate rules;
  runtime tests cover response-header priority, missing/invalid/non-string
  values, and extraction before downstream JSON deletion. The independent
  `onr-core` and root modules both passed complete `go test ./...` suites.
- 2026-08-28: Stage 6B added validated provider-usage runtime configuration,
  environment-only OpenRouter credentials, and Redis reconciliation candidates.
  Tests cover dependency validation, YAML credential rejection, stable
  candidate identity, duplicate enqueue, random lease tokens, owner-only
  acknowledgement/retry, sanitized error codes, and expired-lease recovery.
  Focused `go test ./pkg/config ./pkg/controlplane` and the complete root
  `go test ./...` suite passed.
- 2026-08-28: Stage 6C added the Ctyun provider DSL, explicit public-model to
  Ctyun model-ID mappings, server-only APP_KEY/EOP credential fields,
  Access Key to internal-key bindings, EOP signing, and aggregate monitor
  response normalization. Focused core tests, provider DSL validation, and the
  complete root `go test ./...` suite passed. Ctyun live API acceptance and
  monitor report wiring remain pending.
- 2026-08-28: The administrator Web API and UI now accept and display explicit
  Access Key provider-key bindings. Admin/service and integration tests passed.
- A live Meterry/Redis/provider deployment has not yet been verified. Local test
  doubles do not establish external service compatibility or complete Stage 7.

## 9. Plan Maintenance Rules

- Update checkboxes and the stage heading in this file in the same commit as
  each completed implementation stage.
- Add the verification command and outcome to the verification record.
- Use one dedicated Git commit per completed stage; do not include unrelated
  worktree files.
- A stage is complete only when implementation, tests, required DSL/docs, and
  failure behavior are all present. Local test doubles must be labeled as such
  and do not close real-service acceptance items.

## 10. Explicitly Out of Scope for the MVP

- A separate local user identity, email, password, organization, or membership
  system.
- Multiple simultaneously active customer keys sharing one account.
- Atomic cost reservation or strict zero-balance enforcement.
- A second authoritative wallet or ledger inside ONR.
- Implicit provider compatibility rules in proxy code.
- User selection or disclosure of private provider credentials and internal key
  identities.
