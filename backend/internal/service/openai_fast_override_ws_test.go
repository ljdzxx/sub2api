package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAPIKeyFastOverrideWSTurns(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		for _, groupFast := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/group=%v", mode, groupFast), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: groupFast})
				cfg := passthroughLifecycleConfig()
				cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
				cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
				cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
				account := passthroughLifecycleAccount()
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
				upstream := newStagedPassthroughConn()
				svc := newPassthroughLifecycleService(cfg, upstream)
				svc.settingService = newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{APIKeyFastIgnoreUpstreamTier: true}).settingService
				tiers := []string{"fast", "flex", "", "priority"}
				events := make([]string, len(tiers))
				for i := range tiers {
					events[i] = fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_fast_%d","model":"gpt-5.1","status":"completed","service_tier":"default","usage":{"input_tokens":10,"output_tokens":1}}}`, i)
				}
				switch mode {
				case OpenAIWSIngressModeCtxPool:
					pool := newOpenAIWSConnPool(cfg)
					defer pool.Close()
					conn := &openAIWSCaptureConn{}
					for _, event := range events {
						conn.events = append(conn.events, []byte(event))
					}
					pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn})
					svc.openaiWSPool = pool
				case OpenAIWSIngressModeHTTPBridge:
					recorder := &httpUpstreamRecorder{}
					for _, event := range events {
						recorder.responses = append(recorder.responses, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + event + "\n\ndata: [DONE]\n\n"))})
					}
					svc.httpUpstream = recorder
				}
				results := make(chan *OpenAIForwardResult, len(tiers))
				server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
					return &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
						if err == nil && result != nil {
							CaptureOpenAIUsagePolicy(c, result)
							results <- result
						}
					}}
				})
				defer server.Close()
				frame := func(tier string) string {
					field := ""
					if tier != "" {
						field = fmt.Sprintf(`,"service_tier":%q`, tier)
					}
					return fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","stream":false%s}`, field)
				}
				client := dialPassthroughLifecycleClientWithPayload(t, server, frame(tiers[0]))
				defer client.CloseNow()
				for i, tier := range tiers {
					if i > 0 {
						require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(frame(tier))))
					}
					if mode == OpenAIWSIngressModePassthrough {
						requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
						upstream.Send(events[i])
					}
					payload, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
					require.NoError(t, err)
					want := "default"
					if groupFast || isOpenAIFastTier(tier) {
						want = "priority"
					}
					require.Equal(t, want, gjson.GetBytes(payload, "response.service_tier").String())
				}
				require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
				select {
				case err := <-serverErr:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal("websocket session did not finish")
				}
				for _, tier := range tiers {
					select {
					case result := <-results:
						require.Equal(t, "default", result.UpstreamResponseServiceTier)
						require.Equal(t, groupFast || isOpenAIFastTier(tier), result.UsagePolicy.KeepFast)
						resolution := ApplyOpenAIServiceTierBillingResolution(account, result)
						want := tier
						if groupFast || isOpenAIFastTier(tier) {
							want = "priority"
						}
						require.Equal(t, want, resolution.Billing)
					default:
						t.Fatal("missing completed turn result")
					}
				}
			})
		}
	}
}
