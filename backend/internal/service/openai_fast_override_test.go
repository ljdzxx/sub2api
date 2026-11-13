package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAPIKeyFastOverrideEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, tier, outbound, platform, accountType string
		enabled, group, shadow, want                bool
	}{
		{name: "fast", tier: "fast", outbound: "priority", enabled: true, want: true},
		{name: "priority", tier: " Priority ", outbound: "priority", enabled: true, want: true},
		{name: "disabled", tier: "fast", outbound: "priority"},
		{name: "forced group", outbound: "priority", enabled: true, group: true, want: true},
		{name: "group converts flex", tier: "flex", outbound: "priority", enabled: true, group: true, want: true},
		{name: "flex", tier: "flex", outbound: "flex", enabled: true},
		{name: "scale", tier: "scale", outbound: "scale", enabled: true},
		{name: "ultrafast", tier: "ultrafast", outbound: "ultrafast", enabled: true},
		{name: "global force missing is excluded", outbound: "priority", enabled: true},
		{name: "global force flex is excluded", tier: "flex", outbound: "priority", enabled: true},
		{name: "filter wins", tier: "fast", enabled: true},
		{name: "oauth", tier: "fast", outbound: "priority", enabled: true, accountType: AccountTypeOAuth},
		{name: "setup token", tier: "fast", outbound: "priority", enabled: true, accountType: AccountTypeSetupToken},
		{name: "other platform", tier: "fast", outbound: "priority", enabled: true, platform: PlatformAnthropic},
		{name: "oauth shadow", tier: "fast", outbound: "priority", enabled: true, shadow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			if tc.platform != "" {
				account.Platform = tc.platform
			}
			if tc.accountType != "" {
				account.Type = tc.accountType
			}
			if tc.shadow {
				id := int64(1)
				account.ParentAccountID = &id
			}
			ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: tc.group})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{APIKeyFastIgnoreUpstreamTier: tc.enabled})
			body := []byte(fmt.Sprintf(`{"service_tier":%q}`, tc.tier))
			ctx = svc.prepareOpenAIFastOverride(ctx, c, account, body)
			updateOpenAIFastOverrideOutbound(ctx, []byte(fmt.Sprintf(`{"service_tier":%q}`, tc.outbound)))
			require.Equal(t, tc.want, EnsureOpenAIUsagePolicyExecution(c, nil).Snapshot().KeepFast)
		})
	}
}

func TestAPIKeyFastOverrideResponseAndBilling(t *testing.T) {
	for _, tc := range []struct {
		name, tier, observed, action string
		enabled, group, want         bool
	}{
		{name: "enabled fast default", tier: "fast", observed: "default", enabled: true, want: true},
		{name: "enabled priority flex", tier: "priority", observed: "flex", enabled: true, want: true},
		{name: "enabled missing response tier", tier: "fast", enabled: true, want: true},
		{name: "disabled", tier: "priority", observed: "default"},
		{name: "forced group", observed: "default", enabled: true, group: true, want: true},
		{name: "flex unchanged", tier: "flex", observed: "flex", enabled: true},
		{name: "scale unchanged", tier: "scale", observed: "scale", enabled: true},
		{name: "filtered fast", tier: "fast", observed: "default", enabled: true, action: BetaPolicyActionFilter},
		{name: "global force missing", observed: "default", enabled: true, action: OpenAIFastPolicyActionForcePriority},
		{name: "global force flex", tier: "flex", observed: "default", enabled: true, action: OpenAIFastPolicyActionForcePriority},
	} {
		for _, route := range []string{"responses", "passthrough", "chat", "chat_responses", "responses_chat"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", tc.name, route, stream), func(t *testing.T) {
					gin.SetMode(gin.TestMode)
					settings := &OpenAIFastPolicySettings{APIKeyFastIgnoreUpstreamTier: tc.enabled}
					if tc.action != "" {
						tier := OpenAIFastTierAny
						if tc.tier == "" {
							tier = OpenAIFastTierMissing
						}
						settings.Rules = []OpenAIFastPolicyRule{{ServiceTier: tier, Action: tc.action, Scope: "all"}}
					}
					svc := newOpenAIGatewayServiceWithSettings(t, settings)
					svc.cfg = &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
					account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}, Extra: map[string]any{"openai_responses_supported": true}}
					if route == "chat" || route == "responses_chat" {
						account.Extra["openai_responses_supported"] = false
					}
					if route == "passthrough" {
						account.Extra["openai_passthrough"] = true
					}
					tierField := ""
					if tc.tier != "" {
						tierField = fmt.Sprintf(`,"service_tier":%q`, tc.tier)
					}
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","input":"hello","stream":%t%s}`, stream, tierField))
					path := "/v1/responses"
					if route == "chat" || route == "chat_responses" {
						path = "/v1/chat/completions"
						body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":%t%s}`, stream, tierField))
					}
					responseTier := ""
					if tc.observed != "" {
						responseTier = fmt.Sprintf(`,"service_tier":%q`, tc.observed)
					}
					responseBody := fmt.Sprintf(`{"id":"resp_fast","object":"response","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":100,"output_tokens":50}%s}`, responseTier)
					contentType := "application/json"
					if stream || route == "chat_responses" {
						contentType = "text/event-stream"
						responseBody = "data: {\"type\":\"response.completed\",\"response\":" + responseBody + "}\n\ndata: [DONE]\n\n"
					}
					if route == "chat" || route == "responses_chat" {
						responseBody = fmt.Sprintf(`{"id":"chatcmpl_fast","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50}%s}`, responseTier)
						if stream {
							responseBody = "data: " + strings.ReplaceAll(responseBody, "chat.completion\"", "chat.completion.chunk\"") + "\n\ndata: [DONE]\n\n"
						}
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(responseBody))}}
					svc.httpUpstream = upstream
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: tc.group})
					c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
					var result *OpenAIForwardResult
					var err error
					if path == "/v1/chat/completions" {
						result, err = svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
					} else {
						result, err = svc.Forward(ctx, c, account, body)
					}
					require.NoError(t, err)
					require.Equal(t, tc.want, result.UsagePolicy.KeepFast)
					require.Equal(t, normalizeObservedOpenAIServiceTier(tc.observed), result.UpstreamResponseServiceTier)
					transform := NewOpenAIUsagePolicyWireTransformer(EnsureOpenAIUsagePolicyExecution(c, nil), false)
					wire := rec.Body.Bytes()
					if stream {
						var out bytes.Buffer
						for _, event := range bytes.SplitAfter(wire, []byte("\n\n")) {
							out.Write(transform.TransformSSEEvent(event))
						}
						wire = out.Bytes()
					} else {
						wire = transform.Transform(wire)
					}
					if tc.want {
						require.Contains(t, string(wire), `"service_tier":"priority"`)
						require.NotContains(t, string(wire), `"service_tier":"default"`)
					} else {
						require.Equal(t, rec.Body.String(), string(wire))
					}
					logs := &openAIRecordUsageLogRepoStub{inserted: true}
					billing := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
					wantTier := ResolveOpenAIServiceTierBilling(account, optionalStringValue(result.ServiceTier), result.UpstreamResponseServiceTier).Billing
					if tc.want {
						wantTier = "priority"
					}
					require.NoError(t, billing.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 1}, User: &User{ID: 2}, Account: account}))
					require.Equal(t, wantTier, optionalStringValue(logs.lastLog.ServiceTier))
					wantCost, err := billing.billingService.CalculateCostWithServiceTier("gpt-5.4", UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, wantTier)
					require.NoError(t, err)
					require.InDelta(t, wantCost.TotalCost, logs.lastLog.TotalCost, 1e-10)
				})
			}
		}
	}
}

func TestAPIKeyFastOverrideWirePreservesOtherFields(t *testing.T) {
	execution := NewOpenAIUsagePolicyExecution(nil)
	execution.setKeepFast(true)
	transform := NewOpenAIUsagePolicyWireTransformer(execution, false)
	for _, payload := range []string{
		`{"type":"error","error":{"service_tier":"default"}}`,
		`{"type":"response.output_text.delta","delta":"hello"}`,
		`{"object":"list","data":[]}`,
		`[DONE]`,
	} {
		require.Equal(t, payload, string(transform.Transform([]byte(payload))))
	}
	payload := []byte(`{"object":"response","model":"gpt-5.4","error":null,"service_tier":"flex","usage":{"input_tokens":100},"output":[]}`)
	before := append([]byte(nil), payload...)
	out := transform.Transform(payload)
	require.Equal(t, before, payload, "upstream bytes remain untouched")
	require.Equal(t, "priority", gjson.GetBytes(out, "service_tier").String())
	require.Equal(t, gjson.GetBytes(before, "usage").Raw, gjson.GetBytes(out, "usage").Raw)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(out, "model").String())
}

func TestAPIKeyFastOverrideSettingsPersistence(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, nil).settingService
	settings, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.APIKeyFastIgnoreUpstreamTier)
	settings.APIKeyFastIgnoreUpstreamTier = true
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), settings))
	loaded, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.True(t, loaded.APIKeyFastIgnoreUpstreamTier)
	loaded.APIKeyFastIgnoreUpstreamTier = false
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), loaded))
	loaded, err = svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.False(t, loaded.APIKeyFastIgnoreUpstreamTier)
}

func TestAPIKeyFastOverrideAccountFailover(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{APIKeyFastIgnoreUpstreamTier: true})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"service_tier":"priority"}`)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	ctx := svc.prepareOpenAIFastOverride(context.Background(), c, account, body)
	updateOpenAIFastOverrideOutbound(ctx, body)
	execution := EnsureOpenAIUsagePolicyExecution(c, nil)
	snapshot := execution.Snapshot()
	require.True(t, snapshot.KeepFast)
	ctx = svc.prepareOpenAIFastOverride(context.Background(), c, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, body)
	updateOpenAIFastOverrideOutbound(ctx, body)
	require.False(t, execution.Snapshot().KeepFast)
	require.True(t, snapshot.KeepFast, "deferred usage retains its original request decision")
}
