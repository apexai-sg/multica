-- Binds an Aurora generation to the issue its agent runs on. No foreign key by
-- house rule; the relationship is validated in application code. The partial
-- index on issue_id lives in 587 because CREATE INDEX CONCURRENTLY cannot share
-- a migration file with other DDL.
ALTER TABLE aurora_generation
    ADD COLUMN IF NOT EXISTS issue_id UUID;
