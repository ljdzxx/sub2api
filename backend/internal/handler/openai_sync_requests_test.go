//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIHandlersEnforceSyncRequestMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"deny", "only"} {
		for _, tc := range []struct {
			path   string
			body   string
			handle func(*OpenAIGatewayHandler, *gin.Context)
		}{
			{"/v1/responses", `{"model":"gpt-5.1","input":"hello"}`, (*OpenAIGatewayHandler).Responses},
			{"/v1/responses", `{"model":"gpt-5.1","input":"hello","stream":false}`, (*OpenAIGatewayHandler).Responses},
			{"/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false}`, (*OpenAIGatewayHandler).ChatCompletions},
			{"/v1/messages", `{"model":"gpt-5.1","max_tokens":10,"messages":[{"role":"user","content":"hello"}],"stream":false}`, (*OpenAIGatewayHandler).Messages},
			{"/v1/embeddings", `{"model":"text-embedding-3-small","input":"hello"}`, (*OpenAIGatewayHandler).Embeddings},
			{"/v1/images/generations", `{"model":"gpt-image-1","prompt":"a landscape"}`, (*OpenAIGatewayHandler).Images},
			{"/v1/alpha/search", `{"model":"gpt-5.1","query":"hello"}`, (*OpenAIGatewayHandler).AlphaSearch},
			{"/v1/responses/input_tokens", `{"model":"gpt-5.1","input":"hello"}`, (*OpenAIGatewayHandler).ResponsesInputTokens},
			{"/v1/messages/count_tokens", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).CountTokens},
		} {
			if mode == "only" && tc.path != "/v1/responses" && tc.path != "/v1/chat/completions" && tc.path != "/v1/messages" && tc.path != "/v1/images/generations" {
				continue
			}
			t.Run(mode+tc.path, func(t *testing.T) {
				groupID := int64(38501)
				upstream := &openAIImagesFailoverHTTPUpstream{}
				h := newOpenAIImagesSizeRoutingTestHandler(t, []service.Account{{
					ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
					Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://example.com"},
					Extra:       map[string]any{service.OpenAISyncRequestModeExtraKey: mode},
				}}, upstream)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := tc.body
				if mode == "only" {
					var request map[string]any
					require.NoError(t, json.Unmarshal([]byte(body), &request))
					request["stream"] = true
					streamBody, err := json.Marshal(request)
					require.NoError(t, err)
					body = string(streamBody)
				}
				c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
					ID: 99, GroupID: &groupID,
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, AllowMessagesDispatch: true},
					User:  &service.User{ID: 100},
				})
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
				tc.handle(h, c)
				require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
				require.Empty(t, upstream.calls(), "account must never receive a request excluded by its sync policy")
			})
		}
	}
}
