-- Antom (Ant International Ams) is the second subscription payment channel
-- beside Stripe. payment_provider records the channel the pending checkout was
-- started on; existing rows are Stripe rows and keep that as their default.
-- antom_subscription_id holds the Antom subscription the notifications address.
-- stripe_event_created_at keeps its name and its Stripe-only ordering meaning.
ALTER TABLE IF EXISTS aurora_subscription
    ADD COLUMN IF NOT EXISTS payment_provider TEXT NOT NULL DEFAULT 'stripe',
    ADD COLUMN IF NOT EXISTS antom_subscription_id TEXT;
