package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCacheReadDeductionArithmetic(t *testing.T) {
	for _, tc := range []struct {
		name          string
		total, cached int
		percent       float64
		want          int
	}{
		{"example", 10000, 9630, 20, 7630}, {"fraction", 101, 100, 20, 79},
		{"decimal", 10001, 9630, 20.01, 7628}, {"floor zero", 100, 5, 20, 0},
		{"disabled", 100, 120, 0, 120}, {"zero input", 0, 12, 20, 0},
		{"malformed", 100, 120, 20, 80}, {"all", 100, 100, 100, 0},
		{"large", math.MaxInt, math.MaxInt, 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := OpenAIUsage{InputTokens: tc.total, CacheReadInputTokens: tc.cached}
			require.Equal(t, tc.want, DeductOpenAICacheRead(raw, tc.percent).CacheReadInputTokens)
			require.Equal(t, tc.cached, raw.CacheReadInputTokens)
		})
	}
	raw := OpenAIUsage{InputTokens: 1001, CacheReadInputTokens: 900, ImageInputTokens: 400, ImageCacheReadTokens: 333, CacheCreationInputTokens: 10}
	out := DeductOpenAICacheRead(raw, 20)
	require.Equal(t, 699, out.CacheReadInputTokens)
	require.Equal(t, 258, out.ImageCacheReadTokens)
	require.Equal(t, 1001, openAIUsageTokens(out).InputTokens+out.CacheReadInputTokens+out.CacheCreationInputTokens)
}

func TestCacheReadDeductionValidation(t *testing.T) {
	for _, p := range []float64{-1, 101, 0.001, math.NaN(), math.Inf(1)} {
		require.Error(t, normalizeGroupCacheReadDeduction(&Group{Platform: PlatformOpenAI, CacheReadDeductionPercent: p}))
	}
	g := &Group{Platform: PlatformAnthropic, CacheReadDeductionPercent: 20}
	require.NoError(t, normalizeGroupCacheReadDeduction(g))
	require.Zero(t, g.CacheReadDeductionPercent)
}

func TestCacheReadWireProtocols(t *testing.T) {
	for _, tc := range []struct{ body, path string }{
		{`{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":9630}}}`, "usage.input_tokens_details.cached_tokens"},
		{`{"usage":{"prompt_tokens":10000,"prompt_tokens_details":{"cached_tokens":9630}}}`, "usage.prompt_tokens_details.cached_tokens"},
		{`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":9630}}}}`, "response.usage.input_tokens_details.cached_tokens"},
	} {
		tr := NewCacheReadWireTransformer(20, false)
		for i := 0; i < 2; i++ {
			require.EqualValues(t, 7630, gjson.GetBytes(tr.Transform([]byte(tc.body)), tc.path).Int())
		}
	}
	tr := NewCacheReadWireTransformer(20, true)
	start := tr.Transform([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":370,"cache_read_input_tokens":9630,"cache_creation_input_tokens":0}}}`))
	require.EqualValues(t, 2370, gjson.GetBytes(start, "message.usage.input_tokens").Int())
	end := tr.Transform([]byte(`{"type":"message_delta","usage":{"output_tokens":20}}`))
	require.EqualValues(t, 7630, gjson.GetBytes(end, "usage.cache_read_input_tokens").Int())
	require.EqualValues(t, 2370, gjson.GetBytes(end, "usage.input_tokens").Int())
	require.EqualValues(t, 20, gjson.GetBytes(end, "usage.output_tokens").Int())
}

func TestCacheReadWireImageAndPartialUsage(t *testing.T) {
	tr := NewCacheReadWireTransformer(20, false)
	partial := tr.Transform([]byte(`{"usage":{"input_tokens_details":{"cached_tokens":900}}}`))
	require.False(t, gjson.GetBytes(partial, "usage.input_tokens_details.cached_tokens").Exists())
	body := []byte(`{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":900,"image_tokens":400,"cached_tokens_details":{"image_tokens":333,"text_tokens":567}}}}`)
	out := tr.Transform(body)
	require.EqualValues(t, 700, gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens").Int())
	require.EqualValues(t, 259, gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens_details.image_tokens").Int())
	require.EqualValues(t, 441, gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens_details.text_tokens").Int())
	require.EqualValues(t, 900, gjson.GetBytes(body, "usage.input_tokens_details.cached_tokens").Int())
}

func TestCacheReadDeductionRecordUsageKeepsUpstreamAccounting(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	billing := &openAIRecordUsageBillingRepoStub{}
	svc.usageBillingRepo = billing
	groupID := int64(4)
	user := &User{ID: 1}
	key := &APIKey{ID: 2, User: user, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1, CacheReadDeductionPercent: 20}}
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}}
	raw := OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 9630, OutputTokens: 20}
	result := &OpenAIForwardResult{RequestID: "deduction-test", Model: "gpt-5.1", Usage: raw}
	input := &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: account}
	var firstCost float64
	for i := 0; i < 2; i++ {
		require.NoError(t, svc.RecordUsage(context.Background(), input))
		require.Equal(t, raw, result.Usage)
		require.Equal(t, 7630, logs.lastLog.CacheReadTokens)
		require.Equal(t, 2370, logs.lastLog.InputTokens)
		require.NotNil(t, logs.lastLog.CacheReadAdjustment)
		require.Equal(t, raw, logs.lastLog.CacheReadAdjustment.Raw)
		require.Less(t, logs.lastLog.CacheReadAdjustment.UpstreamCost, logs.lastLog.TotalCost)
		require.InDelta(t, logs.lastLog.CacheReadAdjustment.UpstreamCost, billing.lastCmd.AccountQuotaCost, 1e-8)
		require.InDelta(t, logs.lastLog.ActualCost, billing.lastCmd.BalanceCost, 1e-8)
		require.NotNil(t, logs.lastLog.AccountStatsCost)
		require.Equal(t, logs.lastLog.CacheReadAdjustment.UpstreamCost, *logs.lastLog.AccountStatsCost)
		if i == 0 {
			firstCost = logs.lastLog.ActualCost
		} else {
			require.Equal(t, firstCost, logs.lastLog.ActualCost)
		}
	}
	p := &postUsageBillingParams{Cost: &CostBreakdown{TotalCost: 9}, UpstreamTotalCost: &firstCost}
	require.Equal(t, firstCost, p.upstreamTotalCost())
}

func TestCacheReadPrivateErrorDetails(t *testing.T) {
	body := `{"error":{"message":"failed","response":{"usage":{"input_tokens":100,"cached_tokens":90}}},"cache_read_adjustment":{"percent":20}}`
	public := publicUsageErrorBody(body)
	require.NotContains(t, public, "cached_tokens")
	require.NotContains(t, public, "adjustment")
	require.NotContains(t, public, "usage")
	require.Contains(t, public, "failed")
}
