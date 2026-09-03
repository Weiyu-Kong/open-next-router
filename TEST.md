# Local Billing Verification

1. Start Redis and enable `billing.enabled` in `onr.yaml`.
2. Create an Access Key in the administrator UI and record the one-time secret.
3. Call `/v1/chat/completions` with that secret and a configured model.
4. Confirm the response contains usage and that the user portal shows the
   request, model totals, charged amount, and reduced balance.
5. Repeat the same request ID in a test harness and confirm the ledger event
   and charge are present only once.
6. Create a second key and confirm its balance and reports are isolated.
7. Credit or debit a key with a unique idempotency key, then repeat the same
   adjustment and confirm it is applied once.
8. Revoke the key and confirm both gateway access and the existing user session
   are rejected after revalidation.
9. Query hour, day, week, and bounded custom ranges from the user page.
10. For a cluster, create/revoke on one instance and authenticate through a
    second instance; stop one billing worker and verify the other reclaims the
    pending Redis Stream entry.
