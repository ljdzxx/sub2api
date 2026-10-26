ALTER TABLE groups ADD COLUMN IF NOT EXISTS cache_read_deduction_percent NUMERIC(5,2) NOT NULL DEFAULT 0
    CHECK (cache_read_deduction_percent >= 0 AND cache_read_deduction_percent <= 100);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_read_adjustment JSONB;
COMMENT ON COLUMN usage_logs.cache_read_adjustment IS
    'Admin-only immutable upstream/effective usage, deduction policy snapshot and upstream cost; NULL for historical/unadjusted requests';
