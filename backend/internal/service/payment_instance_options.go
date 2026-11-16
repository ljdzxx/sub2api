package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type RechargePolicy struct {
	Mode   string              `json:"mode"`
	Tiers  []RechargeBonusTier `json:"tiers"`
	Notice string              `json:"notice"`
}

type PaymentOption struct {
	ID                   string         `json:"id"`
	ProviderInstanceID   string         `json:"provider_instance_id"`
	ProviderName         string         `json:"provider_name"`
	ProviderKey          string         `json:"provider_key"`
	PaymentType          string         `json:"payment_type"`
	DisplayName          string         `json:"display_name,omitempty"`
	SortOrder            int            `json:"sort_order"`
	Currency             string         `json:"currency"`
	SingleMin            float64        `json:"single_min"`
	SingleMax            float64        `json:"single_max"`
	DailyLimit           float64        `json:"daily_limit"`
	DailyUsed            float64        `json:"daily_used"`
	DailyRemaining       float64        `json:"daily_remaining"`
	FeeRate              float64        `json:"fee_rate"`
	Available            bool           `json:"available"`
	RechargePolicy       RechargePolicy `json:"recharge_policy"`
	PricingRevision      string         `json:"pricing_revision"`
	StripePublishableKey string         `json:"stripe_publishable_key,omitempty"`
}

func normalizeRechargePolicy(policy *RechargePolicy) (RechargePolicy, error) {
	if policy == nil {
		policy = &RechargePolicy{}
	}
	mode, valid := NormalizeRechargeBonusMode(policy.Mode)
	if !valid {
		return RechargePolicy{}, infraerrors.BadRequest("INVALID_RECHARGE_BONUS_MODE", "recharge bonus mode must be bonus or discount")
	}
	tiers, err := NormalizeRechargeBonusTiers(policy.Tiers)
	if err != nil {
		return RechargePolicy{}, infraerrors.BadRequest("INVALID_RECHARGE_BONUS_TIERS", err.Error())
	}
	if err := ValidateRechargeBonusTiersForMode(mode, tiers); err != nil {
		return RechargePolicy{}, infraerrors.BadRequest("INVALID_RECHARGE_BONUS_TIERS", err.Error())
	}
	if err := validateRechargeBonusNotice(policy.Notice); err != nil {
		return RechargePolicy{}, infraerrors.BadRequest("INVALID_RECHARGE_BONUS_NOTICE", err.Error())
	}
	return RechargePolicy{Mode: mode, Tiers: tiers, Notice: policy.Notice}, nil
}

func rechargePolicyMap(policy RechargePolicy) map[string]any {
	encoded, _ := json.Marshal(policy)
	var stored map[string]any
	_ = json.Unmarshal(encoded, &stored)
	return stored
}

func instanceRechargePolicy(inst *dbent.PaymentProviderInstance) (RechargePolicy, error) {
	policy := RechargePolicy{Mode: RechargeBonusModeBonus, Tiers: []RechargeBonusTier{}}
	if inst == nil || len(inst.RechargePolicy) == 0 {
		return policy, nil
	}
	rawMode, _ := inst.RechargePolicy["mode"].(string)
	policy.Mode, _ = NormalizeRechargeBonusMode(rawMode)
	policy.Notice, _ = inst.RechargePolicy["notice"].(string)
	encoded, err := json.Marshal(inst.RechargePolicy["tiers"])
	if err != nil {
		return policy, err
	}
	for _, tier := range parseRechargeBonusTiers(string(encoded)) {
		if policy.Mode == RechargeBonusModeDiscount && tier.BonusPercent >= 100 {
			continue
		}
		policy.Tiers = append(policy.Tiers, tier)
	}
	return policy, nil
}

func instancePaymentTypes(inst *dbent.PaymentProviderInstance) []string {
	if inst == nil || !inst.Enabled {
		return nil
	}
	switch inst.ProviderKey {
	case payment.TypeStripe, payment.TypeAirwallex:
		return []string{inst.ProviderKey}
	case payment.TypeAlipay, payment.TypeWxpay:
		if inst.SupportedTypes == "" || payment.InstanceSupportsType(inst.SupportedTypes, inst.ProviderKey) {
			return []string{inst.ProviderKey}
		}
		return nil
	case payment.TypeEasyPay:
		return NormalizeVisibleMethods(splitTypes(inst.SupportedTypes))
	default:
		return nil
	}
}

func instanceSupportsCheckoutType(inst *dbent.PaymentProviderInstance, paymentType string) bool {
	for _, candidate := range instancePaymentTypes(inst) {
		if candidate == NormalizeVisibleMethod(paymentType) {
			return true
		}
	}
	return false
}

func paymentInstancePricingRevision(inst *dbent.PaymentProviderInstance, cfg *PaymentConfig, policy RechargePolicy, currency string) string {
	encoded, _ := json.Marshal(struct {
		InstanceID                                                  int64
		ProviderKey                                                 string
		Currency                                                    string
		Policy                                                      RechargePolicy
		Multiplier, FeeRate, SubscriptionRate, MinAmount, MaxAmount float64
	}{InstanceID: inst.ID, ProviderKey: inst.ProviderKey, Currency: currency, Policy: policy,
		Multiplier: cfg.BalanceRechargeMultiplier, FeeRate: cfg.RechargeFeeRate, SubscriptionRate: cfg.SubscriptionUSDToCNYRate,
		MinAmount: cfg.MinAmount, MaxAmount: cfg.MaxAmount})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (s *PaymentConfigService) GetPaymentOptions(ctx context.Context, cfg *PaymentConfig) ([]PaymentOption, error) {
	options := make([]PaymentOption, 0)
	if !cfg.Enabled {
		return options, nil
	}
	instances, err := s.entClient.PaymentProviderInstance.Query().
		Where(paymentproviderinstance.EnabledEQ(true)).
		Order(paymentproviderinstance.BySortOrder(), paymentproviderinstance.ByID()).All(ctx)
	if err != nil {
		return nil, err
	}
	usage, err := paymentInstanceDailyUsage(ctx, s.entClient, instances)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		policy, err := instanceRechargePolicy(inst)
		if err != nil {
			return nil, fmt.Errorf("invalid recharge policy for provider %d: %w", inst.ID, err)
		}
		currency := s.pcInstancePaymentCurrency(inst)
		instanceID := strconv.FormatInt(inst.ID, 10)
		types := instancePaymentTypes(inst)
		sort.SliceStable(types, func(left, right int) bool {
			return paymentOptionTypeOrder(types[left]) < paymentOptionTypeOrder(types[right])
		})
		for _, paymentType := range types {
			limits := paymentInstanceChannelLimits(inst, paymentType)
			remaining := 0.0
			if limits.DailyLimit > 0 {
				remaining = math.Max(0, limits.DailyLimit-usage[instanceID])
			}
			option := PaymentOption{
				ID: instanceID + ":" + paymentType, ProviderInstanceID: instanceID,
				ProviderName: inst.Name, ProviderKey: inst.ProviderKey, PaymentType: paymentType,
				DisplayName: s.pcInstanceEasyPayCustomMethodDisplayName(inst, paymentType), SortOrder: inst.SortOrder,
				Currency: currency, SingleMin: limits.SingleMin, SingleMax: limits.SingleMax,
				DailyLimit: limits.DailyLimit, DailyUsed: usage[instanceID], DailyRemaining: remaining,
				Available: limits.DailyLimit <= 0 || remaining > 0, FeeRate: cfg.RechargeFeeRate,
				RechargePolicy: policy, PricingRevision: paymentInstancePricingRevision(inst, cfg, policy, currency),
			}
			if inst.ProviderKey == payment.TypeStripe {
				config, err := s.decryptConfig(inst.Config)
				if err != nil {
					return nil, err
				}
				option.StripePublishableKey = config[payment.ConfigKeyPublishableKey]
			}
			options = append(options, option)
		}
	}
	return options, nil
}

func paymentOptionTypeOrder(paymentType string) int {
	switch paymentType {
	case payment.TypeWxpay:
		return 0
	case payment.TypeAlipay:
		return 1
	default:
		return 2
	}
}

func paymentInstanceChannelLimits(inst *dbent.PaymentProviderInstance, paymentType string) payment.ChannelLimits {
	var all payment.InstanceLimits
	if err := json.Unmarshal([]byte(inst.Limits), &all); err != nil {
		return payment.ChannelLimits{}
	}
	if limits, ok := all[paymentType]; ok {
		return limits
	}
	for key, limits := range all {
		if NormalizeVisibleMethod(key) == paymentType {
			return limits
		}
	}
	return payment.ChannelLimits{}
}

func paymentInstanceDailyUsage(ctx context.Context, client *dbent.Client, instances []*dbent.PaymentProviderInstance) (map[string]float64, error) {
	usage := make(map[string]float64, len(instances))
	if len(instances) == 0 {
		return usage, nil
	}
	ids := make([]string, 0, len(instances))
	for _, inst := range instances {
		ids = append(ids, strconv.FormatInt(inst.ID, 10))
	}
	var rows []struct {
		InstanceID string  `json:"provider_instance_id"`
		Sum        float64 `json:"sum"`
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	err := client.PaymentOrder.Query().Where(
		paymentorder.ProviderInstanceIDIn(ids...),
		paymentorder.Or(
			paymentorder.And(paymentorder.StatusEQ(OrderStatusPending), paymentorder.ExpiresAtGT(now)),
			paymentorder.And(paymentorder.StatusIn(OrderStatusPaid, OrderStatusCompleted, OrderStatusRecharging), paymentorder.PaidAtGTE(start)),
		),
	).GroupBy(paymentorder.FieldProviderInstanceID).Aggregate(dbent.Sum(paymentorder.FieldPayAmount)).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		usage[row.InstanceID] = row.Sum
	}
	return usage, nil
}

func (s *PaymentService) resolveRequestedPaymentInstance(ctx context.Context, req CreateOrderRequest) (*dbent.PaymentProviderInstance, error) {
	if s.configService == nil || s.configService.entClient == nil {
		return nil, nil
	}
	query := s.configService.entClient.PaymentProviderInstance.Query().Where(paymentproviderinstance.EnabledEQ(true))
	if raw := strings.TrimSpace(req.ProviderInstanceID); raw != "" {
		instanceID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || instanceID <= 0 {
			return nil, infraerrors.BadRequest("INVALID_PAYMENT_PROVIDER", "invalid payment provider instance")
		}
		query = query.Where(paymentproviderinstance.IDEQ(instanceID))
	}
	instances, err := query.All(ctx)
	if err != nil {
		return nil, err
	}
	var selected *dbent.PaymentProviderInstance
	for _, inst := range instances {
		if !instanceSupportsCheckoutType(inst, req.PaymentType) {
			continue
		}
		if selected != nil {
			return nil, infraerrors.Conflict("PAYMENT_PROVIDER_REQUIRED", "select a payment provider and retry")
		}
		selected = inst
	}
	if selected == nil {
		return nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_UNAVAILABLE", "selected payment provider is unavailable")
	}
	return selected, nil
}

func (s *PaymentService) selectPinnedPaymentInstance(ctx context.Context, inst *dbent.PaymentProviderInstance, req CreateOrderRequest, payAmount float64) (*payment.InstanceSelection, error) {
	limits := paymentInstanceChannelLimits(inst, req.PaymentType)
	if (limits.SingleMin > 0 && payAmount < limits.SingleMin) || (limits.SingleMax > 0 && payAmount > limits.SingleMax) {
		return nil, infraerrors.BadRequest("PAYMENT_INSTANCE_AMOUNT_OUT_OF_RANGE", "amount is outside the selected provider limits")
	}
	usage, err := paymentInstanceDailyUsage(ctx, s.configService.entClient, []*dbent.PaymentProviderInstance{inst})
	if err != nil {
		return nil, err
	}
	instanceID := strconv.FormatInt(inst.ID, 10)
	if limits.DailyLimit > 0 && usage[instanceID]+payAmount > limits.DailyLimit {
		return nil, infraerrors.TooManyRequests("NO_AVAILABLE_INSTANCE", "selected payment provider daily limit reached")
	}
	config, err := s.configService.decryptConfig(inst.Config)
	if err != nil {
		return nil, err
	}
	if config == nil {
		config = map[string]string{}
	}
	if inst.PaymentMode != "" {
		config["paymentMode"] = inst.PaymentMode
	}
	return &payment.InstanceSelection{InstanceID: instanceID, ProviderKey: inst.ProviderKey, Config: config, SupportedTypes: inst.SupportedTypes, PaymentMode: inst.PaymentMode}, nil
}

func selectedStripePublishableKey(sel *payment.InstanceSelection) string {
	if sel == nil || sel.ProviderKey != payment.TypeStripe {
		return ""
	}
	return sel.Config[payment.ConfigKeyPublishableKey]
}

func (s *PaymentService) validatePinnedPaymentInTx(ctx context.Context, tx *dbent.Tx, req CreateOrderRequest, cfg *PaymentConfig, payAmount float64) error {
	instanceID, err := strconv.ParseInt(req.ProviderInstanceID, 10, 64)
	if err != nil {
		return infraerrors.BadRequest("INVALID_PAYMENT_PROVIDER", "invalid payment provider instance")
	}
	inst, err := tx.PaymentProviderInstance.Query().Where(
		paymentproviderinstance.IDEQ(instanceID),
		func(selector *entsql.Selector) {
			if selector.Dialect() == dialect.Postgres {
				selector.ForUpdate()
			}
		},
	).Only(ctx)
	if err != nil {
		return err
	}
	if !instanceSupportsCheckoutType(inst, req.PaymentType) {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_UNAVAILABLE", "selected payment provider is unavailable")
	}
	policy, err := instanceRechargePolicy(inst)
	if err != nil {
		return err
	}
	if paymentInstancePricingRevision(inst, cfg, policy, s.configService.pcInstancePaymentCurrency(inst)) != req.PricingRevision {
		return infraerrors.Conflict("PAYMENT_PRICING_CHANGED", "payment pricing changed; review and confirm again")
	}
	limits := paymentInstanceChannelLimits(inst, req.PaymentType)
	if (limits.SingleMin > 0 && payAmount < limits.SingleMin) || (limits.SingleMax > 0 && payAmount > limits.SingleMax) {
		return infraerrors.BadRequest("PAYMENT_INSTANCE_AMOUNT_OUT_OF_RANGE", "amount is outside the selected provider limits")
	}
	if limits.DailyLimit > 0 {
		usage, err := paymentInstanceDailyUsage(ctx, tx.Client(), []*dbent.PaymentProviderInstance{inst})
		if err != nil {
			return err
		}
		if usage[req.ProviderInstanceID]+payAmount > limits.DailyLimit {
			return infraerrors.TooManyRequests("NO_AVAILABLE_INSTANCE", "selected payment provider daily limit reached")
		}
	}
	return nil
}

func PaymentOrderProviderName(order *dbent.PaymentOrder) string {
	if order == nil {
		return ""
	}
	return psSnapshotStringValue(order.PricingSnapshot["provider_name"])
}

func (s *PaymentService) GetOrderStripePublishableKey(ctx context.Context, order *dbent.PaymentOrder) string {
	if order == nil || s.configService == nil || s.entClient == nil {
		return ""
	}
	inst, err := s.getOrderProviderInstance(ctx, order)
	if err != nil || inst == nil || inst.ProviderKey != payment.TypeStripe {
		return ""
	}
	config, err := s.configService.decryptConfig(inst.Config)
	if err != nil {
		return ""
	}
	return config[payment.ConfigKeyPublishableKey]
}
