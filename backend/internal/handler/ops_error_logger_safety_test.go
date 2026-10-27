package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsErrorLoggerSafetyPolicyHTTPAndSSE(t *testing.T) {
	for _, code := range service.SafetyPolicyErrorCodes() {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", code, streaming), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				router.Use(OpsErrorLoggerMiddleware(ops))
				body := fmt.Sprintf(`{"type":"error","error":{"code":%q,"type":"invalid_request","message":"blocked"}}`, code)
				router.POST("/v1/responses", func(c *gin.Context) {
					setOpsRequestContext(c, "test-model", streaming)
					if streaming {
						c.Header("Content-Type", "text/event-stream")
						_, _ = c.Writer.WriteString("data: " + body[:len(body)/2])
						_, _ = c.Writer.WriteString(body[len(body)/2:] + "\n\n")
					} else {
						c.Data(http.StatusBadRequest, "application/json", []byte(body))
					}
				})
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
				require.Equal(t, int64(1), OpsErrorLogQueueLength())
				entry := (<-opsErrorLogQueue).entry
				require.Equal(t, code, entry.ErrorType)
				require.Equal(t, "request", entry.ErrorPhase)
				require.Contains(t, entry.ErrorBody, code)
				require.Contains(t, w.Body.String(), body, "response stays unchanged")
				if streaming {
					require.Equal(t, http.StatusOK, w.Code)
				}
			})
		}
	}
}

func TestOpsErrorLoggerContentFilterAndOrdinarySuccess(t *testing.T) {
	cases := []struct{ name, body, code string }{
		{"bare_policy_error", `{"error":{"code":"bio_policy","message":"blocked"}}`, "bio_policy"},
		{"responses_filter", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, "content_filter"},
		{"responses_sse_filter", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, "content_filter"},
		{"chat_filter", `{"choices":[{"finish_reason":"content_filter"}]}`, "content_filter"},
		{"chat_success", `{"choices":[{"finish_reason":"stop","message":{"content":"bio_policy content_filter"}}]}`, ""},
		{"token_limit", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, ""},
		{"refusal", `{"status":"completed","output":[{"type":"refusal","refusal":"no"}]}`, ""},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				router.Use(OpsErrorLoggerMiddleware(ops))
				router.POST("/v1/responses", func(c *gin.Context) {
					setOpsRequestContext(c, "test-model", stream)
					if stream {
						c.Header("Content-Type", "text/event-stream")
						_, _ = c.Writer.WriteString("data: " + tc.body + "\n\n")
					} else {
						c.Data(http.StatusOK, "application/json", []byte(tc.body))
					}
				})
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
				require.Equal(t, http.StatusOK, w.Code)
				if tc.code == "" {
					require.Zero(t, OpsErrorLogQueueLength())
				} else {
					require.Equal(t, int64(1), OpsErrorLogQueueLength())
					entry := (<-opsErrorLogQueue).entry
					require.Equal(t, tc.code, entry.ErrorType)
					require.Contains(t, entry.ErrorBody, tc.code)
				}
			})
		}
	}
}

func TestOpsErrorLoggerSafetyEvidenceSurvivesCompatibilityConversion(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprint(recovered), func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				service.SetOpsUpstreamError(c, 400, "blocked", `{"error":{"code":"bio_policy","message":"blocked"}}`)
				if recovered {
					c.JSON(200, gin.H{"status": "completed"})
				} else {
					c.JSON(502, gin.H{"error": gin.H{"type": "api_error", "message": "Upstream request failed"}})
				}
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			if !recovered {
				require.Equal(t, int64(1), OpsErrorLogQueueLength())
				entry := (<-opsErrorLogQueue).entry
				require.Equal(t, "bio_policy", entry.ErrorType)
				require.Contains(t, entry.ErrorBody, "bio_policy")
			} else if OpsErrorLogQueueLength() > 0 {
				require.NotEqual(t, "bio_policy", (<-opsErrorLogQueue).entry.ErrorType)
			}
		})
	}
}

func TestOpsSafetyPolicyStreamKeepsExplicitStatus(t *testing.T) {
	parsed := parseOpsErrorResponse([]byte("data: " + `{"type":"response.failed","response":{"error":{"code":"bio_policy","status_code":400}}}` + "\n\n"))
	require.Equal(t, "bio_policy", parsed.Code)
	require.Equal(t, 400, inferStreamFailureStatus(nil, parsed))
}
