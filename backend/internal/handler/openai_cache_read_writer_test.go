package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCacheReadWriterFlushesJSONKeepaliveWithoutBypassingUsage(t *testing.T) {
	for _, accountEnabled := range []bool{false, true} {
		for _, groupEnabled := range []bool{false, true} {
			name := "account_off"
			if accountEnabled {
				name = "account_on"
			}
			if groupEnabled {
				name += "/group_on"
			} else {
				name += "/group_off"
			}
			t.Run(name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
				finish := installCacheReadWriter(c, &service.APIKey{Group: &service.Group{
					Platform: service.PlatformOpenAI, OpenAIExperimentalFeaturesEnabled: groupEnabled,
					TransferCacheWriteToInput: true,
				}})
				defer finish()
				service.BindOpenAIUsagePolicyAccount(c, &service.Account{
					ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					OpenAIExperimentalFeaturesEnabled: accountEnabled,
					OpenAINormalInputCacheRules:       []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 100}},
				})
				c.Header("Content-Type", "application/json; charset=utf-8")
				for i := 1; i <= 2; i++ {
					n, err := c.Writer.Write([]byte(" \n"))
					require.NoError(t, err)
					require.Equal(t, 2, n)
					c.Writer.Flush()
					require.Equal(t, strings.Repeat(" \n", i), rec.Body.String())
					require.Equal(t, 2*i, c.Writer.Size())
				}
				// Whitespace inside an incomplete JSON document must stay buffered.
				for _, part := range []string{`{"usage":`, " \n"} {
					_, err := c.Writer.Write([]byte(part))
					require.NoError(t, err)
					c.Writer.Flush()
					require.Equal(t, " \n \n", rec.Body.String())
				}
				_, err := c.Writer.Write([]byte(`{"input_tokens":1000,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":100}}}`))
				require.NoError(t, err)
				require.True(t, gjson.Valid(rec.Body.String()))
				wantCacheWrite := 100
				if accountEnabled {
					wantCacheWrite = 500
				}
				if groupEnabled {
					wantCacheWrite = 0
				}
				require.EqualValues(t, wantCacheWrite, gjson.Get(rec.Body.String(), "usage.input_tokens_details.cache_write_tokens").Int())
			})
		}
	}
}

type cacheReadKeepaliveErrorWriter struct {
	gin.ResponseWriter
	err   error
	calls int
}

func (w *cacheReadKeepaliveErrorWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, w.err
}

func TestCacheReadWriterPropagatesJSONKeepaliveWriteFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	w := &cacheReadKeepaliveErrorWriter{ResponseWriter: c.Writer, err: errors.New("client disconnected")}
	c.Writer = w
	finish := installCacheReadWriter(c, &service.APIKey{})
	c.Header("Content-Type", "application/json")
	for _, body := range []string{" \n", `{"usage":{"input_tokens":1000}}`} {
		n, err := c.Writer.Write([]byte(body))
		require.Zero(t, n)
		require.ErrorIs(t, err, w.err)
	}
	finish()
	require.Equal(t, 1, w.calls)
}

func TestCacheReadAuditNeverReturnedByUserStats(t *testing.T) {
	repo := &userUsageRepoCapture{stats: &usagestats.UsageStats{TotalCacheReadTokens: 7630, CacheReadAudit: &usagestats.CacheReadAuditStats{UpstreamCacheReadTokens: 9630, UpstreamCost: 0.5}}}
	router := newUserUsageRequestTypeTestRouter(repo)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/usage/stats?include_cache_read_audit=true", nil))
	require.Equal(t, 200, rec.Code)
	require.False(t, repo.statsFilters.IncludeCacheReadAudit)
	require.NotContains(t, rec.Body.String(), "cache_read_audit")
	require.NotContains(t, rec.Body.String(), "9630")
	require.Contains(t, rec.Body.String(), "7630")
	require.NotNil(t, repo.stats.CacheReadAudit, "public projection must not mutate a shared source")
}

func TestCacheReadWriterJSONAndFragmentedSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		finish := installCacheReadWriter(c, &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, OpenAIExperimentalFeaturesEnabled: true, CacheReadDeductionPercent: 20}})
		body := `{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":9630}}}`
		wire := body
		if stream {
			c.Header("Content-Type", "text/event-stream")
			wire = "data: " + body + "\n\n"
		} else {
			c.Header("Content-Type", "application/json")
		}
		c.Header("Content-Length", "100")
		for _, b := range []byte(wire) {
			_, err := c.Writer.Write([]byte{b})
			require.NoError(t, err)
		}
		if stream {
			c.Writer.Flush()
			require.Contains(t, rec.Body.String(), "7630")
		}
		require.Contains(t, rec.Body.String(), "7630", "complete JSON and SSE usage must be sent before deferred cleanup")
		finish()
		require.NotContains(t, rec.Body.String(), "9630")
		require.Empty(t, rec.Result().Header.Get("Content-Length"))
		if !stream {
			require.EqualValues(t, 7630, gjson.Get(rec.Body.String(), "usage.input_tokens_details.cached_tokens").Int())
		}
	}
}

func TestCacheReadWriterFragmentedSSEAppliesDeductionBeforeAmplification(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	finish := installCacheReadWriter(c, &service.APIKey{Group: &service.Group{
		Platform:                             service.PlatformOpenAI,
		OpenAIExperimentalFeaturesEnabled:    true,
		CacheReadDeductionPercent:            10,
		CacheReadAmplificationPercent:        20,
		CacheReadAmplificationMinInputTokens: 10000,
	}})
	c.Header("Content-Type", "text/event-stream")
	c.Header("Content-Length", "100")

	// Raw usage: ordinary=200, cache-read=9700, cache-write=100, total input=10000.
	// Deduction removes 1000 cache-read tokens, then amplification adds 2000
	// tokens to both total input and cache-read: input=12000, cache-read=10700.
	body := `{"type":"response.completed","response":{"usage":{"input_tokens":10000,"output_tokens":20,"total_tokens":10020,"input_tokens_details":{"cached_tokens":9700,"cache_write_tokens":100}}}}`
	wire := "data: " + body + "\n\n"
	for _, b := range []byte(wire) {
		_, err := c.Writer.Write([]byte{b})
		require.NoError(t, err)
	}
	c.Writer.Flush()
	finish()

	payload := strings.TrimSpace(strings.TrimPrefix(rec.Body.String(), "data: "))
	require.True(t, gjson.Valid(payload))
	require.EqualValues(t, 12000, gjson.Get(payload, "response.usage.input_tokens").Int())
	require.EqualValues(t, 10700, gjson.Get(payload, "response.usage.input_tokens_details.cached_tokens").Int())
	require.EqualValues(t, 12020, gjson.Get(payload, "response.usage.total_tokens").Int())
	require.Empty(t, rec.Result().Header.Get("Content-Length"))
}

func TestCacheReadWriterUsesBoundAccountNormalInputCacheSelection(t *testing.T) {
	for _, stream := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		finish := installCacheReadWriter(c, &service.APIKey{})
		service.BindOpenAIUsagePolicyAccount(c, &service.Account{
			ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			OpenAIExperimentalFeaturesEnabled: true,
			OpenAINormalInputCacheRules:       []domain.OpenAINormalInputCacheRule{{TransferPercent: 50, ProbabilityPercent: 100}},
		})
		body := `{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":100}}}`
		wire := body
		if stream {
			c.Header("Content-Type", "text/event-stream")
			wire = "data: " + body + "\n\n"
		} else {
			c.Header("Content-Type", "application/json")
		}
		_, err := c.Writer.Write([]byte(wire))
		require.NoError(t, err)
		finish()

		payload := strings.TrimSpace(strings.TrimPrefix(rec.Body.String(), "data: "))
		require.EqualValues(t, 1000, gjson.Get(payload, "usage.input_tokens").Int())
		require.EqualValues(t, 100, gjson.Get(payload, "usage.input_tokens_details.cached_tokens").Int())
		require.EqualValues(t, 500, gjson.Get(payload, "usage.input_tokens_details.cache_write_tokens").Int())
	}
}
