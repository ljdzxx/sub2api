//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIFaultFallback_PriorityOnlyAmongReserves(t *testing.T) {
	for _, mode := range []string{"legacy", "legacy_batch", "advanced", "advanced_weighted"} {
		for _, tc := range []struct {
			name           string
			matchMode      string
			wantIndex      int
			includePrimary bool
			stickyIndex    int // 1-based; zero means no sticky binding.
			previous       bool
			pinned         bool
			noFailure      bool
			configure      func([]Account, *schedulerTestConcurrencyCache)
		}{
			{name: "and_over_numeric_priority", matchMode: "all", wantIndex: 2},
			{name: "or_over_numeric_priority", matchMode: "any", wantIndex: 2},
			{name: "or_status_branch_only", matchMode: "any", wantIndex: 2, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Extra[OpenAIFaultFallbackKeywordsExtraKey] = []string{"different message"}
			}},
			{name: "or_keyword_branch_only", matchMode: "any", wantIndex: 2, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Extra[OpenAIFaultFallbackStatusCodesExtraKey] = []int{401}
			}},
			{name: "and_message_miss", matchMode: "all", wantIndex: 1, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Extra[OpenAIFaultFallbackKeywordsExtraKey] = []string{"different message"}
			}},
			{name: "and_status_miss", matchMode: "all", wantIndex: 1, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Extra[OpenAIFaultFallbackStatusCodesExtraKey] = []int{401}
			}},
			{name: "disabled_specific_reserve", matchMode: "all", wantIndex: 1, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Schedulable = false
			}},
			{name: "unsupported_model", matchMode: "all", wantIndex: 1, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Credentials = map[string]any{"model_mapping": map[string]any{"other-model": "other-model"}}
			}},
			{name: "ordinary_account_with_conditions_not_promoted", matchMode: "all", wantIndex: 1, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Extra[OpenAIFaultFallbackOnlyExtraKey] = false
			}},
			{name: "ordinary_winner_unchanged", matchMode: "all", wantIndex: 0, includePrimary: true, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[0].Priority = 0
				accounts[1].Priority = 10
			}},
			{name: "ordinary_between_reserves_not_promoted", matchMode: "all", wantIndex: 2, includePrimary: true},
			{name: "ordinary_sticky_unchanged", matchMode: "all", wantIndex: 0, includePrimary: true, stickyIndex: 1},
			{name: "status_only_sticky_yields", matchMode: "all", wantIndex: 2, stickyIndex: 2},
			{name: "movable_previous_response_yields", matchMode: "all", wantIndex: 2, previous: true},
			{name: "pinned_previous_response_keeps_owner", matchMode: "all", wantIndex: 1, previous: true, pinned: true},
			{name: "busy_specific_keeps_available_status_only", matchMode: "all", wantIndex: 1, stickyIndex: 2, configure: func(accounts []Account, concurrency *schedulerTestConcurrencyCache) {
				concurrency.acquireResults[accounts[2].ID] = false
				concurrency.loadMap[accounts[2].ID] = &AccountLoadInfo{AccountID: accounts[2].ID, CurrentConcurrency: 1, LoadRate: 100}
			}},
			{name: "busy_status_only_uses_available_specific", matchMode: "all", wantIndex: 2, configure: func(accounts []Account, concurrency *schedulerTestConcurrencyCache) {
				concurrency.acquireResults[accounts[1].ID] = false
				concurrency.loadMap[accounts[1].ID] = &AccountLoadInfo{AccountID: accounts[1].ID, CurrentConcurrency: 1, LoadRate: 100}
			}},
			{name: "both_busy_prefer_specific_wait", matchMode: "all", wantIndex: 2, configure: func(accounts []Account, concurrency *schedulerTestConcurrencyCache) {
				for _, account := range accounts[1:] {
					concurrency.acquireResults[account.ID] = false
					concurrency.loadMap[account.ID] = &AccountLoadInfo{AccountID: account.ID, CurrentConcurrency: 1, LoadRate: 100}
				}
			}},
			{name: "keywords_only_rank_unchanged", matchMode: "any", wantIndex: 1, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Extra[OpenAIFaultFallbackStatusCodesExtraKey] = []int{}
			}},
			{name: "blank_keywords_rank_unchanged", matchMode: "any", wantIndex: 1, configure: func(accounts []Account, _ *schedulerTestConcurrencyCache) {
				accounts[2].Extra[OpenAIFaultFallbackKeywordsExtraKey] = []string{"  "}
			}},
			{name: "initial_selection_still_ordinary", matchMode: "all", wantIndex: 0, includePrimary: true, noFailure: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
				groupID := int64(8350)
				accounts := newLegacySchedulerDecisionTestAccounts(groupID, true)
				accounts[0].Priority = 50
				accounts[1].Priority = 0
				accounts[1].Extra[OpenAIFaultFallbackOnlyExtraKey] = true
				accounts[1].Extra[OpenAIFaultFallbackStatusCodesExtraKey] = []int{403}
				specific := accounts[1]
				specific.ID++
				specific.Priority = 100
				specific.Extra = map[string]any{
					"openai_apikey_responses_websockets_v2_enabled": true,
					OpenAIFaultFallbackOnlyExtraKey:                 true,
					OpenAIFaultFallbackStatusCodesExtraKey:          []int{403},
					OpenAIFaultFallbackKeywordsExtraKey:             []string{"insufficient balance"},
					OpenAIFaultFallbackMatchModeExtraKey:            tc.matchMode,
				}
				accounts = append(accounts, specific)
				var acquired, released []int64
				concurrency := schedulerTestConcurrencyCache{
					acquiredIDs: &acquired, releasedIDs: &released,
					acquireResults: map[int64]bool{}, loadMap: map[int64]*AccountLoadInfo{},
				}
				if tc.configure != nil {
					tc.configure(accounts, &concurrency)
				}
				priorities := []int{accounts[0].Priority, accounts[1].Priority, accounts[2].Priority}
				svc := newLegacySchedulerDecisionTestService(accounts, mode != "legacy", concurrency)
				if mode == "advanced" || mode == "advanced_weighted" {
					svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true", fmt.Sprint(mode == "advanced_weighted"))
					svc.cfg.Gateway.OpenAIWS.LBTopK = 1
					svc.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
					if tc.stickyIndex == 1 {
						svc.cfg.Gateway.OpenAIWS.LBTopK = 3
					}
				}
				ctx := context.Background()
				if !tc.noFailure {
					ctx = WithOpenAIFaultFallback(ctx, &UpstreamFailoverError{
						StatusCode: 403, ResponseBody: []byte(`{"error":{"message":"insufficient balance","type":"billing_error"}}`),
					})
				}
				excluded := map[int64]struct{}{}
				if !tc.includePrimary {
					excluded[accounts[0].ID] = struct{}{}
				}
				cache := svc.cache.(*schedulerTestGatewayCache)
				session := ""
				if tc.stickyIndex > 0 {
					session = "fallback-priority"
					cache.sessionBindings = map[string]int64{"openai:" + session: accounts[tc.stickyIndex-1].ID}
				}
				previous := ""
				if tc.previous {
					previous = "resp_fallback_priority"
					require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, previous, accounts[1].ID, time.Hour))
				}
				selection, decision, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, previous, session,
					"gpt-5.1", excluded, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, !tc.pinned, true)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, accounts[tc.wantIndex].ID, selection.Account.ID)
				require.Equal(t, selection.Account.ID, decision.SelectedAccountID)
				if session != "" && selection.Acquired {
					require.Equal(t, selection.Account.ID, cache.sessionBindings["openai:"+session])
				}
				if tc.wantIndex == 0 {
					require.Equal(t, []int64{accounts[0].ID}, acquired, "ordinary selection must not probe or acquire fallback accounts")
				}
				releaseLegacySchedulerDecisionSelection(selection)
				var successfulAcquires []int64
				for _, id := range acquired {
					if ok, configured := concurrency.acquireResults[id]; !configured || ok {
						successfulAcquires = append(successfulAcquires, id)
					}
				}
				require.ElementsMatch(t, successfulAcquires, released, "replacement must not leak or double-release concurrency slots")
				require.Equal(t, priorities, []int{accounts[0].Priority, accounts[1].Priority, accounts[2].Priority})
			})
		}
	}
}
