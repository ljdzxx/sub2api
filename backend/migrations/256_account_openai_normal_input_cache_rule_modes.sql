COMMENT ON COLUMN accounts.openai_normal_input_cache_rules IS
    '普通输入转写缓存规则：transfer_mode 为转写模式（percent 按比例 / exact 精确保留），transfer_percent 为转移比例，keep_tokens 为精确模式保留的普通输入 Tokens，probability_percent 为触发概率；各规则概率之和不得大于 100';
