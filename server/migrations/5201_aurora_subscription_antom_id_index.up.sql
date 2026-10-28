-- GetAuroraSubscriptionByAntomID resolves a notification to its row, so one
-- Antom subscription may address at most one row. Partial because Stripe rows
-- and pending intents leave the column NULL. Own file because
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS aurora_subscription_antom_id_key
    ON aurora_subscription (antom_subscription_id)
    WHERE antom_subscription_id IS NOT NULL;
