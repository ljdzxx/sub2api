//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIFaultFallback_RequiresReplayableUpstreamFailure(t *testing.T) {
	reserve := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIFaultFallbackOnlyExtraKey: true}}
	for _, status := range []int{0, 200, 400, 401, 403, 404, 413, 429, 500, 502, 503, 504, 529, 599, 600} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx := WithOpenAIFaultFallback(context.Background(), &UpstreamFailoverError{StatusCode: status})
			want := status == 429 || (status >= 500 && status <= 599)
			require.Equal(t, want, openAIFaultFallbackAccountAllowed(ctx, reserve))
		})
	}
	for _, failure := range []*UpstreamFailoverError{
		nil,
		{StatusCode: 503, NextAccountAction: NextAccountStop},
		{StatusCode: 503, Stage: GatewayFailureStageAccountAuth},
	} {
		require.False(t, openAIFaultFallbackAccountAllowed(WithOpenAIFaultFallback(context.Background(), failure), reserve))
	}
	ctx := WithOpenAIFaultFallback(context.Background(), &UpstreamFailoverError{StatusCode: 502})
	require.False(t, openAIFaultFallbackAccountAllowed(WithOpenAIFaultFallback(ctx, nil), reserve), "new request must reset authorization")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.False(t, openAIFaultFallbackAccountAllowed(canceled, reserve))
	require.False(t, openAIFaultFallbackAccountAllowed(nil, reserve))
}

func TestOpenAIFaultFallback_SchedulingIsolation(t *testing.T) {
	// Cover both legacy implementations and advanced hard/weighted affinity.
	for _, mode := range []string{"legacy", "legacy_batch", "advanced", "advanced_weighted"} {
		t.Run(mode, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			groupID := int64(8310)
			accounts := newLegacySchedulerDecisionTestAccounts(groupID, true)
			primaryID, reserveID := accounts[0].ID, accounts[1].ID
			accounts[0].Priority = 100
			accounts[1].Priority = 0 // A reserve must be excluded even if it ranks first.
			accounts[1].Extra[OpenAIFaultFallbackOnlyExtraKey] = true
			svc := newLegacySchedulerDecisionTestService(accounts, mode != "legacy", schedulerTestConcurrencyCache{})
			if mode == "advanced" || mode == "advanced_weighted" {
				svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true", fmt.Sprint(mode == "advanced_weighted"))
				svc.cfg.Gateway.OpenAIWS.LBTopK = 7
				svc.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
			}
			ctx := context.Background()
			cache := svc.cache.(*schedulerTestGatewayCache)
			selectAccount := func(ctx context.Context, previous, session string, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
				selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, previous, session, "gpt-5.1", excluded,
					OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, true, true)
				return selection, err
			}
			assertSelected := func(ctx context.Context, previous, session string, excluded map[int64]struct{}, want int64) {
				t.Helper()
				selection, err := selectAccount(ctx, previous, session, excluded)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, want, selection.Account.ID)
				releaseLegacySchedulerDecisionSelection(selection)
			}
			assertSelected(ctx, "", "", nil, primaryID)
			cache.sessionBindings = map[string]int64{"openai:reserve-session": reserveID}
			assertSelected(ctx, "", "reserve-session", nil, primaryID)
			require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, "resp_reserve", reserveID, time.Hour))
			assertSelected(ctx, "resp_reserve", "", nil, primaryID)

			excluded := map[int64]struct{}{primaryID: {}}
			selection, err := selectAccount(ctx, "", "", excluded)
			require.Error(t, err, "exclusion/empty primary pool alone must not unlock a reserve")
			require.Nil(t, selection)
			failedCtx := WithOpenAIFaultFallback(ctx, &UpstreamFailoverError{StatusCode: 503})
			assertSelected(failedCtx, "", "reserve-session", excluded, reserveID)
			// Success can leave sticky data behind; the next request still starts with primary.
			assertSelected(ctx, "", "reserve-session", nil, primaryID)

			// An eligible failure does not override model/group/disabled-account gates.
			accounts[1].Schedulable = false
			selection, err = selectAccount(failedCtx, "", "", excluded)
			require.Error(t, err)
			require.Nil(t, selection)
		})
	}
}

func TestOpenAIFaultFallback_BusyPrimaryDoesNotUnlockReserve(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	groupID := int64(8311)
	accounts := newLegacySchedulerDecisionTestAccounts(groupID, false)
	accounts[1].Extra[OpenAIFaultFallbackOnlyExtraKey] = true
	var acquired []int64
	svc := newLegacySchedulerDecisionTestService(accounts, true, schedulerTestConcurrencyCache{
		acquiredIDs:    &acquired,
		acquireResults: map[int64]bool{accounts[0].ID: false},
		loadMap:        map[int64]*AccountLoadInfo{accounts[0].ID: {AccountID: accounts[0].ID, LoadRate: 100}},
	})
	selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	if err == nil {
		require.NotNil(t, selection)
		require.Equal(t, accounts[0].ID, selection.Account.ID)
		releaseLegacySchedulerDecisionSelection(selection)
	}
	require.NotContains(t, acquired, accounts[1].ID)
}

func TestOpenAIFaultFallback_Configuration(t *testing.T) {
	require.NoError(t, validateOpenAIFaultFallbackExtra(PlatformOpenAI, AccountTypeAPIKey, nil))
	require.NoError(t, validateOpenAIFaultFallbackExtra(PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAIFaultFallbackOnlyExtraKey: true}))
	require.Error(t, validateOpenAIFaultFallbackExtra(PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAIFaultFallbackOnlyExtraKey: "true"}))
	require.Error(t, validateOpenAIFaultFallbackExtra(PlatformOpenAI, AccountTypeOAuth, map[string]any{OpenAIFaultFallbackOnlyExtraKey: true}))
	require.Error(t, validateOpenAIFaultFallbackExtra(PlatformAnthropic, AccountTypeAPIKey, map[string]any{OpenAIFaultFallbackOnlyExtraKey: true}))
	for _, account := range []*Account{nil, {}, {Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, {Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIFaultFallbackOnlyExtraKey: true}}} {
		require.False(t, account.IsOpenAIFaultFallbackOnly())
		require.True(t, openAIFaultFallbackAccountAllowed(context.Background(), account))
	}
}

func TestOpenAIFaultFallback_AdminUpdateRequiresExplicitDisable(t *testing.T) {
	repo := &updateAccountOveragesRepoStub{account: &Account{
		ID: 8340, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Extra: map[string]any{OpenAIFaultFallbackOnlyExtraKey: true, OpenAIFaultFallbackStatusCodesExtraKey: []int{401, 503}},
	}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 8340, &UpdateAccountInput{Extra: map[string]any{}})
	require.NoError(t, err)
	require.True(t, updated.IsOpenAIFaultFallbackOnly(), "old-client extra replacement must preserve reserve admission")
	require.Equal(t, []int{401, 503}, updated.Extra[OpenAIFaultFallbackStatusCodesExtraKey])
	updated, err = svc.UpdateAccount(context.Background(), 8340, &UpdateAccountInput{Extra: map[string]any{OpenAIFaultFallbackOnlyExtraKey: false}})
	require.NoError(t, err)
	require.False(t, updated.IsOpenAIFaultFallbackOnly())
}

func TestOpenAIFaultFallback_ConfiguredCodesReplaceDefaults(t *testing.T) {
	reserve := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
		OpenAIFaultFallbackOnlyExtraKey: true, OpenAIFaultFallbackStatusCodesExtraKey: []any{float64(401), float64(413)},
	}}
	for _, status := range []int{401, 403, 413, 429, 500, 503} {
		ctx := WithOpenAIFaultFallback(context.Background(), &UpstreamFailoverError{StatusCode: status})
		require.Equal(t, status == 401 || status == 413, openAIFaultFallbackAccountAllowed(ctx, reserve))
	}
	ctx := WithOpenAIFaultFallback(context.Background(), &UpstreamFailoverError{StatusCode: 401, Stage: GatewayFailureStageAccountAuth, Reason: OpenAIUpstreamAccessStateReason})
	require.True(t, openAIFaultFallbackAccountAllowed(ctx, reserve), "actual upstream access errors can match a configured code")
	reserve.Extra[OpenAIFaultFallbackStatusCodesExtraKey] = []any{}
	require.False(t, openAIFaultFallbackAccountAllowed(ctx, reserve))
	reserve.Extra[OpenAIFaultFallbackStatusCodesExtraKey] = "401"
	require.False(t, openAIFaultFallbackAccountAllowed(ctx, reserve), "malformed stored configuration fails closed")
}

func TestOpenAIFaultFallback_ContentAuditCyberMarkAlwaysExcludes(t *testing.T) {
	reserve := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
		OpenAIFaultFallbackOnlyExtraKey: true, OpenAIFaultFallbackStatusCodesExtraKey: []int{400, 403, 429, 503},
	}}
	for _, status := range []int{400, 403, 429, 503} {
		failure := &UpstreamFailoverError{StatusCode: status, ResponseBody: []byte(`{"error":{"code":"upstream_error"}}`)}
		ctx := WithOpenAIFaultFallback(context.Background(), failure, &CyberPolicyMark{Code: "cyber_policy"})
		require.False(t, openAIFaultFallbackAccountAllowed(ctx, reserve), "audit evidence wins even if forwarding replaced the error body")
	}
	for _, tc := range []struct {
		body    string
		allowed bool
	}{
		{`{"error":{"code":"cyber_policy"}}`, false},
		{`{"response":{"error":{"code":" Cyber_Policy "}}}`, false},
		{`{"error":{"code":"server_error","message":"cyber_policy is mentioned here"}}`, true},
		{`{"error":{"code":"server_error"},"input":{"error":{"code":"cyber_policy"}}}`, true},
		{`{"error":{"code":"content_policy"}}`, true},
	} {
		ctx := WithOpenAIFaultFallback(context.Background(), &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(tc.body)})
		require.Equal(t, tc.allowed, openAIFaultFallbackAccountAllowed(ctx, reserve), tc.body)
	}
}

func TestOpenAIFaultFallback_StatusCodeValidation(t *testing.T) {
	for _, raw := range []any{nil, "503", []any{"50"}, []any{"503.5"}, []any{"3xx"}, []any{"50*"}, []any{true}, []any{503.5}, []int{200}, []int{600}} {
		require.Error(t, validateOpenAIFaultFallbackExtra(PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAIFaultFallbackStatusCodesExtraKey: raw}))
	}
	for _, raw := range []any{[]int{}, []int{401, 429}, []any{json.Number("503"), float64(502)}} {
		require.NoError(t, validateOpenAIFaultFallbackExtra(PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAIFaultFallbackStatusCodesExtraKey: raw}))
	}
}

func TestOpenAIFaultFallback_SingleDigitWildcards(t *testing.T) {
	for _, tc := range []struct {
		pattern         string
		size            int
		matches, misses []int
	}{
		{"50x", 10, []int{500, 503, 509}, []int{499, 510, 599}},
		{"5x3", 10, []int{503, 513, 593}, []int{502, 594}},
		{"4xx", 100, []int{400, 429, 499}, []int{500}},
		{"x03", 2, []int{403, 503}, []int{404, 504}},
		{"xxx", 200, []int{400, 503, 599}, []int{200, 600}},
		{"50X", 10, []int{500, 509}, []int{510}},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			codes, err := parseOpenAIFaultFallbackStatusCodes([]any{tc.pattern})
			require.NoError(t, err)
			require.Len(t, codes, tc.size)
			reserve := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
				OpenAIFaultFallbackOnlyExtraKey: true, OpenAIFaultFallbackStatusCodesExtraKey: []any{tc.pattern},
			}}
			for _, code := range tc.matches {
				ctx := WithOpenAIFaultFallback(context.Background(), &UpstreamFailoverError{StatusCode: code})
				require.True(t, openAIFaultFallbackAccountAllowed(ctx, reserve))
				blocked := WithOpenAIFaultFallback(ctx, &UpstreamFailoverError{StatusCode: code}, &CyberPolicyMark{Code: "cyber_policy"})
				require.False(t, openAIFaultFallbackAccountAllowed(blocked, reserve))
			}
			for _, code := range tc.misses {
				ctx := WithOpenAIFaultFallback(context.Background(), &UpstreamFailoverError{StatusCode: code})
				require.False(t, openAIFaultFallbackAccountAllowed(ctx, reserve))
			}
		})
	}
}
