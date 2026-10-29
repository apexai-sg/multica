ALTER TABLE IF EXISTS aurora_subscription
    DROP COLUMN IF EXISTS antom_subscription_id,
    DROP COLUMN IF EXISTS payment_provider;
