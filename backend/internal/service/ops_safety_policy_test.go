package service

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSafetyPolicyPayloadAndCategory(t *testing.T) {
	for _, code := range SafetyPolicyErrorCodes() {
		t.Run(code, func(t *testing.T) {
			for _, envelope := range []string{
				`{"error":{"type":"invalid_request","code":%q,"message":"blocked"}}`,
				`{"type":"response.failed","response":{"error":{"code":%q,"message":"blocked"}}}`,
				`{"type":"error","code":%q,"message":"blocked"}`,
			} {
				got, message := ParseSafetyPolicyPayload([]byte(fmt.Sprintf(envelope, code)))
				require.Equal(t, code, got)
				require.Equal(t, "blocked", message)
			}
			for _, phase := range []string{"request", "upstream", "internal"} {
				require.Equal(t, "safety_policy", MapUserErrorCategory(phase, code))
			}
			out := ToUserErrorRequest(&OpsErrorLog{Phase: "upstream", Type: code})
			require.Equal(t, "safety_policy", out.Category)
			require.Equal(t, code, out.ErrorCode)
		})
	}
}

func TestSafetyPolicyContentFilterUsesStructuredReasonOnly(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`,
		`{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`,
		`{"choices":[{"finish_reason":"stop"},{"finish_reason":"content_filter"}]}`,
	} {
		code, _ := ParseSafetyPolicyPayload([]byte(body))
		require.Equal(t, "content_filter", code, body)
	}
	for _, body := range []string{
		`{"error":{"code":"invalid_request","message":"bio_policy content_filter"}}`,
		`{"type":"response.output_text.delta","delta":"cyber_policy"}`,
		`{"status":"completed","output":[{"type":"refusal","refusal":"blocked"}]}`,
		`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`,
		`{"status":"completed","incomplete_details":{"reason":"content_filter"}}`,
		`{"error":{"code":"bio_policy"`,
		`{"error":{"code":"data_residency_mismatch"}}`,
	} {
		code, _ := ParseSafetyPolicyPayload([]byte(body))
		require.Empty(t, code, body)
	}
}

func TestSafetyPolicySnapshotReplacedAcrossAttemptsAndTurns(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"error":{"code":"bio_policy","message":"blocked"}}`)
	setOpsUpstreamError(c, 400, "blocked", "", body)
	require.Equal(t, "bio_policy", GetOpsUpstreamSafetyPolicy(c).Code)
	setOpsUpstreamError(c, 503, "unavailable", "")
	require.Nil(t, GetOpsUpstreamSafetyPolicy(c))
	setOpsUpstreamError(c, 400, "blocked", "", body)
	BeginOpsStreamTurn(c, 2)
	require.Nil(t, GetOpsUpstreamSafetyPolicy(c))
}

func TestSafetyPolicyWebSocketTurnsRetainOriginalCodeAndBody(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	for i, code := range []string{"bio_policy", "misalignment_policy_violation"} {
		BeginOpsStreamTurn(c, i+1)
		body := []byte(fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request","code":%q,"message":"blocked"}}`, code))
		markOpenAIWSClientVisibleFailure(c, "error", body)
		markOpenAIWSClientVisibleFailure(c, "error", body)
	}
	errors := GetOpsStreamErrors(c)
	require.Len(t, errors, 2)
	require.Equal(t, "bio_policy", errors[0].Code)
	require.Equal(t, "misalignment_policy_violation", errors[1].Code)
	require.Contains(t, errors[0].Body, `"type":"invalid_request"`)
	require.Nil(t, GetOpsCyberPolicy(c), "classification must not invoke cyber enforcement")
}
