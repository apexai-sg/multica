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
| `multica_aurora_billing_webhook_events_total` | `event`, `result` | One increment per billing webhook request: a Stripe event or an Antom notification. |
| `multica_aurora_billing_settlement_runs_total` | `result` | One increment per monthly settlement run. |
| `multica_aurora_billing_credit_shortfalls_total` | none | One increment per generation create answered 402. |

`result` on the webhook counter is a closed set of the branches in
`Handler.StripeWebhook` and the two Antom notification handlers
(`server/internal/handler/aurora.go`). The HTTP codes below are the Stripe
route's; the Antom routes answer 401 for `invalid_signature`, 400/413 for
`bad_request`, 429 for `rate_limited`, 503 for `not_configured` and 500 for
`handler_error`.

| `result` | HTTP | Cause |
| --- | --- | --- |
| `ok` | 200 | Event verified and accepted: the ledger write landed, or the handler intentionally did not act on it (an unconsumed event type, malformed metadata it acks rather than retries, an unpaid checkout session, or an Antom notification acked without a change — an unknown subscription, an unhandled notification type, a non-payment payload on the charge route, or a write the terminal-state guard declined). |
| `handler_error` | 500 | `handleStripeEvent`, or an Antom notification's lookup, write or grant, failed — including a paid notification whose period is missing or not after its start; the channel will retry. This is the failure signal. |
| `not_configured` | 503 | The channel is unconfigured: `Payments` is nil (`STRIPE_SECRET_KEY` or `STRIPE_WEBHOOK_SECRET` unset) on the Stripe route, `Antom` is nil on the Antom routes (its six `ANTOM_*` variables or `MULTICA_PUBLIC_URL` missing). |
| `invalid_signature` | 401/400 | Missing or unparsable signature header: `Stripe-Signature` absent or rejected by `ConstructEvent`, or an Antom notification whose `client-id`, `request-time` and `signature` headers do not verify. |
| `bad_request` | 400/413 | Body unreadable or above the size cap. |
| `rate_limited` | 429 | Per-IP webhook ingress limiter refused the sender. |
| `unknown` | — | An Antom notification acked without granting its allowance: an unknown subscription, an unhandled notification type, or a charge applied to a row whose tier the catalog does not know — the paid-but-ungranted case, whose only signal is `antom subscription allowance skipped for an unknown tier` in the server log. On the Stripe event labels only: an outcome no call site classified, which must stay at zero. |

`event` is the Stripe event type from the verified payload
(`checkout.session.completed`, `checkout.session.async_payment_succeeded`,
`customer.subscription.updated`, `customer.subscription.deleted`), or the Antom
notification route (`antom_payment_notify`, `antom_subscription_notify`). A
verified but unconsumed type folds onto `other`; a request rejected before
verification carries `unknown`. The label is never an event id, user, or
workspace.

Settlement `result` is `ok` or `failed`; a failed run means paying users were
not granted and the next tick retries (see
`server/cmd/server/aurora_settlement.go`). A run aborted by a cancelled context
(graceful shutdown) is logged but not recorded as failed, so a restart does not
page before the next tick. This rule only sees a run that returned an error:
`RunMonthlySettlement` logs and skips per-user grant failures — a user with no
personal workspace, an `EnsureMonthlyAllowance` error, or an unknown or
zero-credit tier — and still returns nil, so those users count as `ok` and never
page. To notice them, watch the server log for `aurora monthly grant skipped for
user` and `aurora monthly grant skipped for unknown tier`. The 402 counter has
no labels on purpose: it is a rate, not an attribution. A spike is diagnosed
from the generation rows the refusal terminalizes, not from a log line: the
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
      # The webhook is failing with a 5xx. The handlers' only 5xx branches are
      # handler_error (500) and not_configured (503); the channel retries both,
      # so a non-zero rate means grants are not landing.
      - alert: AuroraBillingWebhook5xx
        expr: sum(increase(multica_aurora_billing_webhook_events_total{result=~"handler_error|not_configured"}[10m])) > 0
        for: 0m
        labels:
          severity: page
        annotations:
          summary: Aurora billing webhook is answering 5xx
          description: >-
            {{ $value }} failed webhook request(s) in the last 10 minutes, on
            the event label that names the channel. not_configured means that
            channel has no credentials: the Stripe keys on a Stripe event, the
            ANTOM_* variables and MULTICA_PUBLIC_URL on an antom_*_notify
            event. handler_error means the request reached the billing code and
            failed; both channels retry, so check the server log for
            "stripe webhook handling failed" or "antom ... failed" with the
            subscription and payment ids.

      # A sustained share of requests the webhook rejects without a 5xx. A
      # wrong webhook secret makes every request invalid_signature; a scanner
      # hitting the public route makes it a trickle. `unknown` is counted for
      # the Stripe event labels only. The Antom handlers use it for
      # notifications they ack without granting, which is mostly benign — the
      # exception is a paid charge on a row whose tier the catalog no longer
      # knows, and that case is visible only in the server log
      # ("antom subscription allowance skipped for an unknown tier").
      # Both sums are prewarmed, so the numerator is never an empty vector.
      - alert: AuroraBillingWebhookRejections
        expr: >-
          (
            sum(rate(multica_aurora_billing_webhook_events_total{result=~"invalid_signature|bad_request|rate_limited"}[5m]))
            +
            sum(rate(multica_aurora_billing_webhook_events_total{result="unknown",event!~"antom_payment_notify|antom_subscription_notify"}[5m]))
          )
          /
          sum(rate(multica_aurora_billing_webhook_events_total[5m])) > 0.2
        for: 15m
        labels:
          severity: ticket
        annotations:
          summary: More than 20% of Aurora webhook requests are rejected
          description: >-
            Sustained for 15 minutes. invalid_signature points at a secret
            mismatch — STRIPE_WEBHOOK_SECRET on the Stripe route, the Antom
            public key or key version on the Antom routes; unknown on a Stripe
            event label points at a call site that was added without
            classifying its result.

      # A settlement run failed. One run covers every subscription, so a single
      # failure is a whole month of grants missing until the next tick. Per-user
      # grant skips return nil and land in result="ok", so this rule cannot see
      # them; see the settlement caveat above.
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
