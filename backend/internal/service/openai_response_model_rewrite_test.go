package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMappedResponseModelPreservesOtherData(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := `{"model":"alias","text":"mapped alias","tool":{"model":"mapped","arguments":"{\"model\":\"alias\"}"}}`
	want := `{"model":"public","text":"mapped alias","tool":{"model":"mapped","arguments":"{\"model\":\"alias\"}"}}`
	require.Equal(t, want, string(svc.replaceModelInResponseBody([]byte(body), "mapped", "public")))
	require.Equal(t, "data: "+want, svc.replaceModelInSSELine("data: "+body, "mapped", "public"))
	for _, body := range []string{
		`{"model":"alias",`, `{"model":"alias"} trailing`, `{"model":null}`, `{"model":42}`, `{"model":{}}`, `{"model":[]}`, `{"model":true}`,
		`{"text":"alias","tool":{"model":"alias"}}`,
	} {
		require.Equal(t, body, string(svc.replaceModelInResponseBody([]byte(body), "mapped", "public")))
		require.Equal(t, "data: "+body, svc.replaceModelInSSELine("data: "+body, "mapped", "public"))
	}
	for _, models := range [][2]string{{"same", "same"}, {"", "public"}, {"mapped", ""}} {
		body := `{"model":"alias","response":{"model":"alias"}}`
		require.Equal(t, body, string(svc.replaceModelInResponseBody([]byte(body), models[0], models[1])))
		require.Equal(t, "data: "+body, svc.replaceModelInSSELine("data: "+body, models[0], models[1]))
	}
	require.Equal(t, `data: {"response":{"model":42}}`, svc.replaceModelInSSELine(`data: {"response":{"model":42}}`, "mapped", "public"))
	require.Equal(t, `{"model":"public"}`, string(svc.replaceModelInResponseBody([]byte(`{"model":""}`), "mapped", "public")))
}

func TestForceResponseModelMatchUsesOriginalClientModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, masterEnabled := range []bool{false, true} {
			for _, kind := range []string{"json", "chat", "responses"} {
				name := kind
				if passthrough {
					name += "/passthrough"
				}
				if masterEnabled {
					name += "/enabled"
				} else {
					name += "/master-disabled"
				}
				t.Run(name, func(t *testing.T) {
					const requestedModel = "gpt-public"
					const upstreamModel = "gpt-public-2026-10-01"
					payload := `{"model":"` + upstreamModel + `","text":"keep","tool":{"model":"nested"},"usage":{"prompt_tokens":1,"completion_tokens":1}}`
					wantModel := upstreamModel
					if masterEnabled {
						wantModel = requestedModel
					}
					if kind == "responses" {
						payload = `{"type":"response.completed","response":{"id":"resp_1","model":"` + upstreamModel + `","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`
					}
					body := payload
					contentType := "application/json"
					if kind != "json" {
						body = "data: " + payload + "\n\ndata: [DONE]\n\n"
						contentType = "text/event-stream"
					}

					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					group := &Group{Platform: PlatformOpenAI, OpenAIExperimentalFeaturesEnabled: masterEnabled, ForceResponseModelMatch: true}
					c.Request = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
					svc := &OpenAIGatewayService{}
					account := &Account{ID: 1}
					var err error
					if kind == "json" {
						if passthrough {
							_, err = svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, requestedModel, requestedModel)
						} else {
							_, err = svc.handleNonStreamingResponse(context.Background(), resp, c, account, requestedModel, requestedModel)
						}
					} else if passthrough {
						_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), requestedModel, requestedModel)
					} else {
						_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), requestedModel, requestedModel)
					}
					require.NoError(t, err)
					if kind == "responses" {
						require.Contains(t, rec.Body.String(), `"response":{"id":"resp_1","model":"`+wantModel+`"`)
					} else {
						require.Contains(t, rec.Body.String(), `"model":"`+wantModel+`"`)
						require.Contains(t, rec.Body.String(), `"model":"nested"`)
					}
					require.Equal(t, upstreamModel, observedUpstreamResponseModel(c))
				})
			}
		}
	}
}

// Exercise both streaming processors so substring fast paths cannot bypass the rewrite.
func TestMappedResponseModelForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, returned := range []string{"zhipu/glm-5.3", "glm-5.3-alias"} {
			for _, mapped := range []string{"ZHIPU/GLM-5.3", "public"} {
				for _, kind := range []string{"json", "chat", "responses"} {
					name := kind + "/" + returned + "/" + mapped
					if passthrough {
						name += "/passthrough"
					}
					t.Run(name, func(t *testing.T) {
						model := returned
						if mapped != "public" {
							model = "public"
						}
						payload := `{"model":"` + returned + `","choices":[{"delta":{"content":"keep alias","tool_calls":[{"function":{"arguments":"{\"model\":\"alias\"}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
						want := strings.Replace(payload, `"model":"`+returned+`"`, `"model":"`+model+`"`, 1)
						contentType := "application/json"
						body := payload
						if kind == "responses" {
							payload = `{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"` + returned + `","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`
							want = strings.Replace(payload, `"model":"`+returned+`"`, `"model":"`+model+`"`, 1)
						}
						if kind != "json" {
							contentType = "text/event-stream"
							body = "data: " + payload + "\n\ndata: [DONE]\n\n"
						}
						rec := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(rec)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
						resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
						svc := &OpenAIGatewayService{}
						account := &Account{ID: 1}
						var err error
						if kind == "json" {
							if passthrough {
								_, err = svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, "public", mapped)
							} else {
								_, err = svc.handleNonStreamingResponse(context.Background(), resp, c, account, "public", mapped)
							}
						} else {
							if passthrough {
								_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "public", mapped)
							} else {
								_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "public", mapped)
							}
						}
						require.NoError(t, err)
						require.Contains(t, rec.Body.String(), want)
						require.Equal(t, returned, observedUpstreamResponseModel(c))
					})
				}
			}
		}
	}
}
