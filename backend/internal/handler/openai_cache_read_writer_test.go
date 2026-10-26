package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

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
		finish := installCacheReadWriter(c, &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, CacheReadDeductionPercent: 20}})
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
