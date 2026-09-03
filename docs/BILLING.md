# ONR Local Billing

ONR owns the meter ledger. No external billing service or website is required.
Each active Access Key maps to one `account_id`; the account stores initial
credit, actual charged usage, and the current balance.

## Configuration

```yaml
billing:
  enabled: true
  currency: "CNY"
  initial_credit: "200"

redis:
  enabled: true
  addr: "redis://127.0.0.1:6379/0"
  key_prefix: "onr-prod"
  billing_stream: "onr:billing-events"
  billing_consumer_group: "onr-billing"
  access_key_hash_secret: "${ONR_ACCESS_KEY_HASH_SECRET}"
```

Redis is the authoritative store for the configured deployment. Use durable
storage, authentication, backups, and a highly available Redis endpoint in
production. The plaintext Access Key is returned only on create/rotate and is
never stored in Redis.

`initial_credit` is granted once per account using an idempotency key. Rotating
an Access Key preserves the account and does not grant additional credit.
Amounts are stored as integer millionths of the configured currency and support
up to six decimal places.

## Request accounting

After a successful provider response, ONR reads the usage extracted by the
provider DSL, calculates the charge from the local pricing catalog, and writes
one immutable event keyed by the ONR request ID. Duplicate delivery of the same
request ID is ignored. Provider names and internal provider-key names are
available only to administrators; user reports aggregate by model.

ONR does not reserve an estimate before forwarding. Low balances are not a
hard pre-request stop, and concurrent requests may produce a small temporary
overspend. The final response usage is the charged usage.

## Access-key operations

Use the admin Web UI or `onr-admin access-key` commands to create, list,
rotate, revoke, credit, and debit keys. Credit/debit operations require a
caller-supplied idempotency key. Revocation blocks new gateway requests and
invalidates user sessions after their next session revalidation.

## Reports

The user portal accepts only the Access Key and shows its balance, request
history, token metrics, amount, and model totals. Usage supports hour, day, and
week buckets plus bounded custom ranges. The administrator portal provides the
same model-level totals across keys and can adjust balances.

## Distributed deployment

ONR instances are stateless. Put at least two instances behind a load balancer
and give them the same Redis endpoint, key prefix, hash secret, provider DSL,
model routes, pricing files, and provider secrets. Redis provides shared Access
Key records and the billing Stream; a consumer group lets another instance
reclaim pending entries after a worker stops.

Redis is still a required dependency. If Redis is unavailable, authentication
and ledger operations fail closed. A process crash before a completed response
is recorded in Redis can leave an accounting gap; monitor logs and reconcile
from provider request logs where available.

## Backup and recovery

Back up Redis persistence data and test restoration. Restore the complete
`key_prefix` namespace, including Access Key hashes, account hashes, event
records, time indexes, and idempotency records. Do not restore a stale copy over
newer live data. After recovery, verify account balances, duplicate suppression,
and user/admin report isolation before accepting traffic.
