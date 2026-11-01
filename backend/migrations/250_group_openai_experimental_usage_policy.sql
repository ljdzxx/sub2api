ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS openai_experimental_features_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS cache_read_amplification_percent NUMERIC(5,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_read_amplification_min_input_tokens BIGINT NOT NULL DEFAULT 0;

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_cache_read_amplification_percent_check,
    ADD CONSTRAINT groups_cache_read_amplification_percent_check
        CHECK (cache_read_amplification_percent >= 0 AND cache_read_amplification_percent <= 100),
    DROP CONSTRAINT IF EXISTS groups_cache_read_amplification_min_input_tokens_check,
    ADD CONSTRAINT groups_cache_read_amplification_min_input_tokens_check
        CHECK (cache_read_amplification_min_input_tokens >= 0);

COMMENT ON COLUMN groups.openai_experimental_features_enabled IS
    '是否启用 OpenAI 实验性功能策略；默认关闭';
COMMENT ON COLUMN groups.cache_read_amplification_percent IS
    '按原始总输入百分比增加读缓存 Tokens';
COMMENT ON COLUMN groups.cache_read_amplification_min_input_tokens IS
    '读缓存放大生效的最小原始总输入 Tokens';
