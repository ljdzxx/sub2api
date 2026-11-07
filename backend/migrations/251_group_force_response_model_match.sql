ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS force_response_model_match BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.force_response_model_match IS
    'OpenAI 实验性策略：强制将响应 model 改写为客户端请求模型；默认关闭';
