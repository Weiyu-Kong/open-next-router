# ONR Meter Gateway Development Plan

## 1. Product Goal

Build open-next-router (ONR) into a multi-provider API relay and self-hosted
meter portal. One Access Key is both the API credential and the portal login
credential. The MVP does not introduce a separate user entity.

The customer journey is:

```text
Administrator creates an Access Key and grants initial credit
    -> customer logs in with that Access Key
    -> customer calls the ONR base URL with an OpenAI-compatible request
    -> ONR selects an explicitly configured provider and hides its credentials
    -> ONR records actual response usage and cost in the local Redis ledger
    -> customer views balance, requests, and model usage by time range
```

## 2. Product Decisions

### 2.1 Access Key Identity

- One active Access Key represents one account in the MVP.
- No username, password, email, organization, or separate local user entity.
- Plaintext Access Key is accepted only at API authentication and portal login;
  it is returned once at creation or rotation and is never stored.
- `account_id` is the stable billing identity. Key rotation keeps the same
  account, balance, usage history, and model totals.
- Revocation is a soft delete: gateway and portal authentication stop, while
  the non-secret key ID, account mapping, ledger, and audit data remain.
- Provider credentials and internal provider-key names are server-side only.
  User responses expose only the Access Key's model-level totals.

### 2.2 Local Billing Ledger

- Redis is the authoritative ledger for this ONR deployment. It stores account
  credit, adjustments, immutable usage events, idempotency markers, Access Key
  records, sessions, and billing queue state.
- Each new account receives `billing.initial_credit` exactly once. Repeated
  provisioning and retries cannot grant it twice.
- Monetary values are stored as integer micro-units with six decimal places.
  The configured `billing.currency` is returned in all balance and usage APIs.
- Usage is attributed to the authenticated Access Key/account and grouped for
  customers by the public requested model. Provider and internal-key changes
  do not split the customer's model history.
- Accounting happens after the provider response using actual parsed usage.
  ONR does not reserve an estimated amount or enforce a strict zero-balance
  stop. Low balances and concurrent requests may produce a small overspend.
- A request ID is the usage-event idempotency identity. Replaying the same event
  cannot charge the account twice.

### 2.3 DSL-Driven Runtime

- Runtime behavior is explicitly selected by directives in
  `config/providers/*.conf`.
- `onr/internal/proxy` remains an execution engine; it must not guess behavior
  from provider names, paths, API names, or model names.
- Compatibility transformations must be opt-in atomic DSL directives. Every
  directive requires parser support, validation, `DSL_SYNTAX.md` documentation,
  semantic tests, and provider-directory validation.

### 2.4 Provider Accounting Modes

- `direct`: usage in the proxied response is the billing source. Provider usage
  monitoring must not create a second charge.
- `authoritative`: usage is charged only after a provider usage API returns the
  authoritative record. This mode is not enabled for the current Ctyun path.
- `correction`: a later provider record may apply an explicit idempotent delta;
  this remains out of scope until delta semantics are implemented.

## 3. Current Architecture

```text
Access Key authentication
    -> explicit Access Key route/provider policy
    -> provider relay and DSL response usage extraction
    -> local pricing calculation
    -> pkg/billing.Ledger.Record
    -> Redis atomic event + account-total update
    -> user/admin scoped query APIs and portal views
```

For Ctyun, each Access Key can currently bind to one server-owned Ctyun
credential through `provider_key_bindings`. There is no automatic provider or
credential switching. Ctyun Chat Completions use direct-response billing; the
Ctyun monitor API is available for aggregate reporting work but does not create
billing events.

## 4. Acceptance Criteria

### Relay

- OpenAI-compatible Chat Completions work through the ONR base URL.
- Explicitly configured streaming and non-streaming usage is captured.
- Access Key provider/model/route restrictions are enforced.
- Revoked, disabled, and expired keys cannot call the gateway or use existing
  portal sessions.
- Responses and errors do not leak provider URLs, credentials, or internal key
  names.

### Billing

- A successful billable request creates one local ledger event for the correct
  Access Key and model.
- Initial credit, credit/debit adjustments, and request events are idempotent.
- Balance, token metrics, charged amount, request history, and freshness are
  queryable by the owning Access Key.
- User reports support model, hour, day, week, custom range, and IANA timezone.
- Administrator reports support cross-account inspection and adjustments.
- Redis restart preserves account totals and usage history.

### Provider Usage

- Ctyun credentials use the correct server-side authentication modes: Bearer
  APP_KEY for inference and EOP AK/SK signing for monitor APIs.
- Ctyun public model pricing is loaded from `config/price.ctyun.yaml` and
  charged in the configured currency.
- Delayed provider usage adapters remain explicitly scoped and cannot duplicate
  a direct-response charge.

### Deployment

- Multiple ONR instances may share an HA Redis deployment and the same provider
  configuration. Redis is the shared source of truth for keys, sessions, ledger,
  and queue state.
- Provider DSL, pricing, and secrets must be identical or centrally managed on
  every instance; provider secrets must not be put in Redis.
- The remaining process-failure window between a provider response and the
  Redis ledger write must be monitored and covered by reconciliation where the
  provider supports it.

## 5. Implementation Status

### Stage 1: Authenticated Billing Principal - Complete

- [x] Propagate Access Key ID, account ID, subject, and route policy through
  authentication and billing metadata.
- [x] Use the authenticated subject for gateway policy and billing scope.

### Stage 2: Explicit Access Policy and Routing - Complete for MVP

- [x] Persist and enforce provider/model allowlists and route policies.
- [x] Prevent client provider overrides from escaping configured routes.
- [ ] Add weighted routing, health-aware fallback, or automatic switching only
  if a later product decision requires them.

### Stage 3: Local Redis Ledger and Access Key Administration - Complete for MVP

- [x] Add `pkg/billing` account, event, balance, adjustment, and query types.
- [x] Add Redis atomic initialization, initial credit, event recording, and
  idempotency primitives.
- [x] Create/retry Access Keys with exactly-once initial credit.
- [x] Rotate, disable, revoke, inspect, and route Access Keys.
- [x] Add administrator credit/debit with a caller-supplied idempotency key.
- [x] Remove the external Meterry runtime client, webhook, outbox, config, and
  SDK dependency from the local billing path.

### Stage 4: Access Key Portal and Meter APIs - Complete for MVP

- [x] Add Access Key login, logout, server-side sessions, expiry, throttling,
  and revocation revalidation.
- [x] Add scoped balance, limits, usage, bills, request history, and freshness
  endpoints.
- [x] Add model-level usage with hour/day/week and bounded custom time ranges.
- [x] Add explicit currency, token, cost, empty, loading, error, and narrow
  screen handling in the portal assets.
- [ ] Complete browser-level verification in a normal environment.

### Stage 5: Ctyun Direct Provider Integration - Complete for MVP

- [x] Add the Ctyun provider DSL and explicit Chat Completions model mapping.
- [x] Add server-owned Ctyun APP_KEY and EOP monitor credential handling.
- [x] Add per-Access-Key Ctyun internal-key binding without auto-switching.
- [x] Add streaming and non-streaming usage extraction and response mappings.
- [x] Add Ctyun CNY pricing catalog and local cost calculation.
- [x] Verify direct-response accounting wiring; live acceptance remains a
  deployment task unless credentials and endpoints are available.
- [ ] Add monitor aggregate display to admin/user reports if product requires
  provider-reported reconciliation or comparison data.

### Stage 6: Delayed Usage Reconciliation - Pending

- [x] Define provider-neutral usage adapter and durable candidate state.
- [x] Add explicit provider request-ID extraction and validation.
- [ ] Persist polling checkpoints and overlap windows.
- [ ] Add scheduled polling, leases, backoff, retry, dead-letter, and restart
  recovery.
- [ ] Prove authoritative mode suppresses direct billing and preserves the
  original idempotency identity.
- [ ] Add administrator visibility for reconciliation lag and failures.

### Stage 7: Distributed and Production Readiness - Pending

- [x] Document the shared Redis and identical-configuration deployment boundary.
- [ ] Verify two ONR instances with cross-instance key create/revoke and portal
  session behavior.
- [ ] Verify pending-event recovery after one billing worker stops.
- [ ] Verify Redis persistence, restart, backup, restore, and HA failover.
- [ ] Verify two Access Key account isolation end to end.
- [ ] Verify revocation blocks both gateway requests and existing sessions.
- [ ] Audit all gateway errors and user/admin payloads for secret leakage.
- [ ] Document production migration, rollback, monitoring, and incident
  recovery procedures.

### Stage 8: Provider-Neutral Model Catalog - Complete for MVP

- [x] Add every model from `reference/models-expected.md` to the provider-neutral
  catalog and the runtime selectable model list.
- [x] Add model cards to both the administrator console and the user Meter
  portal, including input, output, and cache-hit pricing when available.
- [x] Preserve blank pricing fields when the Ctyun price document has no exact
  public-model match.
- [x] Keep provider mappings and upstream model IDs in provider-specific
  configuration and admin-only metadata; user model APIs expose no provider
  names, internal IDs, or mapping notes.
- [x] Add explicit Ctyun mappings for the currently supported subset and keep
  unsupported catalog models selectable but unroutable until a provider mapping
  is added.
- [x] Add an admin model picker with all catalog models, search, select-all,
  and clear actions for Access Key creation and routing edits.
- [x] Add tests for the 38-model set, the 16 exact Ctyun price matches, Ctyun
  DSL mapping consistency, user/admin API separation, and model-page assets.

## 6. Next Execution Order

1. Run focused local-ledger and service tests for account isolation, initial
   credit idempotency, administrator adjustment idempotency, model/hour/day/week
   aggregation, and revocation.
2. Run a normal-host Redis + ONR + admin + user acceptance journey with Ctyun:
   create key, call Chat Completions, inspect balance and usage, adjust credit,
   rotate, and revoke.
3. Add the Ctyun monitor aggregate read path only if the product needs provider
   comparison or delayed-usage reporting. Keep it separate from direct charges.
4. Implement delayed reconciliation checkpoints and worker recovery for a
   provider that exposes a supported usage API.
5. Verify two-instance deployment and Redis persistence/HA behavior.
6. Complete deployment and operations documentation.
7. Add additional provider catalogs and mappings only through explicit provider
   DSL directives, catalog metadata, pricing data, and provider validation
   tests.

## 7. Requirement Traceability

| Requirement | Current implementation | Remaining gate |
| --- | --- | --- |
| One Access Key for API and portal | Redis Access Key auth and server-side portal sessions | End-to-end revocation test |
| Hide providers and internal keys | Server-owned credentials and sanitized user APIs | Final leakage audit |
| Charge each request to its model | DSL usage extraction, local pricing, Redis idempotent ledger | Normal-host live acceptance |
| Initial credit and small overspend | Atomic initial credit; post-response accounting | Concurrency and restart acceptance |
| Admin key and balance control | Web/TUI lifecycle and local credit/debit | Browser and two-key acceptance |
| User usage and balance views | Scoped APIs and portal model/time aggregation | Browser verification |
| Provider-neutral model catalog | 38 catalog entries, pricing cards, explicit provider mappings, admin picker | Browser visual verification |
| Distributed operation | Shared Redis-compatible state and queue primitives | Two-instance/HA Redis test |
| Provider monitor APIs | Ctyun EOP normalization and generic adapter foundations | Checkpoints and reconciliation worker |

## 8. Verification Record

- 2026-09-03: Added the Redis-backed local ledger, exactly-once initial credit,
  idempotent usage events and adjustments, local admin/user queries, Ctyun
  pricing integration, and post-response accounting semantics. Focused
  `go test ./pkg/billing ./pkg/controlplane`, `go build ./...`, and
  `git diff --check` passed. Full socket-based tests require a normal host
  because the restricted sandbox blocks local listener creation.
- 2026-09-03: Removed an unused `fmt` import left by the external billing test
  cleanup. The full suite compiles; its HTTP/miniredis tests cannot bind local
  sockets in the restricted sandbox and must be rerun on a normal host.
- 2026-09-03: On a normal host with local socket access, the complete
  `GOCACHE=/tmp/onr-go-build-cache GOMODCACHE=/tmp/onr-go-mod-cache go test ./...`
  suite passed across ONR, admin, billing, Redis, configuration, and usage
  adapter packages.
- 2026-09-03: Removed the old external Meterry runtime path and documentation.
  `billing.edgefn.dev` is not a dependency of the local billing design.
- 2026-09-04: Added the 38-model provider-neutral catalog, Ctyun pricing
  metadata, explicit Ctyun model mappings, admin/user model pages, and the
  admin model picker. Catalog, price, DSL consistency, API isolation, Web,
  Redis, and billing tests passed; JavaScript syntax checks and `git diff
  --check` also passed.
- Pending: live Ctyun request, two-key isolation, revocation, Redis
  restart/backup, and distributed failover verification.

## 9. Maintenance Rules

- Update this file in the same change as each implementation stage.
- Mark a checkbox complete only when implementation, tests, and required docs
  exist. Label local test doubles separately from live-service acceptance.
- Keep source code, CLI output, and English documentation in English. Use a
  `_CN.md` suffix for Chinese documentation.
- Provider DSL changes must include parser, validation, docs, semantic tests,
  and provider-directory validation.

## 10. Explicitly Out of Scope for MVP

- Separate user identity, password login, organization, or membership system.
- Multiple simultaneously active customer keys for one account.
- Atomic cost reservation or strict zero-balance enforcement.
- Automatic provider/key switching or weighted fallback.
- External Meterry website, SDK, API key, or billing dependency.
- User access to provider credentials or internal provider-key identities.
