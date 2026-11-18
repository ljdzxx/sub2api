package service

import (
	"math"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

const defaultBalanceRechargeMultiplier = 1.0

func normalizeBalanceRechargeMultiplier(multiplier float64) float64 {
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
		return defaultBalanceRechargeMultiplier
	}
	return multiplier
}

// normalizeSubscriptionUSDToCNYRate 将非法值归一为 0（换算关闭）。
// 与余额倍率不同，0 是合法状态：表示订阅保持 price 直付的存量行为。
func normalizeSubscriptionUSDToCNYRate(rate float64) float64 {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return 0
	}
	return rate
}

func calculateCreditedBalance(paymentAmount, multiplier float64) float64 {
	return decimal.NewFromFloat(paymentAmount).
		Mul(decimal.NewFromFloat(normalizeBalanceRechargeMultiplier(multiplier))).
		Round(2).
		InexactFloat64()
}

// easyPaySettledBalance converts only the excess receipt at the order's
// recharge multiplier. The quoted balance already includes its promotion.
func easyPaySettledBalance(o *dbent.PaymentOrder, expected, paid float64) float64 {
	multiplier, ok := o.PricingSnapshot["balance_recharge_multiplier"].(float64)
	if !ok {
		// Legacy orders retain the paid principal and fee-inclusive price.
		// Reverse the rounded-up fee to recover the pre-fee payment base.
		base := decimal.NewFromFloat(expected)
		if o.FeeRate > 0 {
			feeFactor := decimal.NewFromInt(1).Add(decimal.NewFromFloat(o.FeeRate).Shift(-2))
			base = base.Div(feeFactor).Truncate(2)
		}
		if base.IsPositive() {
			multiplier = decimal.NewFromFloat(paymentOrderAmountWithoutBonus(o)).Div(base).InexactFloat64()
		}
	}
	extra := decimal.NewFromFloat(paid).Sub(decimal.NewFromFloat(expected))
	credit := extra.Mul(decimal.NewFromFloat(normalizeBalanceRechargeMultiplier(multiplier))).Round(8)
	return decimal.NewFromFloat(o.Amount).Add(credit).InexactFloat64()
}

func calculateGatewayRefundAmount(orderAmount, payAmount, refundAmount float64, currency string) float64 {
	if orderAmount <= 0 || payAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	fractionDigits := int32(payment.CurrencyMaxFractionDigits(currency))
	if math.Abs(refundAmount-orderAmount) <= paymentAmountToleranceForCurrency(currency) {
		return decimal.NewFromFloat(payAmount).Round(fractionDigits).InexactFloat64()
	}
	return decimal.NewFromFloat(payAmount).
		Mul(decimal.NewFromFloat(refundAmount)).
		Div(decimal.NewFromFloat(orderAmount)).
		Round(fractionDigits).
		InexactFloat64()
}
