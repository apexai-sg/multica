-- name: StartAuroraSubscriptionCheckout :one
-- Persist the checkout intent before calling the payment channel. Reusing the
-- stored key makes retries and simultaneous tabs converge on one hosted
-- Checkout Session. payment_provider stamps the channel the intent belongs to.
INSERT INTO aurora_subscription (
    user_id, tier, status, stripe_customer_id, stripe_subscription_id,
    current_period_end, cancel_at_period_end, checkout_idempotency_key,
    checkout_billing_cycle, stripe_event_created_at, payment_provider
)
VALUES (
    sqlc.arg(user_id), sqlc.arg(tier), 'pending', NULL, NULL,
    NULL, false, sqlc.arg(checkout_idempotency_key),
    sqlc.arg(checkout_billing_cycle), 0, sqlc.arg(payment_provider)
)
ON CONFLICT (user_id) DO UPDATE SET
    tier = EXCLUDED.tier,
    status = 'pending',
    stripe_customer_id = NULL,
    stripe_subscription_id = NULL,
    -- A new intent belongs to no Antom subscription yet, so a late
    -- notification for the previous one can no longer match this row.
    antom_subscription_id = NULL,
    current_period_end = NULL,
    cancel_at_period_end = false,
    checkout_idempotency_key = EXCLUDED.checkout_idempotency_key,
    checkout_billing_cycle = EXCLUDED.checkout_billing_cycle,
    stripe_event_created_at = 0,
    payment_provider = EXCLUDED.payment_provider,
    updated_at = now()
RETURNING id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
          current_period_end, cancel_at_period_end, created_at, updated_at,
          checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
          payment_provider, antom_subscription_id;

-- name: ExpireAuroraSubscriptionCheckoutIntent :execrows
-- Mark a failed checkout's intent as replaceable without discarding its key, so
-- a late notification for it still finds the row. Guarded on the exact key, the
-- pending state and the updated_at this request left behind: a concurrent
-- request that reused the intent touches it and must not be expired.
UPDATE aurora_subscription
SET updated_at = sqlc.arg(expired_at)
WHERE user_id = sqlc.arg(user_id)
  AND status = 'pending'
  AND checkout_idempotency_key = sqlc.arg(checkout_idempotency_key)
  AND updated_at = sqlc.arg(intent_updated_at);

-- name: MarkAuroraSubscriptionCheckoutIntentUsed :execrows
-- Record that this intent produced a checkout URL. That is the one fact that
-- keeps it alive: a failing request may not expire an intent whose URL the
-- browser already has. Reusing an intent deliberately does not call this, so a
-- stream of failed attempts cannot keep a row fresh forever — freshness still
-- ages out from the moment a URL was last handed out.
UPDATE aurora_subscription
SET updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND status = 'pending'
  AND checkout_idempotency_key = sqlc.arg(checkout_idempotency_key);

-- name: UpsertAuroraSubscription :one
-- Apply only a non-stale Stripe event. Event timestamps have one-second
-- precision, so a canceled subscription also wins ties: that Stripe
-- subscription cannot become active again, and a late same-second update must
-- not resurrect it.
INSERT INTO aurora_subscription (
    user_id, tier, status, stripe_customer_id, stripe_subscription_id,
    current_period_end, cancel_at_period_end, stripe_event_created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id) DO UPDATE SET
    tier = EXCLUDED.tier,
    status = EXCLUDED.status,
    stripe_customer_id = COALESCE(EXCLUDED.stripe_customer_id, aurora_subscription.stripe_customer_id),
    stripe_subscription_id = COALESCE(EXCLUDED.stripe_subscription_id, aurora_subscription.stripe_subscription_id),
    current_period_end = EXCLUDED.current_period_end,
    cancel_at_period_end = EXCLUDED.cancel_at_period_end,
    checkout_idempotency_key = NULL,
    checkout_billing_cycle = NULL,
    stripe_event_created_at = EXCLUDED.stripe_event_created_at,
    updated_at = now()
WHERE aurora_subscription.stripe_event_created_at < EXCLUDED.stripe_event_created_at
   OR (
       aurora_subscription.stripe_event_created_at = EXCLUDED.stripe_event_created_at
       AND NOT (
           aurora_subscription.status = 'canceled'
           AND EXCLUDED.status <> 'canceled'
       )
   )
RETURNING id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
          current_period_end, cancel_at_period_end, created_at, updated_at,
          checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
          payment_provider, antom_subscription_id;

-- name: UpsertAuroraAntomSubscription :one
-- Antom notifications carry no ordering timestamp, so ordering comes from the
-- row's terminal state instead: a canceled row addressed by the same Antom
-- subscription is never rewritten, while a different antom_subscription_id is a
-- resubscription and may overwrite it. When the guard declines the write the
-- statement returns no row, which callers read as pgx.ErrNoRows: nothing was
-- applied, and retrying the same notification will not change that.
-- payment_provider is stamped at checkout and is deliberately not written here;
-- the checkout intent key is kept so the returning browser can still resolve its
-- own checkout.
--
-- The INSERT branch is a safety net rather than a path to rely on: the checkout
-- row is expected to exist first, and a row created here would carry the
-- column default 'stripe'.
INSERT INTO aurora_subscription (
    user_id, tier, status, antom_subscription_id, current_period_end,
    cancel_at_period_end
)
VALUES (
    sqlc.arg(user_id), sqlc.arg(tier), sqlc.arg(status),
    sqlc.arg(antom_subscription_id), sqlc.arg(current_period_end),
    sqlc.arg(cancel_at_period_end)
)
ON CONFLICT (user_id) DO UPDATE SET
    tier = EXCLUDED.tier,
    status = EXCLUDED.status,
    antom_subscription_id = EXCLUDED.antom_subscription_id,
    current_period_end = EXCLUDED.current_period_end,
    cancel_at_period_end = EXCLUDED.cancel_at_period_end,
    updated_at = now()
WHERE NOT (
    aurora_subscription.status = 'canceled'
    AND aurora_subscription.antom_subscription_id IS NOT DISTINCT FROM EXCLUDED.antom_subscription_id
)
RETURNING id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
          current_period_end, cancel_at_period_end, created_at, updated_at,
          checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
          payment_provider, antom_subscription_id;

-- name: GetAuroraSubscriptionByUser :one
SELECT id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
       current_period_end, cancel_at_period_end, created_at, updated_at,
       checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
       payment_provider, antom_subscription_id
FROM aurora_subscription
WHERE user_id = $1;

-- name: GetAuroraSubscriptionByStripeID :one
SELECT id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
       current_period_end, cancel_at_period_end, created_at, updated_at,
       checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
       payment_provider, antom_subscription_id
FROM aurora_subscription
WHERE stripe_subscription_id = $1;

-- name: GetAuroraSubscriptionByAntomID :one
SELECT id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
       current_period_end, cancel_at_period_end, created_at, updated_at,
       checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
       payment_provider, antom_subscription_id
FROM aurora_subscription
WHERE antom_subscription_id = $1;

-- name: GetAuroraSubscriptionByCheckoutKey :one
SELECT id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
       current_period_end, cancel_at_period_end, created_at, updated_at,
       checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
       payment_provider, antom_subscription_id
FROM aurora_subscription
WHERE checkout_idempotency_key = $1;

-- name: MarkAuroraSubscriptionCancelAtPeriodEnd :one
-- Guarded so a termination that lands between the caller's read and this write
-- is never overwritten: a canceled row has nothing left to stop, and flipping
-- the flag there would render "cancellation scheduled" for an ended plan. A
-- declined write returns no row, which callers read as pgx.ErrNoRows.
UPDATE aurora_subscription
SET cancel_at_period_end = true, updated_at = now()
WHERE user_id = $1
  AND payment_provider = 'antom'
  AND status <> 'canceled'
RETURNING id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
          current_period_end, cancel_at_period_end, created_at, updated_at,
          checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
          payment_provider, antom_subscription_id;

-- name: ListActiveSubscriptionsForGrant :many
SELECT id, user_id, tier, status, stripe_customer_id, stripe_subscription_id,
       current_period_end, cancel_at_period_end, created_at, updated_at,
       checkout_idempotency_key, checkout_billing_cycle, stripe_event_created_at,
       payment_provider, antom_subscription_id
FROM aurora_subscription
WHERE status = 'active' AND current_period_end > now()
ORDER BY user_id;
