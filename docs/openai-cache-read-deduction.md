# OpenAI 实验性缓存用量策略

分组管理的“OpenAI 实验性功能策略”总开关对应 `openai_experimental_features_enabled`，默认关闭。只有总开关启用后，下方子策略才可生效；子策略的百分比为 0 时各自不生效。非 OpenAI 分组会将总开关和相关参数归零。

## 账号级普通输入转写缓存

OpenAI API Key 上游账号有独立的 `openai_experimental_features_enabled` 开关和 `openai_normal_input_cache_rules` 规则，不依赖分组总开关。每条规则包含两个不同的百分比：

- `transfer_percent`：抽中后，将多少比例的普通输入 Tokens 转入写缓存，整数 0–100。
- `probability_percent`：本条规则被抽中的概率，整数 1–100；所有规则的概率之和必须等于 100。每次只选一条规则，不是依次独立触发。

例如“90% 的请求转移一半普通输入，10% 不转移”应配置：

```json
[
  {"transfer_percent": 50, "probability_percent": 90},
  {"transfer_percent": 0, "probability_percent": 10}
]
```

实现使用 `math/rand/v2.IntN(100)`，没有为账号、会话或请求设置固定种子。每个 HTTP 请求独立抽样；Responses WebSocket 的连接池、透传、HTTP 桥接模式均按每轮请求重新抽样。同一轮、同一账号的重试复用已选规则，响应改写与计费也使用同一选择。90% 是长期抽样概率，不保证每连续 10 次必有 9 次命中。

```text
可转移普通输入 = max(0, 原始总输入 - 原始读缓存 - 原始写缓存)
新增写缓存 = floor(可转移普通输入 × transfer_percent / 100)
```

总输入与读缓存不变。抽中 0% 转移规则、没有可转移普通输入、或新增写缓存不足 1 Token，都会使写缓存保持原值。

## 处理顺序

每个请求都以上游返回的原始 usage 为基础，先执行账号级普通输入转写缓存，再依次执行分组的缓存命中率扣减、读缓存放大、写缓存转移。分组策略基于账号调整后的 usage 执行。因此，分组开启“转移写缓存至普通输入”时，也会清零账号策略刚转出的写缓存；最终写缓存为 0 不等于账号概率未命中。

## 缓存命中率扣减（百分点）

技术字段为 `cache_read_deduction_percent`，范围 0–100，最多两位小数。设原始总输入为 T、原始读缓存为 C、扣减值为 D：

```text
扣减后读缓存 = floor(max(0, C - T × D / 100))
普通输入 = max(0, T - 扣减后读缓存 - 缓存写入)
```

总输入、缓存写入和输出不变。

## 放大读缓存（百分比）

- `cache_read_amplification_percent`：放大百分比，范围 0–100，最多两位小数。
- `cache_read_amplification_min_input_tokens`：激活门槛，按上游原始总输入判断；0 表示不设最低门槛。

设原始总输入为 T、放大比例为 A，且 T 达到门槛：

```text
新增读缓存 = floor(T × A / 100)
放大后总输入 = 扣减后总输入 + 新增读缓存
放大后读缓存 = 扣减后读缓存 + 新增读缓存
```

普通输入、缓存写入和输出不变。例如原始总输入 10000，普通输入 200、读缓存 9700、写缓存 100，门槛为 10000、放大比例为 20%，调整后为：

```text
总输入 12000 = 普通输入 200 + 读缓存 11700 + 写缓存 100
```

## 转移写缓存至普通输入

`transfer_cache_write_to_input` 开启后，在前两项调整完成后将写缓存 Tokens 清零，并把相同数量计入普通输入。总输入、读缓存和输出不变。例如上例继续执行转移后：

```text
总输入 12000 = 普通输入 300 + 读缓存 11700 + 写缓存 0
```

## 计费、使用记录和权限

HTTP JSON、SSE、Responses WebSocket 和 Messages 协议转换都在最终客户端写出边界使用调整后 usage。用户余额、订阅、API Key 额度和用户金额限额使用调整后费用；上游账号额度与账号成本统计仍使用原始 usage。

存在有效账号规则选择或生效的分组子策略时，请求保存 `usage_logs.cache_read_adjustment`，包含原始/应计 usage、扣减百分点、放大百分比、放大门槛、写缓存转移开关和原始用量费用。账号策略还保存 `account_policy` 和账号阶段的 `account_effective`，抽中 0% 转移规则时也会保存。管理员可在使用记录、汇总和导出中查看；普通用户 DTO 不暴露实验策略或原始快照。

关闭分组总开关会停止新请求的分组级 OpenAI 实验性用量调整，账号级策略由账号自己的开关控制；两者均不改写历史账单和快照。已建立的长连接需要重连后才读取新配置。

## 排查“设置 90% 但大量无转写”

先限定同一个上游 `account_id`、规则未变更的时间段，必要时再按分组和 HTTP/WS 拆分。不要只按最终 `cache_creation_tokens > 0` 统计概率；上游本来可能已有写缓存，后续分组策略也可能清零写缓存。管理员使用记录的“缓存用量调整明细”及 `usage_logs.cache_read_adjustment` 可区分以下情况：

| 审计字段 | 判断方式 |
| --- | --- |
| `account_policy` | 缺失表示该记录没有捕获到有效的账号规则选择；检查实际路由账号、开关、账号类型、部署版本及旧连接配置，不能将其计作随机未命中。 |
| `account_policy.transfer_percent` | 0 表示抽中了不转移档位；大于 0 表示抽中了转移档位。对应的 `probability_percent` 是该档配置概率。 |
| `raw` 与 `account_effective` | 比较两者的 `cache_creation_input_tokens`，得到账号阶段实际新增写缓存；抽中但增量为 0 时，检查可转移普通输入及向下取整。JSON 省略的 Token 字段按 0 读取。 |
| `transfer_cache_write_to_input` 与 `effective` | 为 true 时，分组会把账号阶段的写缓存转回普通输入；通过 `account_effective` 与 `effective` 对比确认。 |

统计“转移档位命中率”时，分母应为带有效 `account_policy` 的记录数，分子为其中 `transfer_percent > 0` 的记录数；期望值是所有非零转移档位的 `probability_percent` 之和。账号策略缺失的记录应另外统计。账号规则发生变化后，已建立的 WS 会话仍持有建连时的账号配置，需重连后再观察新配置。

### 新旧实例共用 Redis 时的缓存兼容性

不支持账号策略的旧实例仍可能通过后台快照重建、outbox 或账号状态同步写入共享的 `sched:acc:<id>` 和 `sched:meta:<id>`。其序列化结果不包含 `OpenAIExperimentalFeaturesEnabled`、`OpenAINormalInputCacheRules`。即使 Nginx 只将请求转给新实例，旧实例也可能覆盖新实例写出的账号缓存。

旧行为会把缺字段的有效 JSON 当作缓存命中，反序列化为 `false` 和空规则，于是在概率抽样之前就跳过整个账号策略。典型表现是数据库策略始终启用，但 Redis 中上述字段时有时无，使用记录的 `account_policy` 也成批消失；设置 100% 无法解决。

修复后，OpenAI API Key 的完整账号或精简快照缺少上述任一字段时，按缓存未命中走已有数据库回退；明确保存的 `false` 与 `null`/空规则仍是有效的关闭配置。其他平台和 OAuth 账号不受此兼容检查影响。回退仍遵守调度器原有的数据库回退开关、限流与超时。

部署时应将所有共用调度 Redis 的旧实例升级到兼容版本或停用，包括不承接当前域名请求的后台实例。停用后仍需由新实例刷新已被覆盖的缓存。仅重新保存账号、清理一次缓存或重启同一个旧镜像，都不能阻止仍在运行的旧实例随后再次覆盖；修复不追溯调整历史记录或账单。
