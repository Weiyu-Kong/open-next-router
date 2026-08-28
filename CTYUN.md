# Ctyun Provider

ONR integrates Ctyun as an OpenAI-compatible upstream. The client sends an
ONR Access Key to `/v1/chat/completions`; ONR keeps all Ctyun credentials and
upstream URLs server-side.

## Server Configuration

Add a `ctyun` provider key to the private `keys.yaml`:

```yaml
providers:
  ctyun:
    keys:
      - name: primary
        value: ""
        ctyun_access_key: ""
        ctyun_secure_key: ""
        ctyun_user_id: ""
```

Environment injection is supported for a key named `primary`:

- `ONR_UPSTREAM_KEY_CTYUN_PRIMARY`: inference APP_KEY
- `ONR_CTYUN_PRIMARY_ACCESS_KEY`: EOP monitor access key
- `ONR_CTYUN_PRIMARY_SECURE_KEY`: EOP monitor secure key
- `ONR_CTYUN_PRIMARY_USER_ID`: Ctyun monitor user ID

Bind an Access Key to the internal key in the Redis record or file record:

```yaml
provider_key_bindings:
  ctyun: primary
```

The binding is an exact key-name lookup. Missing bindings retain the existing
provider round-robin behavior for compatibility; no provider fallback or
automatic replacement is performed.

## Authentication and Usage

Inference requests use `Authorization: Bearer <APP_KEY>` and the configured
OpenAI-compatible Ctyun base URL. Monitor APIs use EOP signing with
`ctyun-eop-request-id`, `eop-date`, and `Eop-Authorization`. The signing helper
is in `onr-core/pkg/ctyun` and signs the exact request body bytes.

Ctyun monitor reports are aggregate time buckets. `report/call` supplies call
counts and `report/tokensUsage` supplies input/output/total tokens. These
aggregates are suitable for reconciliation and reporting, but must not be
converted into synthetic per-request charges because ONR already records
direct response usage for the same calls.

## Models

`config/providers/ctyun.conf` contains explicit public model to Ctyun model-ID
mappings. Add the public model to `models.yaml` with provider `ctyun` before
exposing it through `/v1/models`. The provider currently supports chat
completions, including streaming usage through the final usage chunk.
