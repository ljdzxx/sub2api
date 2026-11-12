package service

import (
	"context"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
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
	g := &Group{Platform: PlatformAnthropic, CacheReadDeductionPercent: 20, TransferCacheWriteToInput: true}
	require.NoError(t, normalizeGroupCacheReadDeduction(g))
	require.Zero(t, g.CacheReadDeductionPercent)
	require.False(t, g.TransferCacheWriteToInput)
}

func TestOpenAICacheReadDeductionRequiresExperimentalEnable(t *testing.T) {
	key := &APIKey{Group: &Group{Platform: PlatformOpenAI, CacheReadDeductionPercent: 20}}
	require.Zero(t, OpenAICacheReadDeductionPercent(key))
	key.Group.OpenAIExperimentalFeaturesEnabled = true
	require.Equal(t, 20.0, OpenAICacheReadDeductionPercent(key))
}

func TestCacheReadAmplificationArithmeticAndThreshold(t *testing.T) {
	raw := OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 9700, CacheCreationInputTokens: 100}
	require.Equal(t, raw, AmplifyOpenAICacheRead(raw, 20, 10001))
	out := AmplifyOpenAICacheRead(raw, 20, 10000)
	require.Equal(t, 12000, out.InputTokens)
	require.Equal(t, 11700, out.CacheReadInputTokens)
	require.Equal(t, 100, out.CacheCreationInputTokens)
	require.Equal(t, 200, openAIUsageTokens(out).InputTokens)
}

func TestCacheReadPolicyAppliesDeductionBeforeAmplification(t *testing.T) {
	raw := OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 9700, CacheCreationInputTokens: 100}
	out := ApplyOpenAICacheReadPolicy(raw, OpenAICacheReadPolicy{
		DeductionPercent: 20, AmplificationPercent: 20, AmplificationMinInputTokens: 10000,
	})
	require.Equal(t, 12000, out.InputTokens)
	require.Equal(t, 9700, out.CacheReadInputTokens)
	require.Equal(t, 2200, openAIUsageTokens(out).InputTokens)
}

func TestCacheReadPolicyTransfersCacheWriteAfterOtherAdjustments(t *testing.T) {
	raw := OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 9700, CacheCreationInputTokens: 100}
	out := ApplyOpenAICacheReadPolicy(raw, OpenAICacheReadPolicy{
		DeductionPercent: 20, AmplificationPercent: 20, AmplificationMinInputTokens: 10000,
		TransferCacheWriteToInput: true,
	})
	require.Equal(t, 12000, out.InputTokens)
	require.Equal(t, 9700, out.CacheReadInputTokens)
	require.Zero(t, out.CacheCreationInputTokens)
	require.Equal(t, 2300, openAIUsageTokens(out).InputTokens)
	require.Equal(t, 100, raw.CacheCreationInputTokens)
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

func TestCacheReadWireAmplificationUpdatesTotalAndCache(t *testing.T) {
	policy := OpenAICacheReadPolicy{AmplificationPercent: 20, AmplificationMinInputTokens: 10000}
	tr := NewCacheReadPolicyWireTransformer(policy, false)
	out := tr.Transform([]byte(`{"usage":{"input_tokens":10000,"output_tokens":20,"total_tokens":10020,"input_tokens_details":{"cached_tokens":9700,"cache_write_tokens":100}}}`))
	require.EqualValues(t, 12000, gjson.GetBytes(out, "usage.input_tokens").Int())
	require.EqualValues(t, 11700, gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens").Int())
	require.EqualValues(t, 12020, gjson.GetBytes(out, "usage.total_tokens").Int())

	below := NewCacheReadPolicyWireTransformer(policy, false).Transform([]byte(`{"usage":{"input_tokens":9999,"input_tokens_details":{"cached_tokens":9700}}}`))
	require.EqualValues(t, 9999, gjson.GetBytes(below, "usage.input_tokens").Int())
	require.EqualValues(t, 9700, gjson.GetBytes(below, "usage.input_tokens_details.cached_tokens").Int())
}

func TestCacheWriteTransferUpdatesJSONAndSSEUsage(t *testing.T) {
	policy := OpenAICacheReadPolicy{TransferCacheWriteToInput: true}
	body := []byte(`{"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":300}}}`)
	out := NewCacheReadPolicyWireTransformer(policy, false).Transform(body)
	require.EqualValues(t, 1000, gjson.GetBytes(out, "usage.input_tokens").Int())
	require.EqualValues(t, 0, gjson.GetBytes(out, "usage.input_tokens_details.cache_write_tokens").Int())
	require.EqualValues(t, 1020, gjson.GetBytes(out, "usage.total_tokens").Int())

	sse := []byte("event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1000,"output_tokens":20,"input_tokens_details":{"cached_tokens":200,"cache_write_tokens":300}}}}` + "\n\n")
	sseOut := NewCacheReadPolicyWireTransformer(policy, false).TransformSSEEvent(sse)
	require.Contains(t, string(sseOut), `"cache_write_tokens":0`)
}

func TestCacheWriteTransferClearsProvisionalUsageWithoutInputTotal(t *testing.T) {
	policy := OpenAICacheReadPolicy{TransferCacheWriteToInput: true}
	tr := NewCacheReadPolicyWireTransformer(policy, false)

	provisional := tr.Transform([]byte(`{"type":"response.output_text.done","usage":{"input_tokens_details":{"cache_write_tokens":300}}}`))
	require.EqualValues(t, 0, gjson.GetBytes(provisional, "usage.input_tokens_details.cache_write_tokens").Int())

	sse := []byte("event: response.output_text.done\n" +
		`data: {"type":"response.output_text.done","usage":{"cache_creation_input_tokens":300}}` + "\n\n")
	sseOut := NewCacheReadPolicyWireTransformer(policy, false).TransformSSEEvent(sse)
	require.Contains(t, string(sseOut), `"cache_creation_input_tokens":0`)
}

func TestAccountNormalInputCachePolicyUpdatesJSONAndAnthropicWireUsage(t *testing.T) {
	account := &Account{
		ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		OpenAIExperimentalFeaturesEnabled: true,
		OpenAINormalInputCacheRules:       []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 100}},
	}
	execution := NewOpenAIUsagePolicyExecution(nil)
	execution.BindAccount(account)

	jsonBody := []byte(`{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":100}}}`)
	jsonOut := NewOpenAIUsagePolicyWireTransformer(execution, false).Transform(jsonBody)
	require.EqualValues(t, 1000, gjson.GetBytes(jsonOut, "usage.input_tokens").Int())
	require.EqualValues(t, 100, gjson.GetBytes(jsonOut, "usage.input_tokens_details.cached_tokens").Int())
	require.EqualValues(t, 500, gjson.GetBytes(jsonOut, "usage.input_tokens_details.cache_write_tokens").Int())

	withoutCacheWrite := []byte(`{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":100}}}`)
	injected := NewOpenAIUsagePolicyWireTransformer(execution, false).Transform(withoutCacheWrite)
	require.EqualValues(t, 450, gjson.GetBytes(injected, "usage.input_tokens_details.cache_write_tokens").Int())

	anthropicBody := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":800,"cache_read_input_tokens":100,"cache_creation_input_tokens":100}}}`)
	anthropicOut := NewOpenAIUsagePolicyWireTransformer(execution, true).Transform(anthropicBody)
	require.EqualValues(t, 400, gjson.GetBytes(anthropicOut, "message.usage.input_tokens").Int())
	require.EqualValues(t, 100, gjson.GetBytes(anthropicOut, "message.usage.cache_read_input_tokens").Int())
	require.EqualValues(t, 500, gjson.GetBytes(anthropicOut, "message.usage.cache_creation_input_tokens").Int())
}

func TestCacheReadDeductionRecordUsageKeepsUpstreamAccounting(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	billing := &openAIRecordUsageBillingRepoStub{}
	svc.usageBillingRepo = billing
	groupID := int64(4)
	user := &User{ID: 1}
	key := &APIKey{ID: 2, User: user, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1, OpenAIExperimentalFeaturesEnabled: true, CacheReadDeductionPercent: 20}}
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

func TestCacheReadAmplificationRecordUsageKeepsOrdinaryInputAndUpstreamAccounting(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	billing := &openAIRecordUsageBillingRepoStub{}
	svc.usageBillingRepo = billing
	groupID := int64(14)
	user := &User{ID: 11}
	key := &APIKey{ID: 12, User: user, GroupID: &groupID, Group: &Group{
		ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1, OpenAIExperimentalFeaturesEnabled: true,
		CacheReadAmplificationPercent: 20, CacheReadAmplificationMinInputTokens: 10000,
	}}
	account := &Account{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}}
	raw := OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 9700, CacheCreationInputTokens: 100, OutputTokens: 20}
	result := &OpenAIForwardResult{RequestID: "amplification-test", Model: "gpt-5.1", Usage: raw}

	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: account}))
	require.Equal(t, raw, result.Usage)
	require.Equal(t, 200, logs.lastLog.InputTokens)
	require.Equal(t, 11700, logs.lastLog.CacheReadTokens)
	require.Equal(t, 100, logs.lastLog.CacheCreationTokens)
	require.NotNil(t, logs.lastLog.CacheReadAdjustment)
	require.Equal(t, 2, logs.lastLog.CacheReadAdjustment.Version)
	require.Equal(t, 20.0, logs.lastLog.CacheReadAdjustment.AmplificationPercent)
	require.EqualValues(t, 10000, logs.lastLog.CacheReadAdjustment.AmplificationMinInputTokens)
	require.Equal(t, 12000, logs.lastLog.CacheReadAdjustment.Effective.InputTokens)
	require.Greater(t, logs.lastLog.TotalCost, logs.lastLog.CacheReadAdjustment.UpstreamCost)
	require.InDelta(t, logs.lastLog.CacheReadAdjustment.UpstreamCost, billing.lastCmd.AccountQuotaCost, 1e-8)
}

func TestCacheWriteTransferRecordUsageKeepsRawUpstreamAccounting(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	billing := &openAIRecordUsageBillingRepoStub{}
	svc.usageBillingRepo = billing
	groupID := int64(24)
	user := &User{ID: 21}
	key := &APIKey{ID: 22, User: user, GroupID: &groupID, Group: &Group{
		ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1, OpenAIExperimentalFeaturesEnabled: true,
		TransferCacheWriteToInput: true,
	}}
	account := &Account{ID: 23, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}}
	raw := OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 200, CacheCreationInputTokens: 300, OutputTokens: 20}
	result := &OpenAIForwardResult{RequestID: "cache-write-transfer-test", Model: "gpt-5.1", Usage: raw}

	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: account}))
	require.Equal(t, raw, result.Usage)
	require.Equal(t, 800, logs.lastLog.InputTokens)
	require.Equal(t, 200, logs.lastLog.CacheReadTokens)
	require.Zero(t, logs.lastLog.CacheCreationTokens)
	require.NotNil(t, logs.lastLog.CacheReadAdjustment)
	require.True(t, logs.lastLog.CacheReadAdjustment.TransferCacheWriteToInput)
	require.Equal(t, raw, logs.lastLog.CacheReadAdjustment.Raw)
	require.Zero(t, logs.lastLog.CacheReadAdjustment.Effective.CacheCreationInputTokens)
	require.InDelta(t, logs.lastLog.CacheReadAdjustment.UpstreamCost, billing.lastCmd.AccountQuotaCost, 1e-8)
}

func TestAccountNormalInputCacheRecordUsageUsesCapturedSelectionAndRawUpstreamAccounting(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	billing := &openAIRecordUsageBillingRepoStub{}
	svc.usageBillingRepo = billing
	groupID := int64(34)
	user := &User{ID: 31}
	key := &APIKey{ID: 32, User: user, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1}}
	account := &Account{
		ID: 33, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		OpenAIExperimentalFeaturesEnabled: true,
		OpenAINormalInputCacheRules:       []domain.OpenAINormalInputCacheRule{{TransferPercent: 0, ProbabilityPercent: 50}, {TransferPercent: 50, ProbabilityPercent: 50}},
		Extra:                             map[string]any{"quota_limit": 100.0},
	}
	raw := OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 100, CacheCreationInputTokens: 100, OutputTokens: 20}
	selection := OpenAINormalInputCacheSelection{Enabled: true, AccountID: account.ID, TransferPercent: 50, ProbabilityPercent: 50}
	result := &OpenAIForwardResult{
		RequestID: "account-normal-input-cache-test", Model: "gpt-5.1", Usage: raw,
		UsagePolicy: OpenAIUsagePolicySnapshot{Account: selection},
	}

	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: account}))
	require.Equal(t, raw, result.Usage)
	require.Equal(t, 400, logs.lastLog.InputTokens)
	require.Equal(t, 100, logs.lastLog.CacheReadTokens)
	require.Equal(t, 500, logs.lastLog.CacheCreationTokens)
	require.NotNil(t, logs.lastLog.CacheReadAdjustment)
	require.Equal(t, 3, logs.lastLog.CacheReadAdjustment.Version)
	require.Equal(t, raw, logs.lastLog.CacheReadAdjustment.Raw)
	require.Equal(t, &selection, logs.lastLog.CacheReadAdjustment.AccountPolicy)
	require.NotNil(t, logs.lastLog.CacheReadAdjustment.AccountEffective)
	require.Equal(t, 500, logs.lastLog.CacheReadAdjustment.AccountEffective.CacheCreationInputTokens)
	require.Equal(t, logs.lastLog.CacheReadAdjustment.UpstreamCost, billing.lastCmd.AccountQuotaCost)
	require.NotNil(t, logs.lastLog.AccountStatsCost)
	require.Equal(t, logs.lastLog.CacheReadAdjustment.UpstreamCost, *logs.lastLog.AccountStatsCost)
}

func TestCacheReadPrivateErrorDetails(t *testing.T) {
	body := `{"error":{"message":"failed","response":{"usage":{"input_tokens":100,"cached_tokens":90}}},"cache_read_adjustment":{"percent":20}}`
	public := publicUsageErrorBody(body)
	require.NotContains(t, public, "cached_tokens")
	require.NotContains(t, public, "adjustment")
	require.NotContains(t, public, "usage")
	require.Contains(t, public, "failed")
}
