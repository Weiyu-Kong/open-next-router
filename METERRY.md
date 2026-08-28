# Meterry Integration

This document describes the ONR configuration required to expose balance and
usage for an Access Key. Redis stores Access Key records and the durable
billing queue. Meterry is the authoritative account, wallet, usage, and bill
store.

## Billing Identity

ONR creates one Meterry account for each Access Key. The account name is:

```text
onr-access-key:<access-key-name>
```

The account is bound to the Access Key record's `subject_type` and
`subject_id`. Requests are ingested with the same subject and with the public
model in `raw_json.model`. Provider and internal key are retained as server
event dimensions for audit, but user and administrator summaries group by
`model` only. Manually changing `ctyun/primary` to another provider or key
therefore does not split the customer's model history.

Do not create a separate Meterry account per provider key or per upstream
provider for this product model.

## Required Meterry Setup

Create or select a Meterry project and configure:

1. An API key that can create/read accounts and wallets, ingest usage events,
   and query usage, events, and bills for that project.
2. A usage extractor rule set with the ID configured in ONR.
3. Usage facts for `prompt_tokens`, `completion_tokens`, and
   `cached_tokens`, using the event paths below.
4. Billing rules that charge those token quantities in the selected currency.

The exact rule-editor names may differ between Meterry deployments. The
semantic contract is:

| Dimension or metric | Event path | Unit |
| --- | --- | --- |
| model | `$.model` | label |
| provider | `$.provider` | internal label |
| API | `$.api` | internal label |
| request status | `$.status` | internal label |
| input tokens | `$.usage.prompt_tokens` | token |
| output tokens | `$.usage.completion_tokens` | token |
| cached input tokens | `$.usage.cached_tokens` | token |

The input and output token facts should be billable. Cached tokens should use
the Ctyun cached-input price when a cached price is configured; otherwise set
its price to zero or omit it according to the business tariff. Do not charge
`total_tokens` as a third independent metric, because it would double count
input and output.

Set the rule set ID, for example:

```text
ers_onr_gateway
```

The value is deployment-specific. It must exist in Meterry before ONR starts
ingesting events.

## Currency and Ctyun Prices

Choose one wallet currency and use it everywhere. For a Ctyun deployment using
RMB, use `CNY` for the wallet and configure the Ctyun model price as CNY per
one million tokens. The current provider mapping contains these example rates:

| Public model | Input | Cached input | Output |
| --- | ---: | ---: | ---: |
| qwen3.8-max | 12 | 1.5 | 36 |
| qwen3.7-max | 12 | 2.4 | 36 |
| qwen3.7-plus | 2 | 0.4 | 8 |
| qwen3.6-plus | 2 | 0 | 12 |
| qwen3.6-flash | 1.2 | 0 | 7.2 |
| deepseek-v4-flash-0817 | 3 | 0.1 | 9 |
| deepseek-v4-pro-0817 | 9 | 0.3 | 27 |
| kimi-k3 | 20 | 2 | 100 |
| minimax-m3 | 2.1 | 0.42 | 8.4 |
| glm-5.3 | 8 | 2 | 28 |
| glm-5.2 | 8 | 2 | 28 |

These prices must be entered in the Meterry extractor/rate configuration in
the units expected by that deployment. If ONR-side cost metadata is enabled,
the local price catalog must use the same currency and model names. Never mix
CNY wallet amounts with USD prices.

## ONR Configuration

Enable Redis and Meterry in `onr.yaml`:

```yaml
meterry:
  enabled: true
  base_url: "https://meter.example.internal"
  project_id: "project-onr"
  api_key: ""
  extractor_rule_set_id: "ers_onr_gateway"
  initial_credit: "200"
  outbox_dir: "./run/meterry"
  request_timeout_ms: 3000
  retry_interval_ms: 1000
  only_billable_success: true
  subject_type: "api_key"
  balance_enforcement:
    # Keep false when balance is informational and small overspend is allowed.
    enabled: false
    currency: "CNY"
    failure_mode: "open"
    request_timeout_ms: 1000
    cache_ttl_ms: 3000
    negative_cache_ttl_ms: 1000
    webhook_path: "/internal/meterry/webhook"
    webhook_secret: ""

redis:
  enabled: true
  addr: "redis://127.0.0.1:6379/0"
  key_prefix: "onr"
  access_key_mode: "redis_preferred"
  billing_stream: "meterry:events"
  billing_consumer_group: "onr-billing"
  billing_consumer_name: "onr-local"
  billing_max_attempts: 10
  access_key_hash_secret: ""
```

Inject secrets through the environment:

```bash
export ONR_ACCESS_KEY_HASH_SECRET='<stable-random-value>'
export ONR_METERRY_API_KEY='<meterry-project-api-key>'
```

The following environment variables are also supported:

```bash
export ONR_METERRY_ENABLED=true
export ONR_METERRY_BASE_URL='https://meter.example.internal'
export ONR_METERRY_PROJECT_ID='project-onr'
export ONR_METERRY_EXTRACTOR_RULE_SET_ID='ers_onr_gateway'
export ONR_METERRY_BALANCE_CURRENCY=CNY
export ONR_METERRY_BALANCE_ENABLED=false
```

Keep `ONR_ACCESS_KEY_HASH_SECRET` unchanged across restarts. A changed value
makes existing Redis Access Keys impossible to authenticate.

## Access Key Provisioning

Create Access Keys in the administrator Web UI. For Ctyun, set:

```text
Allowed providers: ctyun
Allowed models: qwen3.8-max
Provider key bindings: ctyun=primary
```

With Meterry enabled, creation provisions the account, binds the subject,
creates the wallet, and applies `initial_credit` exactly once using an
idempotency key. A failed provisioning remains pending and can be retried
without issuing another secret or another initial credit.

## Runtime Flow

1. ONR authenticates the Access Key from Redis.
2. ONR resolves the allowed public model and explicit provider-key binding.
3. ONR forwards the request to Ctyun with the server-side APP_KEY.
4. ONR extracts response usage according to the provider DSL.
5. ONR queues one idempotent Meterry event with `onr:<request-id>`.
6. The Redis billing worker retries delivery until Meterry acknowledges it.
7. User and administrator APIs query Meterry by the authenticated subject/account
   and group customer results by model.

The request path does not reserve estimated balance. Ctyun responses are
charged asynchronously, so a small overspend is possible by design.

## Verification

After starting Redis, Meterry, ONR, and the administrator Web UI:

```bash
./bin/onr config-test --config ./onr.yaml
curl -fsS http://127.0.0.1:3300/v1/models \
  -H "Authorization: Bearer <ACCESS_KEY>"
curl -fsS http://127.0.0.1:3300/v1/chat/completions \
  -H "Authorization: Bearer <ACCESS_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3.8-max","messages":[{"role":"user","content":"hello"}],"max_tokens":32}'
```

Check the following in order:

- Redis contains the active Access Key and its pending count eventually falls
  to zero.
- Meterry lists an account named `onr-access-key:<name>`.
- The wallet balance equals initial credit minus successfully ingested charges.
- Meterry usage query returns a row whose dimension is only `model` for the
  user-facing query.
- `/user` on the administrator Web listener shows the same model usage and
  balance after logging in with the Access Key.
- `/api/admin/meter/access-keys` shows the Access Key balance and model totals.

If a request succeeds but usage is absent, inspect the Redis billing pending
count, the ONR process logs, the Meterry API response, and the extractor rule
set. Do not add a second provider reconciliation charge for the same request.
