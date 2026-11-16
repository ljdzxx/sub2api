# 支付服务商选项 API / Payment Provider Options API

本页描述内置充值页的多服务商协议。它不同于外部支付系统的 Admin 充值兑换接口。响应示例省略通用 `{ "code": 0, "data": ... }` 包装。

This protocol powers built-in checkout, not the external Admin redemption integration. Examples omit the standard response envelope.

## 服务商配置 / Provider Configuration

管理员使用 `POST /api/v1/admin/payment/providers` 和 `PUT /api/v1/admin/payment/providers/:id` 创建或更新实例，在原连接参数之外提交：

```json
{
  "recharge_policy": {
    "mode": "discount",
    "tiers": [{ "min_amount": 100, "bonus_percent": 10 }],
    "notice": "充值满 100 享 10% 优惠"
  }
}
```

- `mode`：`bonus` 为到账赠金；`discount` 为实付折扣，沿用 `bonus_percent` 字段名，折扣比例必须小于 100。
- 阶梯最多 20 条，门槛不重复、金额与百分比最多两位小数；空数组表示无优惠。优惠仅用于余额充值，不用于订阅。
- 更新省略 `recharge_policy` 时保留原值；新建省略时无优惠。写入严格校验，历史无效阶梯在读取时安全过滤。
- 创建 / 更新成功只返回 `{ "id": 12 }`；使用管理员列表接口重新获取脱敏配置，凭据不通过创建 / 更新响应返回。
- `sort_order` 仍为实例字段；管理页面通过拖拽或上下移动调用原子排序接口，不需要编辑数字。

Admin create/update accepts `recharge_policy`. Omitted updates preserve the policy; new providers default to no promotion. Bonus mode increases credited USD, while discount mode reduces payment. Subscriptions are excluded. Writes are validated strictly; malformed historical tiers are ignored on reads. Successful writes return only the instance ID.

```http
PUT /api/v1/admin/payment/providers/reorder
Content-Type: application/json

{"ids":[14,12]}
```

`ids` 必须包含完整实例列表且不重复；服务器在事务中保存顺序。列表已变更时返回 `PAYMENT_PROVIDERS_CHANGED`，调用方刷新后重试。

Send the complete instance list exactly once per ID. Reordering is transactional; refresh on `PAYMENT_PROVIDERS_CHANGED`.

## 充值页数据 / Checkout Options

已登录用户通过 `GET /api/v1/payment/checkout-info` 获取 `payment_options`，每个「实例 + 支付方式」对应一个选项，按 `sort_order`、实例 ID 排列；同一实例内先微信、后支付宝。

```json
{
  "min_amount": 1,
  "max_amount": 5000,
  "payment_options": [{
    "id": "12:wxpay",
    "provider_instance_id": "12",
    "provider_name": "X 易支付",
    "provider_key": "easypay",
    "payment_type": "wxpay",
    "sort_order": 0,
    "currency": "CNY",
    "single_min": 1,
    "single_max": 1000,
    "daily_limit": 10000,
    "daily_used": 2000,
    "daily_remaining": 8000,
    "fee_rate": 0,
    "available": true,
    "recharge_policy": {"mode":"bonus","tiers":[],"notice":""},
    "pricing_revision": "opaque-sha256-revision"
  }]
}
```

- 按 `provider_instance_id` 分组显示名称，使用选项 `id` 作为前端选择键；提交时使用实例 ID 和真实 `payment_type`，不提交复合 `id`。
- 全局 `min_amount` / `max_amount` 校验用户输入金额；实例单笔与每日限额校验含手续费的最终实付。限额为 0 表示不限，当日已收款金额与尚未过期的待支付订单（含跨日订单）共同占用实例额度。
- `recharge_policy` 随服务商切换；`pricing_revision` 是不透明价格版本，排序不会使价格版本失效。
- Stripe 选项额外含该实例的 `stripe_publishable_key`，没有任何服务端密钥。恢复已有支付时优先使用订单返回的公钥，不使用另一实例公钥。
- 旧 `methods` 和全局优惠响应字段保留兼容，新前端以 `payment_options` 为准，空数组表示没有可选服务商，不能回退到旧列表。

Each option represents one instance and payment type. Group by instance, retain option IDs in UI state, and calculate previews with that option's policy and currency. A zero limit means unlimited. An empty options array means checkout is unavailable; do not fall back to the legacy method list. Only Stripe publishable keys are exposed, never secrets.

## 下单 / Create Order

```http
POST /api/v1/payment/orders
Content-Type: application/json

{
  "amount": 100,
  "order_type": "balance",
  "payment_type": "wxpay",
  "provider_instance_id": "12",
  "pricing_revision": "opaque-sha256-revision"
}
```

服务端重新计算金额，不信任前端预览。显式选择实例必须携带当前版本。旧客户端省略实例 ID 时，仅在该支付方式只有一个启用实例时兼容；多个匹配实例返回 `PAYMENT_PROVIDER_REQUIRED`，不会随机或按负载均衡选择。

The server recalculates pricing. Explicit instance selection requires the current revision. Legacy requests without an instance work only when exactly one enabled instance supports the method; ambiguous requests never auto-route.

| 错误 / Reason | 处理 / Handling |
| --- | --- |
| `PAYMENT_PRICING_CHANGED` | 刷新选项，展示新价格后要求重新提交 / Refresh and ask for confirmation |
| `PAYMENT_PROVIDER_REQUIRED` | 要求选择具体实例 / Require explicit selection |
| `PAYMENT_PROVIDER_UNAVAILABLE` | 提示原选择不可用，用户自行更换 / User must choose again |
| `PAYMENT_INSTANCE_AMOUNT_OUT_OF_RANGE` | 调整金额或服务商 / Adjust amount or provider |
| `NO_AVAILABLE_INSTANCE` | 所选实例当日额度不足，不自动换家 / Selected provider daily capacity exhausted |

新订单保存不可变 `pricing_snapshot`，包含服务商名称、输入金额、优惠、汇率、手续费和最终金额；管理员订单接口可读取。普通订单响应增加 `provider_name`，Stripe 订单详情增加 `stripe_publishable_key`。微信 OAuth 恢复令牌绑定实例、价格版本和原始输入金额，扫码回退仍使用原实例和原始输入；回调、查询与退款继续按历史实例处理。

Orders freeze pricing and provider identity. OAuth resume and mobile QR fallback preserve the original provider, revision and input amount. Callbacks, queries and refunds remain bound to the historical instance.

Stripe 共用回调地址时先读取 `data.object.metadata.orderId` 定位历史实例，再使用该实例的 Webhook Secret 验签；未验签字段仅作为查找线索。微信密文回调会尝试同类型实例（含已停用实例）的验签 / 解密配置，并继续校验订单商户身份。

Shared Stripe callbacks use order metadata only as an untrusted lookup hint, then verify with the historical instance's secret. Encrypted WeChat callbacks retain disabled instance verifiers so stopping new orders does not interrupt outstanding payments.

## 升级 / Upgrade

应用迁移 `254_payment_instance_recharge_policy.sql`，将旧全局优惠一次性复制到升级时存在的所有服务商。后续实例配置不读取全局优惠，新实例不继承。迁移不修改已有订单金额，不补造历史价格快照。回滚应用时需单独评估旧客户端路由与旧全局优惠的兼容性，不能仅靠数据库降级。

Migration copies legacy promotions once to existing providers, leaves historical order amounts unchanged and does not fabricate historical snapshots. New providers do not inherit global settings. Application rollback requires an explicit compatibility review.
