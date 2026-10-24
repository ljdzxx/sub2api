# ray 分支：仅故障兜底账号

此功能属于 `ray` 分支，默认关闭；未配置的账号维持上游行为。

## 使用

在账号管理中添加或编辑 **OpenAI / API Key** 账号，勾选「仅故障兜底账号」。
账号仍需处于可调度状态，并加入对应分组、支持请求模型和接口。

标记保存在现有 `accounts.extra.openai_fault_fallback_only` 布尔字段中，无需数据库迁移。
管理 API 可以通过 `extra: {"openai_fault_fallback_only": true}` 启用，通过显式 `false` 关闭。
更新中省略该字段会保留原值，避免旧客户端编辑账号时意外解除限制。

## 调度规则

- 普通首选排除兜底账号，不受优先级、负载、会话绑定、`previous_response_id` 或实验调度评分影响。
- 仅当前请求已经发生允许重放的上游 **429 或 500–599** 错误时，才允许兜底账号进入重选池。
  传输失败、部分流首包失败等由现有上游错误处理归类为 502/5xx 后也可触发。
- 不将客户端看到的所有 502 都视为换号凭据：图片和 Embeddings 原有网络错误路径直接写出 502，不进入重选；收到上游 HTTP 429/5xx 时才可兜底。文本接口保留原有网络故障换号能力。
- 本地凭证获取失败、普通 400/401/403/413、并发满、排队、利润门否决和没有普通候选都不会单独解锁兜底。
- 兜底仍受分组、模型能力、配额、并发、冷却、原有重试次数和响应已输出保护约束。
- 解锁后普通候选和兜底候选按原有调度规则竞争；不保证立即跳到兜底，也不要求先耗尽所有普通账号。
- 每个新 HTTP 请求重新从普通账号开始，之前留下的粘性绑定不能绕过限制。
- WebSocket 可在允许重放的故障后选用兜底账号；其后下一轮会以 1013 要求重连，防止长连接让新请求直接复用兜底。
  客户端需要处理重连；仅携带服务端 `previous_response_id`、没有完整上下文的续接请求，切换账号后可能无法恢复上下文。
- 管理员主动测试、探测账号不属于普通请求调度，不受此开关限制。

## 与上游同步

远程仓库约定：

- `origin`: https://github.com/ljdzxx/sub2api.git
- `upstream`: https://github.com/Wei-Shaw/sub2api.git
- 部署分支：`ray`

先提交或暂存自己的工作，再同步：

```sh
git fetch upstream
git switch ray
git merge upstream/main
```

解决冲突并验证后，再自行推送和部署。不要使用上游预构建镜像替代本分支构建产物，否则专有功能不会包含在部署中。

## 维护接入点

策略集中在 `backend/internal/service/openai_fault_fallback.go`；上游文件只保留少量接入：

1. HTTP/WS handler 在选号时将最后一次已接受的故障传入 `WithOpenAIFaultFallback`。
2. 普通和实验调度共用策略判断；previous-response 快捷路径在获取并发槽位前也检查。
3. 调度快照 `filterSchedulerExtra` 保留标记，数据库复核继续执行准入判断。
4. 账号创建/编辑验证布尔值与平台类型，更新保留未显式修改的标记。
5. 前端独立字段组件接入创建/编辑表单；启用和关闭都写入明确的布尔值。

上游新增选号入口或 WebSocket 轮次复用逻辑时，应检查是否需要接入本策略。不要用排除账号数量或换号次数代替上游故障凭据。

验证命令：

```sh
cd backend
go test -tags=unit ./internal/service ./internal/repository ./internal/handler -run 'TestOpenAIFaultFallback_|TestSchedulerExtraPreservesFaultFallbackOnly' -count=1
cd ../frontend
pnpm exec vitest run src/components/account/__tests__/FaultFallbackOnlyField.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm run typecheck
```

合并上游不能保证零冲突；应同时检查关键行为测试，避免无文本冲突的语义回归。
