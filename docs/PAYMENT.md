# Payment System Configuration Guide

Sub2API has a built-in payment system that enables user self-service top-up without deploying a separate payment service.

---

## Table of Contents

- [Supported Payment Methods](#supported-payment-methods)
- [Quick Start](#quick-start)
- [System Settings](#system-settings)
- [Provider Configuration](#provider-configuration)
- [Provider Instance Management](#provider-instance-management)
- [Webhook Configuration](#webhook-configuration)
- [Payment Flow](#payment-flow)
- [Migrating from Sub2ApiPay](#migrating-from-sub2apipay)

---

## Supported Payment Methods

| Provider | Payment Methods | Description |
|----------|----------------|-------------|
| **EasyPay** | Alipay, WeChat Pay | Third-party aggregation via EasyPay protocol |
| **Alipay (Direct)** | Desktop QR code, mobile Alipay redirect | Direct integration with Alipay Open Platform, returning desktop QR codes and mobile WAP/app launch links |
| **WeChat Pay (Direct)** | Native QR, H5, MP/JSAPI Pay | Direct integration with WeChat Pay APIv3 with environment-aware routing |
| **Stripe** | Card, Alipay, WeChat Pay, Link, etc. | International payments, multi-currency support |

> Multiple instances of the same provider type can be enabled together, including direct channels alongside EasyPay. Checkout groups methods by provider name, for example `X EasyPay: WeChat Pay / Alipay` and `Y EasyPay: WeChat Pay / Alipay`. Users select the exact provider and method; there is no single-source restriction per method.

> **EasyPay Provider Recommendations**: Both options below are third-party aggregators compatible with the EasyPay protocol. Pick based on the funding channel and settlement currency you need:
>
> - **Domestic channel / CNY settlement** — [ZPay](https://z-pay.cn/?uid=23808) (`https://z-pay.cn/?uid=23808`): direct integration with official Alipay / WeChat Pay APIs, fee **1.6%**; funds go straight to the merchant account with **T+1 automatic settlement**. Supports **individual users** (no business license required) with up to 10,000 CNY daily transactions; business-licensed accounts have no limit. Link contains the referral code of [Sub2ApiPay](https://github.com/touwaeriol/sub2apipay) original author [@touwaeriol](https://github.com/touwaeriol) — feel free to remove it.
> - **International channel / USDT or USD settlement** — [Kyren Topup](https://kyrenpay.com/?code=SUB2API) (`https://kyrenpay.com/?code=SUB2API`): a ready-to-launch global payment stack for AI startups with WeChat Pay and Alipay support, local-currency checkout, and USD settlement. Fees: WeChat 2.5%, Alipay 2.5%; Multiple withdrawal methods are available. Withdrawals to overseas company accounts incur a $20 fee, while USDT withdrawals incur a $30 fee plus a 0.4% transaction fee, settled in **USDT or USD**. No qualification review required — sign up and use immediately, making it the lowest barrier to entry. Withdrawal threshold is relatively high, recommended for users **who do not use domestic Chinese payment channels, cannot tolerate Stripe's 6%+ fees, have high transaction volume, and have USD or USDT channels to receive withdrawn funds**. Kyren Topup charges a $200 account opening fee; signing up via this link (which contains Sub2Api author [@Wei-Shaw](https://github.com/Wei-Shaw)'s referral code) **waives the opening fee**. Feel free to remove it if you prefer.
>
> Please evaluate the security, reliability, and compliance of any third-party payment provider on your own — this project does not endorse or guarantee any of them.

---

## Quick Start

1. Go to Admin Dashboard → **Settings** → **Payment Settings** tab
2. Enable **Payment**
3. Configure basic parameters (amount range, timeout, etc.)
4. Add at least one provider instance in **Provider Management**
5. Users can now top up from the frontend

---

## System Settings

Configure the following in Admin Dashboard **Settings → Payment Settings**:

### Basic Settings

| Setting | Description | Default |
|---------|-------------|---------|
| **Enable Payment** | Enable or disable the payment system | Off |
| **Product Name Prefix** | Prefix shown on payment page | - |
| **Product Name Suffix** | Suffix (e.g., "Credits") | - |
| **Minimum Amount** | Minimum single top-up amount | 1 |
| **Maximum Amount** | Maximum single top-up amount (empty = unlimited) | - |
| **Daily Limit** | Per-user daily cumulative limit (empty = unlimited) | - |
| **Order Timeout** | Order timeout in minutes (minimum 1) | 30 |
| **Max Pending Orders** | Maximum concurrent pending orders per user | 3 |

### Checkout Provider Selection

Checkout displays providers before payment methods and recharge amounts:

- Identically named methods remain separate under each provider. Orders bind to the selected instance.
- Switching providers updates promotion tiers, Markdown notice, payable/credited previews, currency and limits. Changing currency clears the amount.
- Unavailable or over-limit selections require the user to adjust the amount or choose another provider; there is no automatic failover.
- Pricing changes refresh the quote and require confirmation again. Existing orders retain their original pricing.

Legacy load-balancing settings remain for compatibility but do not select providers in the new checkout.

### Cancel Rate Limiting

Prevents users from repeatedly creating and canceling orders:

| Setting | Description |
|---------|-------------|
| **Enable Limit** | Toggle |
| **Window Mode** | Sliding / Fixed window |
| **Time Window** | Window duration |
| **Window Unit** | Minutes / Hours |
| **Max Cancels** | Maximum cancellations allowed within the window |

### Help Information

| Setting | Description |
|---------|-------------|
| **Help Image** | Customer service QR code or help image (supports upload) |
| **Help Text** | Instructions displayed on the payment page |

---

## Provider Configuration

Each provider type requires different credentials. Select the type when adding a new provider instance in **Provider Management → Add Provider**.

> **Callback URLs are auto-generated**: When adding a provider, the Notify URL and Return URL are automatically constructed from your site domain. You only need to confirm the domain is correct.

### EasyPay

Compatible with any payment service that implements the EasyPay protocol.

| Parameter | Description | Required |
|-----------|-------------|----------|
| **Merchant ID (PID)** | EasyPay merchant ID | Yes |
| **Merchant Key (PKey)** | EasyPay merchant secret key | Yes |
| **API Base URL** | EasyPay API base address | Yes |
| **Alipay Channel ID** | Specify Alipay channel (optional) | No |
| **WeChat Channel ID** | Specify WeChat channel (optional) | No |

### Alipay (Direct)

Direct integration with Alipay Open Platform. Mobile flows return an Alipay WAP/app redirect URL. Desktop flows prefer Face-to-Face Precreate QR payloads; if the merchant has not enabled that product, the provider falls back to Computer Website Pay and also returns the cashier URL so the frontend can render a QR code or open the hosted checkout page directly.

| Parameter | Description | Required |
|-----------|-------------|----------|
| **AppID** | Alipay application AppID | Yes |
| **Private Key** | RSA2 application private key | Yes |
| **Alipay Public Key** | Alipay public key | Yes |

### WeChat Pay (Direct)

Direct integration with WeChat Pay APIv3. Supports Native QR code payment, H5 payment, and MP/JSAPI payment inside the WeChat environment.

| Parameter | Description | Required |
|-----------|-------------|----------|
| **AppID** | WeChat Pay AppID | Yes |
| **Merchant ID (MchID)** | WeChat Pay merchant ID | Yes |
| **Merchant API Private Key** | Merchant API private key (PEM format) | Yes |
| **APIv3 Key** | 32-byte APIv3 key | Yes |
| **WeChat Pay Public Key** | WeChat Pay public key (PEM format) | Yes |
| **WeChat Pay Public Key ID** | WeChat Pay public key ID | Yes |
| **Certificate Serial Number** | Merchant certificate serial number | Yes |

### Stripe

International payment platform supporting multiple payment methods and currencies.

| Parameter | Description | Required |
|-----------|-------------|----------|
| **Secret Key** | Stripe secret key (`sk_live_...` or `sk_test_...`) | Yes |
| **Publishable Key** | Stripe publishable key (`pk_live_...` or `pk_test_...`) | Yes |
| **Webhook Secret** | Stripe Webhook signing secret (`whsec_...`) | Yes |

---

## Provider Instance Management

See [Payment Provider Options API](PAYMENT_PROVIDER_OPTIONS_API.md) for request fields and upgrade compatibility.

You can create and enable **multiple instances** of the same provider type for explicit user selection:

- **Provider groups** — Show each instance's name and payment methods independently
- **Independent promotions** — Configure bonus / discount tiers and a Markdown notice in the instance's Recharge Promotion tab; subscriptions do not receive recharge promotions
- **Independent limits** — Each instance can have its own min/max amount and daily limit
- **Independent toggle** — Enable/disable individual instances without affecting others
- **Refund control** — Enable or disable refunds per instance
- **Payment methods** — Each instance can support a subset of payment methods
- **Ordering** — Drag or use move-up / move-down buttons; the saved order controls checkout display without a numeric sort field

On upgrade, migration `254_payment_instance_recharge_policy.sql` copies the legacy global recharge promotion to existing instances once. Policies are independent afterward, and new instances start without promotions. Existing orders keep their original pricing. Disabling a provider blocks new orders without interrupting callbacks for existing orders.

### Instance Limit Configuration

Each instance supports these limits:

| Limit | Description |
|-------|-------------|
| **Minimum Amount** | Minimum order amount accepted by this instance |
| **Maximum Amount** | Maximum order amount accepted by this instance |
| **Daily Limit** | Daily cumulative transaction limit for this instance |

> Instance limits apply to the final payable amount including fees. Pending orders reserve daily instance capacity. A limit rejection never redirects the order to another provider.

---

## Webhook Configuration

Payment callbacks are essential for the payment system to work correctly.

### Callback URL Format

When adding a provider, the system auto-generates callback URLs from your site domain:

| Provider | Callback Path |
|----------|-------------|
| **EasyPay** | `https://your-domain.com/api/v1/payment/webhook/easypay` |
| **Alipay (Direct)** | `https://your-domain.com/api/v1/payment/webhook/alipay` |
| **WeChat Pay (Direct)** | `https://your-domain.com/api/v1/payment/webhook/wxpay` |
| **Stripe** | `https://your-domain.com/api/v1/payment/webhook/stripe` |

> Replace `your-domain.com` with your actual domain. For EasyPay / Alipay / WeChat Pay, the callback URL is auto-filled when adding the provider — no manual configuration needed.

### Stripe Webhook Setup

1. Log in to [Stripe Dashboard](https://dashboard.stripe.com/)
2. Go to **Developers → Webhooks**
3. Add an endpoint with the callback URL
4. Subscribe to events: `payment_intent.succeeded`, `payment_intent.payment_failed`
5. Copy the generated Webhook Secret (`whsec_...`) to your provider configuration

### Important Notes

- Callback URLs must use **HTTPS** (required by Stripe, strongly recommended for others)
- Ensure your firewall allows callback requests from payment platforms
- The system automatically verifies callback signatures to prevent forgery
- Balance top-up is processed automatically upon successful payment — no manual intervention needed

---

## Payment Flow

```
User selects amount and payment method
       │
       ▼
  Create Order (PENDING)
  ├─ Validate amount range, pending order count, daily limit
  ├─ Validate selected instance and pricing revision; freeze pricing snapshot
  └─ Call provider to get payment info
       │
       ▼
  User completes payment
  ├─ EasyPay     → QR code / H5 redirect
  ├─ Alipay      → Desktop QR payload (Face-to-Face preferred, Website Pay fallback) / mobile Alipay redirect
  ├─ WeChat Pay  → Desktop Native QR / non-WeChat H5 / in-WeChat JSAPI
  └─ Stripe      → Payment Element (card/Alipay/WeChat/etc.)
       │
       ▼
  Webhook callback verified → Order PAID
       │
       ▼
  Auto top-up to user balance → Order COMPLETED
```

### Order Status Reference

| Status | Description |
|--------|-------------|
| `PENDING` | Waiting for user to complete payment |
| `PAID` | Payment confirmed, awaiting balance credit |
| `COMPLETED` | Balance credited successfully |
| `EXPIRED` | Timed out without payment |
| `CANCELLED` | Cancelled by user |
| `FAILED` | Balance credit failed, admin can retry |
| `REFUND_REQUESTED` | Refund requested |
| `REFUNDING` | Refund in progress |
| `REFUNDED` | Refund completed |

### Timeout and Fallback

- Before marking an order as expired, the background job queries the upstream payment status first
- If the user has actually paid but the callback was delayed, the system will reconcile automatically
- The background job runs every 60 seconds to check for timed-out orders

---

## Migrating from Sub2ApiPay

If you previously used [Sub2ApiPay](https://github.com/touwaeriol/sub2apipay) as an external payment system, you can migrate to the built-in payment system:

### Key Differences

| Aspect | Sub2ApiPay | Built-in Payment |
|--------|-----------|-----------------|
| Deployment | Separate service (Next.js + PostgreSQL) | Built into Sub2API, no extra deployment |
| Payment Methods | EasyPay, Alipay, WeChat, Stripe | Same |
| Configuration | Environment variables + separate admin UI | Unified in Sub2API admin dashboard |
| Top-up Integration | Via Admin API callback | Internal processing, more reliable |
| Subscription Plans | Supported | Not yet (planned) |
| Order Management | Separate admin interface | Integrated in Sub2API admin dashboard |

### Migration Steps

1. Enable payment in Sub2API admin dashboard and configure providers (use the same payment credentials)
2. Update webhook callback URLs to Sub2API's callback endpoints
3. Verify that new orders are processed correctly via built-in payment
4. Decommission the Sub2ApiPay service

> **Note**: Historical order data from Sub2ApiPay will not be automatically migrated. Keep Sub2ApiPay running for a while to access historical records.
