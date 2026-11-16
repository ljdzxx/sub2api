package service

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"
)

type paymentOptionUserRepository struct{ UserRepository }

func createPaymentOptionPendingOrder(t *testing.T, ctx context.Context, client *dbent.Client, inst *dbent.PaymentProviderInstance) {
	t.Helper()
	user := client.User.Create().SetEmail("pending@example.com").SetPasswordHash("hash").SaveX(ctx)
	client.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).SetUserName("pending").
		SetAmount(88).SetPayAmount(88).SetRechargeCode("PENDING").SetOutTradeNo("pending-provider-order").
		SetPaymentType("wxpay").SetPaymentTradeNo("").SetStatus(OrderStatusPending).
		SetCreatedAt(time.Now().Add(-24 * time.Hour)).
		SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("").SetSrcHost("").
		SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).SetProviderKey(inst.ProviderKey).SaveX(ctx)
}

func (repo *paymentOptionUserRepository) GetByID(context.Context, int64) (*User, error) {
	return &User{ID: 1, Email: "test@example.com", Username: "test", Status: "active"}, nil
}

func createPaymentOptionTestInstance(t *testing.T, client *dbent.Client, name string, policy RechargePolicy, sortOrder int) *dbent.PaymentProviderInstance {
	t.Helper()
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).SetName(name).
		SetSupportedTypes("wxpay,alipay").SetEnabled(true).SetSortOrder(sortOrder).
		SetPaymentMode("popup").SetRechargePolicy(rechargePolicyMap(policy)).
		SetConfig(`{"pid":"` + name + `","pkey":"secret-` + name + `","apiBase":"https://` + strings.ToLower(name) + `.example.com","notifyUrl":"https://app.example.com/notify","returnUrl":"https://app.example.com/result"}`).Save(context.Background())
	require.NoError(t, err)
	return inst
}

func TestPaymentOptionsPreserveInstancesPoliciesAndOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	bonus := RechargePolicy{Mode: "bonus", Tiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}}, Notice: "X promotion"}
	discount := RechargePolicy{Mode: "discount", Tiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 10}}}
	first := createPaymentOptionTestInstance(t, client, "X", bonus, 9)
	second := createPaymentOptionTestInstance(t, client, "Y", discount, 2)
	_, err := client.PaymentProviderInstance.UpdateOneID(second.ID).SetLimits(`{"wxpay":{"singleMin":2,"singleMax":1000,"dailyLimit":500}}`).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentConfigService{entClient: client}
	cfg := &PaymentConfig{Enabled: true, BalanceRechargeMultiplier: .14, RechargeFeeRate: 2}
	options, err := svc.GetPaymentOptions(ctx, cfg)
	require.NoError(t, err)
	require.Len(t, options, 4)
	require.Equal(t, strconv.FormatInt(second.ID, 10)+":wxpay", options[0].ID)
	require.Equal(t, "Y", options[0].ProviderName)
	require.Equal(t, "discount", options[0].RechargePolicy.Mode)
	require.Equal(t, 500.0, options[0].DailyRemaining)
	require.Equal(t, 2.0, options[0].SingleMin)
	require.Equal(t, options[0].PricingRevision, options[1].PricingRevision)
	require.Equal(t, strconv.FormatInt(first.ID, 10), options[2].ProviderInstanceID)
	require.Equal(t, bonus, options[2].RechargePolicy)
	require.NotEqual(t, options[0].PricingRevision, options[2].PricingRevision)
	encoded, err := json.Marshal(options)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-")
	require.NotContains(t, string(encoded), "apiBase")
	_, err = client.PaymentProviderInstance.UpdateOneID(first.ID).SetEnabled(false).Save(ctx)
	require.NoError(t, err)
	options, err = svc.GetPaymentOptions(ctx, cfg)
	require.NoError(t, err)
	require.Len(t, options, 2)
}

func TestRequestedPaymentInstanceRejectsAmbiguityAndUnsupportedMethods(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	first := createPaymentOptionTestInstance(t, client, "X", RechargePolicy{}, 0)
	second := createPaymentOptionTestInstance(t, client, "Y", RechargePolicy{}, 1)
	svc := &PaymentService{configService: &PaymentConfigService{entClient: client}}
	_, err := svc.resolveRequestedPaymentInstance(ctx, CreateOrderRequest{PaymentType: "wxpay"})
	require.Equal(t, "PAYMENT_PROVIDER_REQUIRED", infraerrors.Reason(err))
	selected, err := svc.resolveRequestedPaymentInstance(ctx, CreateOrderRequest{PaymentType: "wxpay", ProviderInstanceID: strconv.FormatInt(first.ID, 10)})
	require.NoError(t, err)
	require.Equal(t, first.ID, selected.ID)
	_, err = svc.resolveRequestedPaymentInstance(ctx, CreateOrderRequest{PaymentType: "stripe", ProviderInstanceID: strconv.FormatInt(first.ID, 10)})
	require.Equal(t, "PAYMENT_PROVIDER_UNAVAILABLE", infraerrors.Reason(err))
	_, err = client.PaymentProviderInstance.UpdateOneID(second.ID).SetEnabled(false).Save(ctx)
	require.NoError(t, err)
	selected, err = svc.resolveRequestedPaymentInstance(ctx, CreateOrderRequest{PaymentType: "alipay"})
	require.NoError(t, err)
	require.Equal(t, first.ID, selected.ID)
}

func TestCreateOrderUsesSelectedInstancePolicyAndSnapshot(t *testing.T) {
	for _, mode := range []string{"bonus", "discount"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			user, err := client.User.Create().SetEmail("test@example.com").SetPasswordHash("hash").Save(ctx)
			require.NoError(t, err)
			require.EqualValues(t, 1, user.ID)
			policy := RechargePolicy{Mode: mode, Tiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}}}
			selected := createPaymentOptionTestInstance(t, client, "X", policy, 1)
			createPaymentOptionTestInstance(t, client, "Y", RechargePolicy{}, 0)
			settings := &paymentConfigSettingRepoStub{values: map[string]string{
				SettingPaymentEnabled: "true", SettingBalanceRechargeMult: ".14",
				SettingRechargeBonusTiers: `[{"min_amount":0,"bonus_percent":999}]`,
			}}
			configService := &PaymentConfigService{entClient: client, settingRepo: settings}
			cfg, err := configService.GetPaymentConfig(ctx)
			require.NoError(t, err)
			service := &PaymentService{entClient: client, configService: configService, userRepo: &paymentOptionUserRepository{}}
			req := CreateOrderRequest{UserID: 1, Amount: 100, OrderType: "balance", PaymentType: "wxpay", ProviderInstanceID: strconv.FormatInt(selected.ID, 10), PricingRevision: paymentInstancePricingRevision(selected, cfg, policy, "CNY")}
			result, err := service.CreateOrder(ctx, req)
			require.NoError(t, err)
			require.Equal(t, req.ProviderInstanceID, result.ProviderInstanceID)
			require.Equal(t, "X", result.ProviderName)
			parsed, err := url.Parse(result.PayURL)
			require.NoError(t, err)
			require.Equal(t, "x.example.com", parsed.Host)
			require.Equal(t, "X", parsed.Query().Get("pid"))
			order, err := client.PaymentOrder.Get(ctx, result.OrderID)
			require.NoError(t, err)
			require.Equal(t, req.ProviderInstanceID, *order.ProviderInstanceID)
			require.Equal(t, req.PricingRevision, order.PricingSnapshot["pricing_revision"])
			require.Equal(t, 100.0, order.PricingSnapshot["input_amount"])
			require.Equal(t, 20.0, order.PricingSnapshot["matched_percent"])
			if mode == "bonus" {
				require.Equal(t, 100.0, order.PayAmount)
				require.Equal(t, 16.8, order.Amount)
			} else {
				require.Equal(t, 80.0, order.PayAmount)
				require.Equal(t, 14.0, order.Amount)
			}
			require.Equal(t, 2.8, order.BonusAmount)
			_, err = client.PaymentProviderInstance.UpdateOneID(selected.ID).SetRechargePolicy(rechargePolicyMap(RechargePolicy{Mode: "bonus", Tiers: []RechargeBonusTier{}})).Save(ctx)
			require.NoError(t, err)
			_, err = service.CreateOrder(ctx, req)
			require.Equal(t, "PAYMENT_PRICING_CHANGED", infraerrors.Reason(err))
			require.Equal(t, 1, client.PaymentOrder.Query().CountX(ctx))
			reloaded := client.PaymentOrder.GetX(ctx, order.ID)
			require.Equal(t, order.PricingSnapshot, reloaded.PricingSnapshot)
		})
	}
}

func TestPinnedPaymentRejectsLimitsWithoutFallingBack(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("test@example.com").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, user.ID)
	inst := createPaymentOptionTestInstance(t, client, "X", RechargePolicy{}, 0)
	createPaymentOptionTestInstance(t, client, "Y", RechargePolicy{}, 1)
	inst = client.PaymentProviderInstance.UpdateOneID(inst.ID).SetLimits(`{"wxpay":{"singleMax":100,"dailyLimit":150}}`).SaveX(ctx)
	svc := &PaymentService{configService: &PaymentConfigService{entClient: client}}
	req := CreateOrderRequest{PaymentType: "wxpay", ProviderInstanceID: strconv.FormatInt(inst.ID, 10)}
	_, err = svc.selectPinnedPaymentInstance(ctx, inst, req, 101)
	require.Equal(t, "PAYMENT_INSTANCE_AMOUNT_OUT_OF_RANGE", infraerrors.Reason(err))
	client.PaymentOrder.Create().SetUserID(1).SetUserEmail("test@example.com").SetUserName("test").
		SetAmount(100).SetPayAmount(100).SetRechargeCode("PAY").SetPaymentType("wxpay").SetPaymentTradeNo("").
		SetProviderInstanceID(req.ProviderInstanceID).SetStatus(OrderStatusPending).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("").SetSrcHost("").SaveX(ctx)
	_, err = svc.selectPinnedPaymentInstance(ctx, inst, req, 60)
	require.Equal(t, "NO_AVAILABLE_INSTANCE", infraerrors.Reason(err))
	options, err := svc.configService.GetPaymentOptions(ctx, &PaymentConfig{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, 50.0, options[0].DailyRemaining)
}

func TestReorderPaymentProvidersRollsBackAndKeepsPricingRevision(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	first := createPaymentOptionTestInstance(t, client, "X", RechargePolicy{}, 0)
	second := createPaymentOptionTestInstance(t, client, "Y", RechargePolicy{}, 1)
	svc := &PaymentConfigService{entClient: client}
	require.Error(t, svc.ReorderProviderInstances(ctx, []int64{second.ID, second.ID}))
	require.Equal(t, 1, client.PaymentProviderInstance.GetX(ctx, second.ID).SortOrder)
	require.NoError(t, svc.ReorderProviderInstances(ctx, []int64{second.ID, first.ID}))
	require.Equal(t, 0, client.PaymentProviderInstance.GetX(ctx, second.ID).SortOrder)
	policy, err := instanceRechargePolicy(first)
	require.NoError(t, err)
	cfg := &PaymentConfig{BalanceRechargeMultiplier: .14}
	require.Equal(t, paymentInstancePricingRevision(first, cfg, policy, "CNY"), paymentInstancePricingRevision(client.PaymentProviderInstance.GetX(ctx, first.ID), cfg, policy, "CNY"))
	cfg.RechargeFeeRate = 2
	require.NotEqual(t, paymentInstancePricingRevision(first, cfg, policy, "CNY"), paymentInstancePricingRevision(first, &PaymentConfig{BalanceRechargeMultiplier: .14}, policy, "CNY"))
}

func TestPaymentInstanceRechargePolicyReadsLegacyInvalidTiersSafely(t *testing.T) {
	inst := &dbent.PaymentProviderInstance{RechargePolicy: map[string]any{
		"mode": "discount", "notice": "legacy notice",
		"tiers": []RechargeBonusTier{
			{MinAmount: 100, BonusPercent: 10}, {MinAmount: -1, BonusPercent: 2},
			{MinAmount: 200, BonusPercent: 100}, {MinAmount: 100, BonusPercent: 20},
		},
	}}
	policy, err := instanceRechargePolicy(inst)
	require.NoError(t, err)
	require.Equal(t, "legacy notice", policy.Notice)
	require.Equal(t, []RechargeBonusTier{{MinAmount: 100, BonusPercent: 10}}, policy.Tiers)
}

func TestPaymentOptionsReservePendingAcrossMidnightAndUseActualPaidDay(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	inst := createPaymentOptionTestInstance(t, client, "X", RechargePolicy{}, 0)
	createPaymentOptionPendingOrder(t, ctx, client, inst)
	order := client.PaymentOrder.Query().OnlyX(ctx)
	now := time.Now()
	usage, err := paymentInstanceDailyUsage(ctx, client, []*dbent.PaymentProviderInstance{inst})
	require.NoError(t, err)
	require.Equal(t, 88.0, usage[strconv.FormatInt(inst.ID, 10)])
	client.PaymentOrder.UpdateOneID(order.ID).SetExpiresAt(now.Add(-time.Minute)).SaveX(ctx)
	usage, err = paymentInstanceDailyUsage(ctx, client, []*dbent.PaymentProviderInstance{inst})
	require.NoError(t, err)
	require.Empty(t, usage)
	client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).SetPaidAt(now).SaveX(ctx)
	usage, err = paymentInstanceDailyUsage(ctx, client, []*dbent.PaymentProviderInstance{inst})
	require.NoError(t, err)
	require.Equal(t, 88.0, usage[strconv.FormatInt(inst.ID, 10)])
}

func TestCreateOrderWeChatOAuthPreservesOriginalAmountAndSelectedInstance(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PAYMENT_RESUME_SIGNING_KEY", "0123456789abcdef0123456789abcdef")
	client := newPaymentConfigServiceTestClient(t)
	user := client.User.Create().SetEmail("test@example.com").SetPasswordHash("hash").SaveX(ctx)
	require.EqualValues(t, 1, user.ID)
	policy := RechargePolicy{Mode: "discount", Tiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}}}
	inst := client.PaymentProviderInstance.Create().SetProviderKey("wxpay").SetName("Official WeChat").
		SetConfig(`{"appId":"wx-mp-app"}`).SetSupportedTypes("wxpay").SetEnabled(true).SetRechargePolicy(rechargePolicyMap(policy)).SaveX(ctx)
	configService := &PaymentConfigService{entClient: client, settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{
		SettingPaymentEnabled: "true", SettingKeyWeChatConnectEnabled: "true", SettingKeyWeChatConnectMode: "mp",
		SettingKeyWeChatConnectAppID: "wx-mp-app", SettingKeyWeChatConnectAppSecret: "secret",
	}}}
	svc := &PaymentService{entClient: client, configService: configService, userRepo: &paymentOptionUserRepository{}}
	cfg, err := configService.GetPaymentConfig(ctx)
	require.NoError(t, err)
	request := CreateOrderRequest{UserID: 1, Amount: 100, OrderType: "balance", PaymentType: "wxpay", IsWeChatBrowser: true,
		ProviderInstanceID: strconv.FormatInt(inst.ID, 10), PricingRevision: paymentInstancePricingRevision(inst, cfg, policy, "CNY")}
	result, err := svc.CreateOrder(ctx, request)
	require.NoError(t, err)
	require.Equal(t, payment.CreatePaymentResultOAuthRequired, result.ResultType)
	require.Equal(t, 100.0, result.Amount)
	require.Equal(t, 80.0, result.PayAmount)
	authorizeURL, err := url.Parse(result.OAuth.AuthorizeURL)
	require.NoError(t, err)
	require.Equal(t, "100", authorizeURL.Query().Get("amount"))
	require.Equal(t, request.ProviderInstanceID, authorizeURL.Query().Get("provider_instance_id"))
	require.Equal(t, request.PricingRevision, authorizeURL.Query().Get("pricing_revision"))
	require.Zero(t, client.PaymentOrder.Query().CountX(ctx))
}

func TestPaymentInstanceRechargePolicyWritesAreStrictAndDoNotInheritGlobal(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client, settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{
		SettingRechargeBonusMode: "discount", SettingRechargeBonusTiers: `[{"min_amount":1,"bonus_percent":20}]`,
		SettingRechargeBonusNotice: "global notice",
	}}}
	request := CreateProviderInstanceRequest{
		ProviderKey: "easypay", Name: "X", Enabled: true, SupportedTypes: []string{"wxpay", "alipay"},
		Config: map[string]string{"pid": "X", "pkey": "secret-X", "apiBase": "https://x.example.com", "notifyUrl": "https://app.example.com/notify", "returnUrl": "https://app.example.com/result"},
	}
	first, err := svc.CreateProviderInstance(ctx, request)
	require.NoError(t, err)
	policy, err := instanceRechargePolicy(first)
	require.NoError(t, err)
	require.Empty(t, policy.Tiers)
	require.Empty(t, policy.Notice)
	request.Name = "Y"
	second, err := svc.CreateProviderInstance(ctx, request)
	require.NoError(t, err)
	require.True(t, first.Enabled && second.Enabled)
	require.Greater(t, second.SortOrder, first.SortOrder)
	invalidPolicies := []RechargePolicy{
		{Mode: "invalid"},
		{Mode: "discount", Tiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 100}}},
		{Tiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 10}, {MinAmount: 100, BonusPercent: 20}}},
		{Notice: strings.Repeat("x", maxRechargeBonusNoticeRunes+1)},
	}
	for _, invalidPolicy := range invalidPolicies {
		_, err = svc.UpdateProviderInstance(ctx, first.ID, UpdateProviderInstanceRequest{RechargePolicy: &invalidPolicy})
		require.Error(t, err)
	}
	stored := client.PaymentProviderInstance.GetX(ctx, first.ID)
	require.Equal(t, first.RechargePolicy, stored.RechargePolicy)
}

func TestPinnedPaymentTransactionRechecksPolicyLimitsAndEnabledState(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	inst := createPaymentOptionTestInstance(t, client, "X", RechargePolicy{}, 0)
	configService := &PaymentConfigService{entClient: client}
	svc := &PaymentService{entClient: client, configService: configService}
	cfg := &PaymentConfig{Enabled: true, BalanceRechargeMultiplier: .14}
	policy, err := instanceRechargePolicy(inst)
	require.NoError(t, err)
	req := CreateOrderRequest{PaymentType: "wxpay", ProviderInstanceID: strconv.FormatInt(inst.ID, 10), PricingRevision: paymentInstancePricingRevision(inst, cfg, policy, "CNY")}
	check := func() error {
		transaction, err := client.Tx(ctx)
		require.NoError(t, err)
		defer func() { require.NoError(t, transaction.Rollback()) }()
		return svc.validatePinnedPaymentInTx(ctx, transaction, req, cfg, 100)
	}
	require.NoError(t, check())
	client.PaymentProviderInstance.UpdateOneID(inst.ID).SetRechargePolicy(rechargePolicyMap(RechargePolicy{Mode: "bonus", Tiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 10}}})).SaveX(ctx)
	require.Equal(t, "PAYMENT_PRICING_CHANGED", infraerrors.Reason(check()))
	client.PaymentProviderInstance.UpdateOneID(inst.ID).SetRechargePolicy(inst.RechargePolicy).SetLimits(`{"wxpay":{"singleMax":99}}`).SaveX(ctx)
	require.Equal(t, "PAYMENT_INSTANCE_AMOUNT_OUT_OF_RANGE", infraerrors.Reason(check()))
	client.PaymentProviderInstance.UpdateOneID(inst.ID).SetLimits(`{"wxpay":{"dailyLimit":150}}`).SaveX(ctx)
	createPaymentOptionPendingOrder(t, ctx, client, inst)
	require.Equal(t, "NO_AVAILABLE_INSTANCE", infraerrors.Reason(check()))
	client.PaymentProviderInstance.UpdateOneID(inst.ID).SetEnabled(false).SaveX(ctx)
	require.Equal(t, "PAYMENT_PROVIDER_UNAVAILABLE", infraerrors.Reason(check()))
}

func TestPaymentOptionsKeepMixedCurrencyInstancesIndependent(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	for position, currency := range []string{"CNY", "USD"} {
		config, err := json.Marshal(map[string]string{"currency": currency, "publishableKey": "pk_test_" + currency, "secretKey": "sk_test_" + currency})
		require.NoError(t, err)
		client.PaymentProviderInstance.Create().SetName(currency).SetProviderKey("stripe").SetEnabled(true).SetSortOrder(position).SetConfig(string(config)).SaveX(ctx)
	}
	svc := &PaymentConfigService{entClient: client}
	options, err := svc.GetPaymentOptions(ctx, &PaymentConfig{Enabled: true})
	require.NoError(t, err)
	require.Len(t, options, 2)
	require.Equal(t, "CNY", options[0].Currency)
	require.Equal(t, "USD", options[1].Currency)
	require.Equal(t, "pk_test_USD", options[1].StripePublishableKey)
	encoded, err := json.Marshal(options)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "sk_test_")
	paymentSvc := &PaymentService{configService: svc}
	for _, option := range options {
		selected, err := paymentSvc.resolveRequestedPaymentInstance(ctx, CreateOrderRequest{PaymentType: "stripe", ProviderInstanceID: option.ProviderInstanceID})
		require.NoError(t, err)
		require.Equal(t, option.Currency, svc.pcInstancePaymentCurrency(selected))
	}
}

func TestPaymentProviderDisablePreservesPinnedCallbacksAndRejectsOtherInstanceSignature(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	selected := createPaymentOptionTestInstance(t, client, "X", RechargePolicy{}, 0)
	createPaymentOptionTestInstance(t, client, "Y", RechargePolicy{}, 1)
	createPaymentOptionPendingOrder(t, ctx, client, selected)
	configService := &PaymentConfigService{entClient: client}
	disabled := false
	_, err := configService.UpdateProviderInstance(ctx, selected.ID, UpdateProviderInstanceRequest{Enabled: &disabled})
	require.NoError(t, err)
	svc := &PaymentService{entClient: client, configService: configService, loadBalancer: payment.NewDefaultLoadBalancer(client, nil)}
	order := client.PaymentOrder.Query().OnlyX(ctx)
	providers, err := svc.GetWebhookProviders(ctx, "easypay", order.OutTradeNo)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	params := url.Values{"pid": {"X"}, "trade_no": {"gateway-trade"}, "out_trade_no": {order.OutTradeNo}, "type": {"wxpay"}, "money": {"88.00"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
	keys := make([]string, 0, len(params))
	for key := range params {
		if key != "sign_type" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params.Get(key))
	}
	sign := func(secret string) string {
		digest := md5.Sum([]byte(strings.Join(parts, "&") + secret))
		return hex.EncodeToString(digest[:])
	}
	params.Set("sign", sign("secret-X"))
	notification, err := providers[0].VerifyNotification(ctx, params.Encode(), nil)
	require.NoError(t, err)
	require.Equal(t, order.OutTradeNo, notification.OrderID)
	params.Set("sign", sign("secret-Y"))
	_, err = providers[0].VerifyNotification(ctx, params.Encode(), nil)
	require.ErrorContains(t, err, "signature")
	_, err = svc.resolveRequestedPaymentInstance(ctx, CreateOrderRequest{PaymentType: "wxpay", ProviderInstanceID: strconv.FormatInt(selected.ID, 10)})
	require.Equal(t, "PAYMENT_PROVIDER_UNAVAILABLE", infraerrors.Reason(err))
}

func TestPaymentProviderStripeCallbacksUseSelectedInstanceEvenWhenDisabled(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	var selected *dbent.PaymentProviderInstance
	for _, name := range []string{"X", "Y"} {
		config, err := json.Marshal(map[string]string{"secretKey": "sk_test_" + name, "webhookSecret": "whsec-" + name, "currency": "USD"})
		require.NoError(t, err)
		instance := client.PaymentProviderInstance.Create().SetName(name).SetProviderKey("stripe").SetEnabled(name == "Y").SetConfig(string(config)).SaveX(ctx)
		if name == "X" {
			selected = instance
		}
	}
	createPaymentOptionPendingOrder(t, ctx, client, selected)
	order := client.PaymentOrder.Query().OnlyX(ctx)
	svc := &PaymentService{entClient: client, loadBalancer: payment.NewDefaultLoadBalancer(client, nil)}
	providers, err := svc.GetWebhookProviders(ctx, "stripe", order.OutTradeNo)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	payload, err := json.Marshal(map[string]any{
		"id": "evt_test", "object": "event", "type": "payment_intent.succeeded", "api_version": stripe.APIVersion,
		"data": map[string]any{"object": map[string]any{"id": "pi_test", "object": "payment_intent", "amount": 8800, "currency": "usd", "metadata": map[string]string{"orderId": order.OutTradeNo}}},
	})
	require.NoError(t, err)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sign := func(secret string) map[string]string {
		signature := hmac.New(sha256.New, []byte(secret))
		_, _ = signature.Write([]byte(timestamp + "." + string(payload)))
		return map[string]string{"stripe-signature": "t=" + timestamp + ",v1=" + hex.EncodeToString(signature.Sum(nil))}
	}
	notification, err := providers[0].VerifyNotification(ctx, string(payload), sign("whsec-X"))
	require.NoError(t, err)
	require.Equal(t, order.OutTradeNo, notification.OrderID)
	require.Equal(t, 88.0, notification.Amount)
	_, err = providers[0].VerifyNotification(ctx, string(payload), sign("whsec-Y"))
	require.Error(t, err)
}
