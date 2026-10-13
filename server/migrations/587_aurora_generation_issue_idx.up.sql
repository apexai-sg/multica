-- GetAuroraGenerationByIssueID. No foreign key. Own file because
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS aurora_generation_issue_idx
    ON aurora_generation (issue_id)
    WHERE issue_id IS NOT NULL;
