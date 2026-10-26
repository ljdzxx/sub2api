• ## 结论

  /v1/responses/compact 不是简单的反向代理。

  对于支持 Responses 的 OpenAI 账号，它最终会把请求发送到上游的 /v1/responses/compact，但在此之前会经过路由归一化、请求
  体裁剪、账号能力筛选、模型映射、重试/故障转移、响应转换和计费处理。

  此外，不同账号类型的实际上游路径并不完全相同。

  ## 当前处理链路

  ### 1. 路由和端点识别

  /v1/responses/*subpath 由 OpenAI 网关处理；裸路径和 Codex 别名也支持：

  - /responses/compact
  - /backend-api/codex/responses/compact

  见 backend/internal/server/routes/gateway.go:221、backend/internal/server/routes/gateway.go:361。

  端点归一化时，/responses/compact 必须先于 /responses 判断，因此被识别为独立的 EndpointResponsesCompact，不会被当作普通
  Responses 根路径，见 backend/internal/handler/endpoint.go:108。

  非法子路径会在调度前被拒绝，避免被改写成普通 /responses。

  ### 2. 请求体不是原样转发

  处理函数会把 compact 请求限制为指定字段：

  model
  input
  instructions
  tools
  parallel_tool_calls
  reasoning
  service_tier
  text
  previous_response_id

  并删除 prompt_cache_key、store、stream 等字段；没有 tools 时还会删除 parallel_tool_calls。见 backend/internal/service/
  openai_gateway_request_body.go:387。

  因此显式 /v1/responses/compact 请求会被强制按“非流式 unary compact”处理，即使客户端带了 stream:true。

  ### 3. 账号能力筛选

  Handler 将显式 compact 请求标记为 legacyCompact=true，并把 requireCompact 传给调度器，见 backend/internal/handler/
  openai_gateway_handler.go:403、backend/internal/handler/openai_gateway_handler.go:573。

  调度器按 compact 能力分层：

  - 2：明确支持
  - 1：未知，允许尝试
  - 0：明确不支持，排除

  见 backend/internal/service/openai_gateway_scheduling.go:336 和 backend/internal/service/
  openai_gateway_scheduling.go:1037。

  没有可用账号时返回：

  503 compact_not_supported
  No available accounts support /responses/compact

  ### 4. 实际上游路径

   账号/平台                                    实际行为
  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
   OpenAI OAuth / SetupToken                    ChatGPT Codex 上游，拼接 /responses/compact，并设置 Accept:
                                                application/json、隔离后的 session_id 等
  ───────────────────────────────────────────  ─────────────────────────────────────────────────────────────────────────
   OpenAI API Key，Responses 模式               自定义 Base URL 或官方 API 的 /v1/responses/compact
  ───────────────────────────────────────────  ─────────────────────────────────────────────────────────────────────────
   OpenAI API Key，Chat Completions 回退模式    进入 Responses→Chat Completions 兼容桥，实际发往 /v1/chat/completions，
                                                不是直接 compact 转发
  ───────────────────────────────────────────  ─────────────────────────────────────────────────────────────────────────
   OpenAI passthrough                           保留上游路径后缀，但仍会做 compact 模型映射、OAuth body 归一化、鉴权头
                                                处理
  ───────────────────────────────────────────  ─────────────────────────────────────────────────────────────────────────
   Grok                                         xAI 没有原生 /responses/compact；网关改造成普通 Responses 摘要请求，再
                                                把响应转换为 OpenAI compaction item，见 backend/internal/service/
                                                openai_gateway_grok.go:67 和 backend/internal/service/
                                                openai_gateway_grok_compact.go:11
  ───────────────────────────────────────────  ─────────────────────────────────────────────────────────────────────────
   Kimi/Zhipu/DeepSeek 等                       当前 compact 能力分层通常为不支持，通常会被调度器排除

  上游 URL 的 /compact 后缀由 backend/internal/service/openai_gateway_forward.go:1285 和 backend/internal/service/
  openai_gateway_request_body.go:680 拼接。

  ### 5. 模型映射和失败重试

  compact 请求优先使用账号的 compact_model_mapping；没有匹配项时使用全局 gateway.openai_compact_model，默认值是 gpt-
  5.4，见 backend/internal/service/openai_compact_fallback.go:52 和 backend/internal/config/config.go:2373。

  遇到模型不存在、不支持、上下文窗口等错误时，会在同一账号上用 fallback 模型重试一次，然后再进入账号 failover。

  ### 6. 响应也会被处理

  响应阶段包括：

  - SSE 上游响应转 JSON
  - Grok 响应转换为 OpenAI compact item
  - 恢复工具名称、namespace 和模型名
  - 显式转发 x-codex-turn-state
  - 统计 usage、TTFT、计费
  - 必要时生成 response.failed

  见 backend/internal/service/openai_gateway_response_handling.go:1558。

  需要特别区分：

  - /v1/responses/compact：当前仍是 legacy unary compact，返回 JSON
  - 普通 /v1/responses + stream:true + compaction_trigger：这是 native remote compaction v2，走普通 /responses 流式协议

  native v2 的识别见 backend/internal/handler/openai_gateway_handler.go:404。

  ## 2026 年 8 月相关提交

  审查了 main 分支 2026-08-01 至 2026-08-31 的非 merge 提交，确实有多次针对 compact 或直接影响 compact 的更新：

   日期    提交         主要内容
  ━━━━━━  ━━━━━━━━━━━  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
   8/5     2f109e74c    compact keepalive 已提交 200 头但没有语义 SSE 时，补发协议正确的 response.failed，避免客户端挂
                        起
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/7     de349187d    compact 请求体保留 service_tier，并完善 OpenAI 路由提示头
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/8     280c1c862    空的 response.completed（无 output、usage、error）不再记为 0/0 成功，而是触发 failover
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/14    9662cff2e    保留 native compaction v2 的普通 /responses 路径，不再误改成 /responses/compact
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/14    a8b9ea22b    正式拆分 native v2 与 legacy compact：只有显式 /responses/compact 使用 RequireCompact 和
                        compact 专属模型映射
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/14    3c5553e25    网关合成的 Responses/失败对象补充 created_at，修复严格 Codex 客户端反序列化失败
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/15    8219dcfc8    从 compact JSON 响应转发 x-codex-turn-state，并防止 failover 后跨账号回带旧状态
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/15    8ae6d8f67    compact 探测从已下线的 /responses/compact 改为 native v2 探测：普通 /responses +
                        compaction_trigger；同时补充 remote_compaction_v2 协商头
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/19    acce29af2    增加 compact 专属模型映射和全局 fallback 模型机制
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/20    17c0ee385    Grok compaction 的 422 响应允许重试
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/20    b2b2adcf8    收紧模型不可用错误识别，规范 fallback 错误响应并补充运维记录
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/21    7e9af4c10    compact fallback 从递归重试改为循环，避免重置“一次性重试”状态
  ──────  ───────────  ─────────────────────────────────────────────────────────────────────────────────────────────────
   8/28    81ac8ccd6    非流式请求收到 HTTP 200 的 response.failed/SSE 终止事件时，也按流式逻辑分类并触发 failover

  ## 最重要的架构变化

  8 月最大的变化不是简单修补 /compact，而是把两种协议明确分开：

  1. 旧的 /v1/responses/compact：legacy unary 接口，仍保留兼容转发、模型映射和 fallback。
  2. 新的 remote compaction v2：普通 /v1/responses 流式请求，通过 compaction_trigger 触发，不再改写为 /compact。

  同时，代码注释明确指出 OpenAI 官方已经下线旧的 unary /responses/compact，compact 探测因此改为 native v2。也就是说，当
  前 /v1/responses/compact 更像是面向旧客户端、兼容网关和 Grok 模拟实现的兼容层，而不是 OpenAI 官方推荐的新协议路径。