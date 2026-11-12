package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIAccountUsagePolicyWSTurns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		retry bool
	}{
		{name: "pool", mode: OpenAIWSIngressModeCtxPool},
		{name: "pool_retry", mode: OpenAIWSIngressModeCtxPool, retry: true},
		{name: "passthrough", mode: OpenAIWSIngressModePassthrough},
		{name: "http_bridge", mode: OpenAIWSIngressModeHTTPBridge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			account := passthroughLifecycleAccount()
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = tc.mode
			account.OpenAIExperimentalFeaturesEnabled = true
			account.OpenAINormalInputCacheRules = []domain.OpenAINormalInputCacheRule{
				{TransferPercent: 0, ProbabilityPercent: 50}, {TransferPercent: 50, ProbabilityPercent: 50},
			}
			groupID := int64(34)
			user := &User{ID: 31}
			key := &APIKey{ID: 32, User: user, GroupID: &groupID, Group: &Group{
				ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1,
				OpenAIExperimentalFeaturesEnabled: true, CacheReadDeductionPercent: 10,
				CacheReadAmplificationPercent: 20,
			}}
			events := make([]string, 2)
			for i := range events {
				events[i] = fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_policy_%d","model":"gpt-5.1","status":"completed","usage":{"input_tokens":1000,"output_tokens":1,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":100}}}}`, i+1)
			}
			upstream := newStagedPassthroughConn()
			svc := newPassthroughLifecycleService(cfg, upstream)
			var retryDialer *openAIWSQueueDialer
			switch tc.mode {
			case OpenAIWSIngressModeCtxPool:
				pool := newOpenAIWSConnPool(cfg)
				defer pool.Close()
				if tc.retry {
					// EOF on the reused connection forces a retry of turn 2.
					retryDialer = &openAIWSQueueDialer{conns: []openAIWSClientConn{
						&openAIWSCaptureConn{events: [][]byte{[]byte(events[0])}},
						&openAIWSCaptureConn{events: [][]byte{[]byte(events[1])}},
					}}
					pool.setClientDialerForTest(retryDialer)
				} else {
					pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: &openAIWSCaptureConn{
						events: [][]byte{[]byte(events[0]), []byte(events[1])},
					}})
				}
				svc.openaiWSPool = pool
			case OpenAIWSIngressModeHTTPBridge:
				recorder := &httpUpstreamRecorder{}
				for _, event := range events {
					recorder.responses = append(recorder.responses, &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(strings.NewReader("data: " + event + "\n\ndata: [DONE]\n\n")),
					})
				}
				svc.httpUpstream = recorder
			}
			var draws atomic.Int32
			results := make(chan *OpenAIForwardResult, 2)
			server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
				execution := EnsureOpenAIUsagePolicyExecution(c, key)
				execution.roll = func(int) int {
					if draws.Add(1) == 1 {
						return 0
					}
					return 99
				}
				return &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
					if err == nil && result != nil {
						CaptureOpenAIUsagePolicy(c, result)
						results <- result
					}
				}}
			})
			defer server.Close()
			client := dialPassthroughLifecycleClient(t, server)
			defer client.CloseNow()
			for i := range events {
				if i > 0 {
					require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`)))
				}
				if tc.mode == OpenAIWSIngressModePassthrough {
					requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
					upstream.Send(events[i])
				}
				payload, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf("resp_policy_%d", i+1), gjson.GetBytes(payload, "response.id").String())
				require.EqualValues(t, 100+400*i, gjson.GetBytes(payload, "response.usage.input_tokens_details.cache_write_tokens").Int())
				require.EqualValues(t, 1200, gjson.GetBytes(payload, "response.usage.input_tokens").Int())
				require.EqualValues(t, 200, gjson.GetBytes(payload, "response.usage.input_tokens_details.cached_tokens").Int())
			}
			require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			select {
			case err := <-serverErr:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("websocket session did not finish")
			}
			require.EqualValues(t, 2, draws.Load(), "one selection per turn, including retries")
			if retryDialer != nil {
				require.Equal(t, 2, retryDialer.DialCount())
			}
			// Record after both turns finish to catch accidental use of the latest
			// mutable selection instead of each result's captured policy.
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			billing := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			for i := range events {
				select {
				case result := <-results:
					require.Equal(t, 1000, result.Usage.InputTokens)
					require.Equal(t, 100, result.Usage.CacheCreationInputTokens)
					require.Equal(t, i*50, result.UsagePolicy.Account.TransferPercent)
					require.NoError(t, billing.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: result, APIKey: key, User: user, Account: account,
					}))
					require.Equal(t, 100+400*i, logs.lastLog.CacheCreationTokens)
					require.Equal(t, 200, logs.lastLog.CacheReadTokens)
					require.Equal(t, 900-400*i, logs.lastLog.InputTokens)
					require.Equal(t, 100+400*i, logs.lastLog.CacheReadAdjustment.AccountEffective.CacheCreationInputTokens)
				default:
					t.Fatal("missing completed turn result")
				}
			}
		})
	}
}
