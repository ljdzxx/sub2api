package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIFastOverrideEligibleKey struct{}

func isOpenAIFastTier(tier string) bool {
	switch normalizeBillingServiceTier(tier) {
	case "fast", OpenAIFastTierPriority:
		return true
	default:
		return false
	}
}

// Fast override eligibility records the group's Fast flag before the outbound
// Ultrafast gate and global tier policy run; the final outbound tier gates usage.
func openAIGroupForcesFast(ctx context.Context, account *Account) bool {
	if ctx == nil || account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	return IsGroupContextValid(group) && groupSupportsOpenAIFast(group.Platform) && group.ForceOpenAIFast
}

func openAIFastOverrideEligible(ctx context.Context, account *Account, body []byte) bool {
	settings := openAIFastPolicySettingsFromContext(ctx)
	return settings != nil && settings.APIKeyFastIgnoreUpstreamTier &&
		account != nil && account.IsOpenAIApiKey() && !account.IsShadow() &&
		(isOpenAIFastTier(gjson.GetBytes(body, "service_tier").String()) || openAIGroupForcesFast(ctx, account))
}

// Snapshot the original client intent before any global force_priority rule.
func (s *OpenAIGatewayService) prepareOpenAIFastOverride(ctx context.Context, c *gin.Context, account *Account, body []byte) context.Context {
	execution := EnsureOpenAIUsagePolicyExecution(c, nil)
	execution.setKeepFast(false)
	ctx = context.WithValue(ctx, openAIUsagePolicyExecutionContextKey{}, execution)
	if account != nil && account.IsOpenAIApiKey() && !account.IsShadow() && openAIFastPolicySettingsFromContext(ctx) == nil && s != nil && s.settingService != nil {
		if settings, err := s.settingService.GetOpenAIFastPolicySettings(ctx); err == nil {
			ctx = withOpenAIFastPolicyContext(ctx, settings)
		}
	}
	return context.WithValue(ctx, openAIFastOverrideEligibleKey{}, openAIFastOverrideEligible(ctx, account, body))
}

func updateOpenAIFastOverrideOutbound(ctx context.Context, body []byte) {
	if ctx == nil {
		return
	}
	eligible, ok := ctx.Value(openAIFastOverrideEligibleKey{}).(bool)
	if ok {
		openAIUsagePolicyExecutionFromContext(ctx).setKeepFast(eligible && isOpenAIFastTier(gjson.GetBytes(body, "service_tier").String()))
	}
}

func updateOpenAIWSFastOverride(ctx context.Context, account *Account, original, outbound []byte) {
	openAIUsagePolicyExecutionFromContext(ctx).setKeepFast(openAIFastOverrideEligible(ctx, account, original) &&
		isOpenAIFastTier(gjson.GetBytes(outbound, "service_tier").String()))
}

// Rewrite only OpenAI response envelopes at the client boundary. Upstream
// observation retains the original tier, and errors/deltas stay untouched.
func rewriteOpenAIFastResponseTier(body []byte) []byte {
	root := gjson.ParseBytes(body)
	if errorValue := root.Get("error"); (errorValue.Exists() && errorValue.Type != gjson.Null) || root.Get("type").String() == "error" {
		return body
	}
	path := ""
	if root.Get("response").IsObject() && strings.HasPrefix(root.Get("type").String(), "response.") {
		path = "response.service_tier"
	} else {
		switch root.Get("object").String() {
		case "response", "response.compaction", "chat.completion", "chat.completion.chunk":
			path = "service_tier"
		}
	}
	if path == "" {
		return body
	}
	updated, err := sjson.SetBytes(body, path, OpenAIFastTierPriority)
	if err != nil {
		return body
	}
	return updated
}
