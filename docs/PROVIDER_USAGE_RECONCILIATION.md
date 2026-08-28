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
