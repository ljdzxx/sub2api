ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS openai_experimental_features_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS openai_normal_input_cache_rules JSONB NOT NULL DEFAULT '[]'::jsonb;

COMMENT ON COLUMN accounts.openai_experimental_features_enabled IS
    '是否启用账号级 OpenAI 实验性功能策略；仅 OpenAI API Key 账号可用';
COMMENT ON COLUMN accounts.openai_normal_input_cache_rules IS
    '普通输入转写缓存规则：transfer_percent 为转移比例，probability_percent 为触发概率';
