//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type faultFallbackTestRepo struct{ grokCredentialHandlerRepo }

func (r *faultFallbackTestRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return append([]service.Account(nil), r.accounts...), nil
}

type faultFallbackTestUpstream struct {
	service.HTTPUpstream
	mu             sync.Mutex
	status         int
	hits           []int64
	beforeResponse func()
	body           string
}

func (u *faultFallbackTestUpstream) Do(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.hits = append(u.hits, id)
	u.mu.Unlock()
	if u.beforeResponse != nil {
		u.beforeResponse()
	}
	if u.status == 0 {
		return nil, errors.New("dial tcp: network unreachable")
	}
	body := u.body
	if body == "" {
		body = `{"error":{"type":"server_error","message":"upstream unavailable"}}`
	}
	return &http.Response{
		StatusCode: u.status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}, nil
}

func TestOpenAIFaultFallback_HTTPHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name, path, body string
		handle           func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"gpt-5.1","input":"hello"}`, (*OpenAIGatewayHandler).Responses},
		{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).ChatCompletions},
		{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
		{"images", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"cat"}`, (*OpenAIGatewayHandler).Images},
		{"embeddings", "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hello"}`, (*OpenAIGatewayHandler).Embeddings},
	} {
		for _, scenario := range []struct {
			name        string
			status      int
			onlyReserve bool
			want        []int64
			codes       []int
			keywords    []string
			mode        string
			body        string
		}{
			{name: "upstream_failure", status: 503, want: []int64{1, 2}},
			{name: "rate_limit", status: 429, want: []int64{1, 2}},
			{name: "transport_failure", status: 0, want: []int64{1, 2}},
			{name: "request_error", status: 400, want: []int64{1}},
			{name: "body_limit", status: 413, want: []int64{1}},
			{name: "no_primary", status: 503, onlyReserve: true},
			{name: "configured_exclusion", status: 503, codes: []int{429}, want: []int64{1}},
			{name: "configured_auth", status: 401, codes: []int{401}, want: []int64{1, 2}},
			{name: "audit_cyber_mark", status: 503, want: []int64{1}},
			{name: "cyber_keyword_only", status: 503, want: []int64{1, 2}},
			{
				name: "all_billing_example", status: 403, codes: []int{403}, keywords: []string{"insufficient balance"}, mode: "all",
				body: `{"error":{"message":"insufficient balance","type":"billing_error"}}`, want: []int64{1, 2},
			},
			{
				name: "all_message_mismatch", status: 403, codes: []int{403}, keywords: []string{"insufficient balance"}, mode: "all",
				body: `{"error":{"message":"permission denied"}}`, want: []int64{1},
			},
			{
				name: "all_status_mismatch", status: 503, codes: []int{403}, keywords: []string{"insufficient balance"}, mode: "all",
				body: `{"error":{"message":"insufficient balance"}}`, want: []int64{1},
			},
			{
				name: "any_message_match", status: 403, codes: []int{401}, keywords: []string{"insufficient balance"}, mode: "any",
				body: `{"error":{"message":"Insufficient Balance"}}`, want: []int64{1, 2},
			},
			{
				name: "any_status_match", status: 403, codes: []int{403}, keywords: []string{"insufficient balance"}, mode: "any",
				body: `{"error":{"message":"permission denied"}}`, want: []int64{1, 2},
			},
			{
				name: "keyword_does_not_enable_nonretryable_error", status: 400, codes: []int{}, keywords: []string{"invalid request"}, mode: "any",
				body: `{"error":{"message":"invalid request"}}`, want: []int64{1},
			},
		} {
			t.Run(endpoint.name+"/"+scenario.name, func(t *testing.T) {
				groupID := int64(8330)
				accounts := []service.Account{
					{ID: 1, Name: "primary", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
						Status: service.StatusActive, Schedulable: true, Priority: 100, GroupIDs: []int64{groupID},
						Credentials: map[string]any{"api_key": "primary-key", "base_url": "https://example.com"}},
					{ID: 2, Name: "reserve", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
						Status: service.StatusActive, Schedulable: true, Priority: 0, GroupIDs: []int64{groupID},
						Credentials: map[string]any{"api_key": "reserve-key", "base_url": "https://example.com"},
						Extra:       map[string]any{service.OpenAIFaultFallbackOnlyExtraKey: true}},
				}
				if scenario.codes != nil {
					accounts[1].Extra[service.OpenAIFaultFallbackStatusCodesExtraKey] = scenario.codes
				}
				if scenario.keywords != nil {
					accounts[1].Extra[service.OpenAIFaultFallbackKeywordsExtraKey] = scenario.keywords
				}
				if scenario.mode != "" {
					accounts[1].Extra[service.OpenAIFaultFallbackMatchModeExtraKey] = scenario.mode
				}
				if scenario.onlyReserve {
					accounts = accounts[1:]
				}
				repo := &faultFallbackTestRepo{grokCredentialHandlerRepo: grokCredentialHandlerRepo{accounts: accounts}}
				upstream := &faultFallbackTestUpstream{status: scenario.status, body: scenario.body}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg,
					nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				h.maxAccountSwitches = 3
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID,
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, AllowMessagesDispatch: true}, User: &service.User{ID: 100}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 100})
				if scenario.name == "audit_cyber_mark" {
					upstream.beforeResponse = func() {
						service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Code: "cyber_policy", UpstreamStatus: 503})
						// Simulate an already recorded audit event. This fixture tests
						// routing and deliberately has no asynchronous billing writer.
						c.Set(cyberPolicyRecordedKey, true)
					}
				}
				if scenario.name == "cyber_keyword_only" {
					upstream.body = `{"error":{"type":"server_error","message":"cyber_policy mentioned, upstream unavailable"}}`
				}
				endpoint.handle(h, c)
				upstream.mu.Lock()
				hits := append([]int64(nil), upstream.hits...)
				upstream.mu.Unlock()
				want := scenario.want
				if scenario.status == 0 && (endpoint.name == "images" || endpoint.name == "embeddings") {
					// These upstream paths commit a 502 on transport errors instead of
					// returning a failover signal. Preserve that existing replay boundary.
					want = []int64{1}
					require.Equal(t, http.StatusBadGateway, rec.Code)
				}
				require.Equal(t, want, hits, "response: %d %s", rec.Code, rec.Body.String())
				if scenario.onlyReserve {
					require.NotEqual(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
				}
			})
		}
	}
}

type faultFallbackChannelRepo struct {
	service.ChannelRepository
	groupID int64
}

func (r *faultFallbackChannelRepo) ListAll(context.Context) ([]service.Channel, error) {
	return []service.Channel{{
		ID:       9410,
		Status:   service.StatusActive,
		GroupIDs: []int64{r.groupID},
		ModelMapping: map[string]map[string]string{
			service.PlatformOpenAI: {"gpt-public": "gpt-6-luna"},
		},
	}}, nil
}

func (r *faultFallbackChannelRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{r.groupID: service.PlatformOpenAI}, nil
}

type faultFallbackModelRequest struct {
	accountID int64
	path      string
	model     string
}

type faultFallbackModelUpstream struct {
	service.HTTPUpstream
	mu       sync.Mutex
	requests []faultFallbackModelRequest
	stream   bool
}

func (u *faultFallbackModelUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.requests = append(u.requests, faultFallbackModelRequest{accountID: accountID, path: req.URL.Path, model: payload.Model})
	u.mu.Unlock()

	if accountID == 1 {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"primary unavailable"}}`)),
		}, nil
	}

	contentType := "application/json"
	responseBody := `{"id":"chatcmpl_reserve","object":"chat.completion","created":1700000000,"model":"gpt-6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	if u.stream {
		contentType = "text/event-stream"
		responseBody = faultFallbackChatSSE()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": {contentType},
			"x-request-id": {"reserve-request"},
		},
		Body: io.NopCloser(strings.NewReader(responseBody)),
	}, nil
}

func (u *faultFallbackModelUpstream) calls() []faultFallbackModelRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]faultFallbackModelRequest(nil), u.requests...)
}

func faultFallbackChatSSE() string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl_reserve","object":"chat.completion.chunk","created":1700000000,"model":"gpt-6-luna","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reserve","object":"chat.completion.chunk","created":1700000000,"model":"gpt-6-luna","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
}

func collectResponseModels(value any, models *[]string) {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if key == "model" {
				if model, ok := item.(string); ok {
					*models = append(*models, model)
				}
			}
			collectResponseModels(item, models)
		}
	case []any:
		for _, item := range value {
			collectResponseModels(item, models)
		}
	}
}

func responseModelsFromBody(t *testing.T, body string, stream bool) []string {
	t.Helper()
	models := make([]string, 0)
	if !stream {
		var payload any
		require.NoError(t, json.Unmarshal([]byte(body), &payload), body)
		collectResponseModels(payload, &models)
		return models
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var payload any
		require.NoError(t, json.Unmarshal([]byte(data), &payload), line)
		collectResponseModels(payload, &models)
	}
	return models
}

func TestOpenAIFaultFallback_ForceResponseModelMatchAfterChannelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			const requestedModel = "gpt-public"
			const channelMappedModel = "gpt-6-luna"
			groupID := int64(9411)
			group := &service.Group{
				ID:                                groupID,
				Platform:                          service.PlatformOpenAI,
				Status:                            service.StatusActive,
				OpenAIExperimentalFeaturesEnabled: true,
				ForceResponseModelMatch:           true,
			}
			credentials := func(apiKey string) map[string]any {
				return map[string]any{
					"api_key":  apiKey,
					"base_url": "https://example.com",
				}
			}
			accounts := []service.Account{
				{ID: 1, Name: "primary", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Priority: 100, GroupIDs: []int64{groupID}, Credentials: credentials("primary-key"),
					Extra: map[string]any{openai_compat.ExtraKeyResponsesSupported: false}},
				{ID: 2, Name: "reserve", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Priority: 0, GroupIDs: []int64{groupID}, Credentials: credentials("reserve-key"),
					Extra: map[string]any{service.OpenAIFaultFallbackOnlyExtraKey: true, openai_compat.ExtraKeyResponsesSupported: false}},
			}
			repo := &faultFallbackTestRepo{grokCredentialHandlerRepo: grokCredentialHandlerRepo{accounts: accounts}}
			upstream := &faultFallbackModelUpstream{stream: stream}
			channelService := service.NewChannelService(&faultFallbackChannelRepo{groupID: groupID}, nil, nil, nil, nil)
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCache.Stop)
			gateway := service.NewOpenAIGatewayService(
				repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil,
				billingCache, upstream, &service.DeferredService{}, nil, nil, nil, channelService, nil, nil, nil,
			)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billingCache,
				service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			h.maxAccountSwitches = 3

			requestBody, err := json.Marshal(map[string]any{"model": requestedModel, "input": "hello", "stream": stream})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(requestBody))
			req.Header.Set("Content-Type", "application/json")
			requestContext := context.WithValue(req.Context(), ctxkey.Group, group)
			c.Request = req.WithContext(requestContext)
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
				ID: 99, GroupID: &groupID, Group: group, User: &service.User{ID: 100, Status: service.StatusActive},
			})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 100})

			h.Responses(c)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			calls := upstream.calls()
			require.Len(t, calls, 2, rec.Body.String())
			require.Equal(t, []int64{1, 2}, []int64{calls[0].accountID, calls[1].accountID})
			require.Equal(t, []string{channelMappedModel, channelMappedModel}, []string{calls[0].model, calls[1].model},
				"channel mapping must survive the retry: %+v", calls)
			require.Equal(t, "/v1/chat/completions", calls[1].path)
			models := responseModelsFromBody(t, rec.Body.String(), stream)
			require.NotEmpty(t, models, rec.Body.String())
			for _, model := range models {
				require.Equal(t, requestedModel, model, rec.Body.String())
			}
			if stream {
				require.Contains(t, rec.Body.String(), "data: [DONE]")
			}
		})
	}
}
