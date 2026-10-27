package service

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// SafetyPolicyErrorCodes is the shared logging/filter taxonomy. It does not
// change forwarding, retries, billing, or the cyber-specific enforcement path.
func SafetyPolicyErrorCodes() []string {
	return []string{
		"cyber_policy", "bio_policy", "misalignment_policy_violation",
		"image_content_policy_violation", "moderation_blocked", "content_policy_violation",
		"content_policy", "content_filter",
		"cyber_policy_session_blocked", "session_blocked_by_cyber_policy",
	}
}

func IsSafetyPolicyErrorCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, candidate := range SafetyPolicyErrorCodes() {
		if code == candidate {
			return true
		}
	}
	return false
}

// ParseSafetyPolicyPayload reads structured protocol fields only. Mentions in
// generated text, refusal text, or arbitrary messages are not policy errors.
// content_filter is a stop reason, retained as such in the original body.
func ParseSafetyPolicyPayload(payload []byte) (code, message string) {
	if !gjson.ValidBytes(payload) {
		return "", ""
	}
	root := gjson.ParseBytes(payload)
	for _, path := range []string{"error", "response.error"} {
		errObj := root.Get(path)
		code = strings.TrimSpace(errObj.Get("code").String())
		if IsSafetyPolicyErrorCode(code) {
			return code, strings.TrimSpace(errObj.Get("message").String())
		}
		code = strings.TrimSpace(errObj.Get("type").String())
		if IsSafetyPolicyErrorCode(code) {
			return code, strings.TrimSpace(errObj.Get("message").String())
		}
	}
	if root.Get("type").String() == "error" {
		code = strings.TrimSpace(root.Get("code").String())
		if IsSafetyPolicyErrorCode(code) {
			return code, strings.TrimSpace(root.Get("message").String())
		}
	}
	for _, response := range []gjson.Result{root, root.Get("response")} {
		if response.Get("incomplete_details.reason").String() == "content_filter" &&
			(response.Get("status").String() == "incomplete" || root.Get("type").String() == "response.incomplete") {
			return "content_filter", "Response stopped by content filter."
		}
	}
	for _, choice := range root.Get("choices").Array() {
		if choice.Get("finish_reason").String() == "content_filter" {
			return "content_filter", "Response stopped by content filter."
		}
	}
	return "", ""
}

const opsUpstreamSafetyPolicyKey = "ops_upstream_safety_policy"

// OpsSafetyPolicyError snapshots evidence before compatibility conversion or
// optional upstream-body logging can discard the original error code.
type OpsSafetyPolicyError struct {
	Code    string
	Message string
	Body    string
}

func setOpsUpstreamSafetyPolicy(c *gin.Context, payload []byte) {
	var policy *OpsSafetyPolicyError
	if code, message := ParseSafetyPolicyPayload(payload); code != "" {
		body, _ := SanitizeOpsErrorBodyForQueue(string(payload))
		policy = &OpsSafetyPolicyError{Code: code, Message: truncateString(message, 2048), Body: body}
	}
	// Every attempt replaces the snapshot, including ordinary errors.
	c.Set(opsUpstreamSafetyPolicyKey, policy)
}

func GetOpsUpstreamSafetyPolicy(c *gin.Context) *OpsSafetyPolicyError {
	if c == nil {
		return nil
	}
	value, _ := c.Get(opsUpstreamSafetyPolicyKey)
	policy, _ := value.(*OpsSafetyPolicyError)
	return policy
}

// Compatibility adapters can replace content_filter with another protocol's
// stop reason. Record the original signal before converting the terminal output.
func markOpsContentFilter(c *gin.Context, payload []byte, nonStream bool) {
	if !bytes.Contains(payload, []byte("content_filter")) {
		return
	}
	code, message := ParseSafetyPolicyPayload(payload)
	if code != "content_filter" {
		return
	}
	MarkOpsStreamErrorValue(c, OpsStreamError{
		ErrType: "invalid_request_error", Code: code, Message: message, Body: string(payload),
		IntendedStatus: http.StatusForbidden, RequestScoped: true, NonStream: nonStream,
	})
}
