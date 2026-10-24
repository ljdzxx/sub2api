//go:build unit

package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
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
	mu     sync.Mutex
	status int
	hits   []int64
}

func (u *faultFallbackTestUpstream) Do(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.hits = append(u.hits, id)
	u.mu.Unlock()
	if u.status == 0 {
		return nil, errors.New("dial tcp: network unreachable")
	}
	return &http.Response{
		StatusCode: u.status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"type":"server_error","message":"upstream unavailable"}}`)),
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
		}{
			{"upstream_failure", 503, false, []int64{1, 2}},
			{"rate_limit", 429, false, []int64{1, 2}},
			{"transport_failure", 0, false, []int64{1, 2}},
			{"request_error", 400, false, []int64{1}},
			{"body_limit", 413, false, []int64{1}},
			{"no_primary", 503, true, nil},
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
				if scenario.onlyReserve {
					accounts = accounts[1:]
				}
				repo := &faultFallbackTestRepo{grokCredentialHandlerRepo: grokCredentialHandlerRepo{accounts: accounts}}
				upstream := &faultFallbackTestUpstream{status: scenario.status}
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
