ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS transfer_cache_write_to_input BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.transfer_cache_write_to_input IS
    'OpenAI 实验性策略：将上游 usage 的写缓存 Tokens 转移至普通输入；默认关闭';
