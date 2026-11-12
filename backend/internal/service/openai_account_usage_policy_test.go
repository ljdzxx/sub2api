package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateOpenAIAccountUsagePolicy(t *testing.T) {
	valid := []domain.OpenAINormalInputCacheRule{
		{TransferPercent: 0, ProbabilityPercent: 20},
		{TransferPercent: 10, ProbabilityPercent: 5},
		{TransferPercent: 50, ProbabilityPercent: 75},
	}
	require.NoError(t, validateOpenAIAccountUsagePolicy(PlatformOpenAI, AccountTypeAPIKey, true, valid))
	require.NoError(t, validateOpenAIAccountUsagePolicy(PlatformOpenAI, AccountTypeAPIKey, false, nil))
	require.NoError(t, validateOpenAIAccountUsagePolicy(PlatformOpenAI, AccountTypeAPIKey, false, valid))
	require.Error(t, validateOpenAIAccountUsagePolicy(PlatformOpenAI, AccountTypeAPIKey, false, []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 99}}))

	tests := []struct {
		name     string
		platform string
		typ      string
		rules    []domain.OpenAINormalInputCacheRule
	}{
		{name: "wrong platform", platform: PlatformAnthropic, typ: AccountTypeAPIKey, rules: valid},
		{name: "wrong account type", platform: PlatformOpenAI, typ: AccountTypeOAuth, rules: valid},
		{name: "no rules", platform: PlatformOpenAI, typ: AccountTypeAPIKey},
		{name: "transfer below range", platform: PlatformOpenAI, typ: AccountTypeAPIKey, rules: []domain.OpenAINormalInputCacheRule{{TransferPercent: -1, ProbabilityPercent: 100}}},
		{name: "transfer above range", platform: PlatformOpenAI, typ: AccountTypeAPIKey, rules: []domain.OpenAINormalInputCacheRule{{TransferPercent: 101, ProbabilityPercent: 100}}},
		{name: "zero probability", platform: PlatformOpenAI, typ: AccountTypeAPIKey, rules: []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 0}}},
		{name: "probability total below 100", platform: PlatformOpenAI, typ: AccountTypeAPIKey, rules: []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 99}}},
		{name: "probability total above 100", platform: PlatformOpenAI, typ: AccountTypeAPIKey, rules: []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 100}, {TransferPercent: 20, ProbabilityPercent: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, validateOpenAIAccountUsagePolicy(tt.platform, tt.typ, true, tt.rules))
		})
	}
}

func TestSelectOpenAINormalInputCacheRuleUsesContiguousProbabilityRanges(t *testing.T) {
	rules := []domain.OpenAINormalInputCacheRule{
		{TransferPercent: 0, ProbabilityPercent: 20},
		{TransferPercent: 10, ProbabilityPercent: 5},
		{TransferPercent: 50, ProbabilityPercent: 75},
	}

	for _, tc := range []struct {
		roll    int
		percent int
	}{
		{roll: 0, percent: 0},
		{roll: 19, percent: 0},
		{roll: 20, percent: 10},
		{roll: 24, percent: 10},
		{roll: 25, percent: 50},
		{roll: 99, percent: 50},
	} {
		rule, ok := selectOpenAINormalInputCacheRule(rules, tc.roll)
		require.True(t, ok)
		require.Equal(t, tc.percent, rule.TransferPercent)
	}
	_, ok := selectOpenAINormalInputCacheRule(rules, 100)
	require.False(t, ok)
}

func TestTransferOpenAINormalInputToCacheWritePreservesTotalAndReadCache(t *testing.T) {
	raw := OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 100, CacheCreationInputTokens: 100}

	effective := TransferOpenAINormalInputToCacheWrite(raw, 50)

	require.Equal(t, 1000, effective.InputTokens)
	require.Equal(t, 100, effective.CacheReadInputTokens)
	require.Equal(t, 500, effective.CacheCreationInputTokens)
	require.Equal(t, 400, effective.InputTokens-effective.CacheReadInputTokens-effective.CacheCreationInputTokens)
	require.Equal(t, OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 100, CacheCreationInputTokens: 100}, raw)
}

func TestTransferOpenAINormalInputToCacheWriteTruncatesFractionalTokens(t *testing.T) {
	raw := OpenAIUsage{InputTokens: 7, CacheReadInputTokens: 1, CacheCreationInputTokens: 1}

	effective := TransferOpenAINormalInputToCacheWrite(raw, 33)

	require.Equal(t, 2, effective.CacheCreationInputTokens)
	require.Equal(t, 4, effective.InputTokens-effective.CacheReadInputTokens-effective.CacheCreationInputTokens)
}

func TestOpenAIUsagePolicySnapshotAppliesAccountBeforeGroup(t *testing.T) {
	raw := OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 100, CacheCreationInputTokens: 100}
	policy := OpenAIUsagePolicySnapshot{
		Account: OpenAINormalInputCacheSelection{Enabled: true, TransferPercent: 50, ProbabilityPercent: 100},
		Group:   OpenAICacheReadPolicy{TransferCacheWriteToInput: true},
	}

	accountEffective, finalEffective := policy.Apply(raw)

	require.Equal(t, 500, accountEffective.CacheCreationInputTokens)
	require.Zero(t, finalEffective.CacheCreationInputTokens)
	require.Equal(t, 1000, finalEffective.InputTokens)
	require.Equal(t, 100, finalEffective.CacheReadInputTokens)
}

func TestOpenAIUsagePolicyExecutionBindsOneAccountSelection(t *testing.T) {
	execution := NewOpenAIUsagePolicyExecution(nil)
	account := &Account{
		ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		OpenAIExperimentalFeaturesEnabled: true,
		OpenAINormalInputCacheRules:       []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 100}},
	}

	execution.BindAccount(account)
	first := execution.Snapshot()
	execution.BindAccount(account)
	second := execution.Snapshot()

	require.Equal(t, first, second)
	require.Equal(t, OpenAINormalInputCacheSelection{
		Enabled: true, AccountID: 42, TransferPercent: 50, ProbabilityPercent: 100,
	}, first.Account)
}

func TestOpenAIUsagePolicyExecutionBeginsNewTurnButRetainsRetrySelection(t *testing.T) {
	execution := NewOpenAIUsagePolicyExecution(&APIKey{Group: &Group{
		Platform: PlatformOpenAI, OpenAIExperimentalFeaturesEnabled: true, CacheReadDeductionPercent: 10,
	}})
	draws := 0
	execution.roll = func(n int) int {
		require.Equal(t, 100, n)
		draws++
		return (draws - 1) * 99
	}
	account := &Account{
		ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		OpenAIExperimentalFeaturesEnabled: true,
		OpenAINormalInputCacheRules: []domain.OpenAINormalInputCacheRule{
			{TransferPercent: 0, ProbabilityPercent: 50}, {TransferPercent: 50, ProbabilityPercent: 50},
		},
	}
	execution.BindAccount(account)
	first := execution.Snapshot()
	execution.BindAccount(account)
	require.Equal(t, first, execution.Snapshot())
	require.Equal(t, 1, draws)

	execution.BeginTurn(account)
	second := execution.Snapshot()
	execution.BindAccount(account)
	require.Equal(t, second, execution.Snapshot())
	require.Equal(t, 2, draws)
	require.Zero(t, first.Account.TransferPercent)
	require.Equal(t, 50, second.Account.TransferPercent)
	require.Equal(t, first.Group, second.Group)
	require.Equal(t, float64(10), second.Group.DeductionPercent)
}
