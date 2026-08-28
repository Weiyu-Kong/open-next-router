# Provider Usage Reconciliation

Provider usage collection is separate from gateway protocol transformation.
It must use an explicit `usageadapter.Adapter`; proxy code must not infer a
usage endpoint or response format from a provider name, request path, or model.

## OpenRouter Generation Details

`usageadapter.OpenRouterGenerationAdapter` supports OpenRouter's generation
detail endpoint:

```text
GET https://openrouter.ai/api/v1/generation?id=<generation-id>
Authorization: Bearer <server-owned-api-key>
```

It normalizes prompt, completion, native, and reasoning token counts plus the
provider-reported total cost. Credentials are supplied by server code and are
never accepted from a customer request.

The endpoint is a per-generation lookup, not a time-range listing API. The
adapter therefore requires upstream generation IDs captured by ONR and accepts
at most 100 IDs per call. It filters returned records to the caller's bounded
time window. There is no cursor or pagination behavior for this adapter.

Runtime candidate persistence, polling schedules, request correlation, and
direct-versus-provider no-double-charge decisions are separate requirements.
The presence of this adapter alone does not enable automatic reconciliation.

## Runtime Configuration

Provider usage reconciliation is opt-in and requires both Meterry and Redis:

```yaml
provider_usage:
  enabled: true
  poll_interval_ms: 60000
  candidate_lease_ms: 30000
  openrouter:
    enabled: true
    mode: authoritative
    endpoint: "https://openrouter.ai/api/v1/generation"
    request_timeout_ms: 5000
    internal_key_ids: ["openrouter-primary"]
```

Set the OpenRouter management credential only through:

```text
ONR_OPENROUTER_USAGE_API_KEY=<server-owned-credential>
```

`provider_usage.openrouter.api_key` is deliberately rejected in YAML. Candidate
records contain the opaque `internal_key_id` mapping but never the provider
credential. The current validation accepts only `authoritative` mode; direct
and correction modes are not silently substituted.

## Durable Candidate State

The Redis control plane stores one stable candidate for each provider,
internal-key ID, and upstream request ID. The record includes its original ONR
request ID, Access Key/account/subject scope, API/model metadata, occurrence
time, retry count, sanitized error code, next-attempt time, and lease owner.

Candidate operations provide atomic enqueue, due-candidate claim, owned retry,
owned acknowledgement, and expired-lease recovery. Re-enqueueing the same
provider record does not reset its retry state or active lease. Error state is
limited to a short machine-readable code so provider responses, URLs, headers,
and credentials cannot be persisted accidentally.

Automatic candidate creation and polling are Stage 6C. Until that worker is
enabled, this storage contract alone does not suppress direct response billing
or produce provider-authoritative charges.
