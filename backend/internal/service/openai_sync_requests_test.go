package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountOpenAISyncRequestMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		platform    string
		accountType string
		extra       map[string]any
		want        OpenAISyncRequestMode
	}{
		{"missing", PlatformOpenAI, AccountTypeAPIKey, nil, OpenAISyncRequestModeAllow},
		{"allow", PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAISyncRequestModeExtraKey: "allow"}, OpenAISyncRequestModeAllow},
		{"deny", PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAISyncRequestModeExtraKey: "deny"}, OpenAISyncRequestModeDeny},
		{"only", PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAISyncRequestModeExtraKey: "only"}, OpenAISyncRequestModeOnly},
		{"invalid", PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAISyncRequestModeExtraKey: "invalid"}, OpenAISyncRequestModeAllow},
		{"invalid type", PlatformOpenAI, AccountTypeAPIKey, map[string]any{OpenAISyncRequestModeExtraKey: false}, OpenAISyncRequestModeAllow},
		{"oauth unaffected", PlatformOpenAI, AccountTypeOAuth, map[string]any{OpenAISyncRequestModeExtraKey: "only"}, OpenAISyncRequestModeAllow},
		{"other platform unaffected", PlatformGrok, AccountTypeAPIKey, map[string]any{OpenAISyncRequestModeExtraKey: "deny"}, OpenAISyncRequestModeAllow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: tc.platform, Type: tc.accountType, Extra: tc.extra}
			require.Equal(t, tc.want, account.GetOpenAISyncRequestMode())
			for _, stream := range []bool{false, true} {
				wantReason := ""
				if !stream && tc.want == OpenAISyncRequestModeDeny {
					wantReason = "sync_requests_disabled"
				} else if stream && tc.want == OpenAISyncRequestModeOnly {
					wantReason = "sync_requests_only"
				}
				require.Equal(t, wantReason, openAISyncRequestAccountFailureReason(WithOpenAIClientStream(context.Background(), stream), account))
			}
			require.Empty(t, openAISyncRequestAccountFailureReason(context.Background(), account))
		})
	}
}

func TestOpenAISyncRequestsScheduling(t *testing.T) {
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	for _, mode := range []string{"deny", "only"} {
		for _, advanced := range []bool{false, true} {
			for _, loadBatch := range []bool{false, true} {
				for _, route := range []string{"ordinary", "sticky", "previous", "accepted", "exhausted"} {
					t.Run(fmt.Sprintf("%s/advanced=%t/batch=%t/%s", mode, advanced, loadBatch, route), func(t *testing.T) {
						groupID := int64(38500)
						accounts := []Account{
							{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, Extra: map[string]any{OpenAISyncRequestModeExtraKey: mode}},
							{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID}},
						}
						if route == "exhausted" || route == "accepted" {
							accounts = accounts[:1]
						}
						cfg := &config.Config{}
						cfg.Gateway.Scheduling.LoadBatchEnabled = loadBatch
						svc := &OpenAIGatewayService{
							accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
							cache:       &schedulerTestGatewayCache{}, cfg: cfg,
							rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(fmt.Sprint(advanced)),
							concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
						}
						stream := mode == "only"
						if route == "accepted" {
							stream = !stream
						}
						ctx := WithOpenAIClientStream(context.Background(), stream)
						session, previous := "", ""
						if route == "sticky" {
							session = "sync-request-session"
							require.NoError(t, svc.setStickySessionAccountID(ctx, &groupID, session, 1, time.Hour))
						}
						if route == "previous" {
							previous = "resp_sync_request"
							require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, previous, 1, time.Hour))
						}
						selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, previous, session, "gpt-5.1", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, true, false)
						if route == "exhausted" {
							require.ErrorIs(t, err, ErrNoAvailableAccounts)
							require.Nil(t, selection)
							return
						}
						require.NoError(t, err)
						require.NotNil(t, selection)
						if selection.ReleaseFunc != nil {
							selection.ReleaseFunc()
						}
						wantID := int64(2)
						if route == "accepted" {
							wantID = 1
						}
						require.Equal(t, wantID, selection.Account.ID)
					})
				}
			}
		}
	}
}

func TestOpenAISyncRequestsWebSocketIngress(t *testing.T) {
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint(advanced), func(t *testing.T) {
			groupID := int64(38502)
			account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID},
				Extra: map[string]any{OpenAISyncRequestModeExtraKey: "only", "openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}}
			cfg := newSchedulerTestOpenAIWSV2Config()
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			svc := &OpenAIGatewayService{
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
				cache:       &schedulerTestGatewayCache{}, cfg: cfg,
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(fmt.Sprint(advanced)),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}
			selection, _, err := svc.SelectAccountWithSchedulerForCapability(context.Background(), &groupID, "", "", "gpt-5.1", nil,
				OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityChatCompletions, false, true, false)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.Nil(t, selection)
			account.Extra[OpenAISyncRequestModeExtraKey] = "deny"
			selection, _, err = svc.SelectAccountWithSchedulerForCapability(context.Background(), &groupID, "", "", "gpt-5.1", nil,
				OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityChatCompletions, false, true, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}
