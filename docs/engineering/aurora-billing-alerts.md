# Aurora billing alerts

Operational alerting for the payable-subscription launch (issue #55 / T11).
This repository ships the metrics and the rules below; it does **not** ship an
alerting stack. Until ops add these rules to the deployment's Prometheus and
route them to an on-call receiver, nothing fires.

## Metric families

All three are registered by `NewBusinessMetrics()` in
`server/internal/metrics/business.go`, but they are served on the **optional**
metrics listener, not on the API port. The main API router answers 404 for
`/metrics` (see `server/cmd/server/metrics_test.go`), and `METRICS_ADDR`
defaults to empty, meaning no listener starts at all
(`server/internal/metrics/config.go`; `docker-compose.selfhost.yml` passes
`${METRICS_ADDR:-}`).

With the listener off, nothing exports these families: every rule below is
evaluated against an empty vector and stays **silent — no alert and no error**.
Enabling the listener is therefore part of this ticket, not an optional extra.
Counters reset when the process restarts; `increase()` and `rate()` handle
that. The webhook and settlement counters are prewarmed to zero, so an enabled
listener shows those series before the first event; the shortfall counter has
no prewarm and appears on the first 402.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `multica_aurora_billing_webhook_events_total` | `event`, `result` | One increment per Stripe webhook request. |
| `multica_aurora_billing_settlement_runs_total` | `result` | One increment per monthly settlement run. |
| `multica_aurora_billing_credit_shortfalls_total` | none | One increment per generation create answered 402. |

`result` on the webhook counter is a closed set of the branches in
`Handler.StripeWebhook` (`server/internal/handler/aurora.go`):

| `result` | HTTP | Cause |
| --- | --- | --- |
| `ok` | 200 | Event verified and accepted: the ledger write landed, or the handler intentionally did not act on it (an unconsumed event type, malformed metadata it acks rather than retries, or an unpaid checkout session). |
| `handler_error` | 500 | `handleStripeEvent` failed; Stripe will retry. This is the failure signal. |
| `not_configured` | 503 | `Payments` is nil — `STRIPE_SECRET_KEY` or `STRIPE_WEBHOOK_SECRET` is unset. |
| `invalid_signature` | 401/400 | Missing `Stripe-Signature`, or `ConstructEvent` rejected the body. |
| `bad_request` | 400/413 | Body unreadable or above the size cap. |
| `rate_limited` | 429 | Per-IP webhook ingress limiter refused the sender. |
| `unknown` | — | An outcome no call site classified. Must stay at zero. |

`event` is the Stripe event type from the verified payload
(`checkout.session.completed`, `checkout.session.async_payment_succeeded`,
`customer.subscription.updated`, `customer.subscription.deleted`). A verified
but unconsumed type folds onto `other`; a request rejected before verification
carries `unknown`. The label is never an event id, user, or workspace.

Settlement `result` is `ok` or `failed`; a failed run means paying users were
not granted and the next tick retries (see
`server/cmd/server/aurora_settlement.go`). A run aborted by a cancelled context
(graceful shutdown) is logged but not recorded as failed, so a restart does not
page before the next tick. The 402 counter has no labels on
purpose: it is a rate, not an attribution. A spike is diagnosed from the
generation rows the refusal terminalizes, not from a log line: the
`ErrInsufficientCredits` branch calls `markGenerationFailed` with reason
`insufficient credits`, which lands in the database as
`aurora_generation.status = 'failed'` with
`aurora_generation.error = 'insufficient credits'`
(`server/internal/handler/aurora.go`). This path writes
no log line unless the wallet read itself fails (`failed to load aurora balance
after insufficient credits`), so grepping the logs for `insufficient credits`
is not a reliable way to find the peak.

## Rules

Copy the group into the deployment's Prometheus rule files.

```yaml
groups:
  - name: aurora-billing
    rules:
      # The webhook is failing with a 5xx. The handler's only 5xx branches are
      # handler_error (500) and not_configured (503); Stripe retries both, so a
      # non-zero rate means grants are not landing.
      - alert: AuroraBillingWebhook5xx
        expr: sum(increase(multica_aurora_billing_webhook_events_total{result=~"handler_error|not_configured"}[10m])) > 0
        for: 0m
        labels:
          severity: page
        annotations:
          summary: Aurora billing webhook is answering 5xx
          description: >-
            {{ $value }} failed webhook request(s) in the last 10 minutes.
            not_configured means the Stripe keys are unset; handler_error means
            the event reached the ledger code and failed. Stripe retries, so
            check server logs for "stripe webhook handling failed".

      # A sustained share of requests the webhook rejects without a 5xx. A
      # wrong webhook secret makes every request invalid_signature; a scanner
      # hitting the public route makes it a trickle.
      - alert: AuroraBillingWebhookRejections
        expr: >-
          sum(rate(multica_aurora_billing_webhook_events_total{result=~"invalid_signature|bad_request|rate_limited|unknown"}[5m]))
          /
          sum(rate(multica_aurora_billing_webhook_events_total[5m])) > 0.2
        for: 15m
        labels:
          severity: ticket
        annotations:
          summary: More than 20% of Aurora webhook requests are rejected
          description: >-
            Sustained for 15 minutes. invalid_signature points at a secret
            mismatch between Stripe and STRIPE_WEBHOOK_SECRET; unknown points at
            a call site that was added without classifying its result.

      # A settlement run failed. One run covers every subscription, so a single
      # failure is a whole month of grants missing until the next tick.
      - alert: AuroraBillingSettlementFailed
        expr: sum(increase(multica_aurora_billing_settlement_runs_total{result="failed"}[25h])) > 0
        for: 0m
        labels:
          severity: page
        annotations:
          summary: Aurora monthly settlement failed
          description: >-
            {{ $value }} failed run(s) in the last 25 hours (the loop runs
            daily). The next tick retries; check server logs for
            "aurora monthly settlement failed".

      # No settlement run at all in a settlement interval plus slack: the loop
      # is hung, which no error log will announce.
      - alert: AuroraBillingSettlementMissing
        expr: sum(increase(multica_aurora_billing_settlement_runs_total[25h])) == 0
        for: 30m
        labels:
          severity: page
        annotations:
          summary: Aurora monthly settlement has not run in 25 hours
          description: >-
            The loop runs once at startup and then daily. No run in 25 hours
            means it is stuck; a healthy process reports at least its startup
            run.

      # 402 refusals per minute. A trickle is normal (users with an empty
      # wallet); a spike means grants, pricing, or expiry moved incorrectly.
      # The threshold is a starting point, tune it against launch traffic.
      - alert: AuroraBillingCreditShortfallRate
        expr: sum(rate(multica_aurora_billing_credit_shortfalls_total[5m])) * 60 > 5
        for: 15m
        labels:
          severity: ticket
        annotations:
          summary: Aurora is refusing more than 5 generation creates per minute for short credits
          description: >-
            Sustained for 15 minutes. Check whether the failed settlement alert
            is also firing, then the free-tier grant and the tier grants. The
            counter has no per-user label by design.
```

## Prerequisites: enable the metrics listener first

No rule above can fire until the metrics listener is running. `METRICS_ADDR` is
empty by default, so no listener starts, `/metrics` on the API port answers 404,
and every rule evaluates against an empty vector and stays silent (no alert, no
error). Enabling it is part of this ticket:

1. Set `METRICS_ADDR` to a concrete `host:port`. For a bare process that is
   loopback, e.g. `METRICS_ADDR=127.0.0.1:9090`. Inside a container bind
   `0.0.0.0:9090` and publish it only to a trusted network — for example a
   host-local mapping `127.0.0.1:9090:9090`. The self-host Compose stack
   publishes only the API port, so add that mapping; `METRICS_ADDR` alone is not
   enough for a host-side scrape.
2. Redeploy or restart the API process, and confirm the startup log line
   `metrics server starting` with the address you set.
3. Point the deployment's Prometheus scrape config at that same address. The
   main API port is not the scrape target.

## Operational notes

- Scrape the metrics listener; every rule uses `sum(...)` so it is correct
  across replicas.
- Alertmanager routing, silences, and severity-to-channel mapping are ops
  concerns and are not configured here.
- The acceptance line "an alert fires when the webhook is forced to fail" is
  verified **only after** the listener is enabled and these rules are wired.
  This change ships the metrics and the rule set; no alert has been observed.
- To verify the path end-to-end on staging: scrape the listener address from
  the prerequisite step,
  `curl -s http://127.0.0.1:9090/metrics | grep multica_aurora_billing`, and
  confirm the webhook and settlement families are present (including their
  zero-valued series); the shortfall counter appears after the first 402. Then
  force a failure: unsetting
  `STRIPE_SECRET_KEY`/`STRIPE_WEBHOOK_SECRET` and posting a test event answers
  503, increments `result="not_configured"`, and fires
  `AuroraBillingWebhook5xx`. A database outage during delivery forces
  `result="handler_error"` through the same rule. Do not unset the keys in
  production: that takes payments down.
