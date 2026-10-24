package service

import (
	"context"
	"maps"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAIFaultFallbackOnlyExtraKey is deliberately stored in the existing extra
// document so the fork needs no schema migration or generated ORM changes.
const OpenAIFaultFallbackOnlyExtraKey = "openai_fault_fallback_only"

type openAIFaultFallbackContextKey struct{}

// IsOpenAIFaultFallbackOnly applies only to OpenAI API-key accounts. Other
// platforms and credential types keep their upstream scheduling behavior.
func (a *Account) IsOpenAIFaultFallbackOnly() bool {
	if a == nil || !a.IsOpenAIApiKey() {
		return false
	}
	enabled, _ := a.Extra[OpenAIFaultFallbackOnlyExtraKey].(bool)
	return enabled
}

// WithOpenAIFaultFallback is used only at selection boundaries after the handler
// has accepted a replayable upstream failure. Exclusions, queue exhaustion,
// credential acquisition failures and priority alone never unlock a reserve.
// Always overwrite the value: authorization must not leak into a new request.
func WithOpenAIFaultFallback(ctx context.Context, failure *UpstreamFailoverError) context.Context {
	allowed := ctx.Err() == nil && failure != nil && failure.ShouldRetryNextAccount() &&
		!failure.IsCredentialFailure() &&
		(failure.StatusCode == http.StatusTooManyRequests || (failure.StatusCode >= 500 && failure.StatusCode <= 599))
	return context.WithValue(ctx, openAIFaultFallbackContextKey{}, allowed)
}

func openAIFaultFallbackAccountAllowed(ctx context.Context, account *Account) bool {
	if !account.IsOpenAIFaultFallbackOnly() {
		return true
	}
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	allowed, _ := ctx.Value(openAIFaultFallbackContextKey{}).(bool)
	return allowed
}

func validateOpenAIFaultFallbackExtra(platform, accountType string, extra map[string]any) error {
	raw, present := extra[OpenAIFaultFallbackOnlyExtraKey]
	if !present {
		return nil
	}
	enabled, ok := raw.(bool)
	if !ok {
		return infraerrors.BadRequest("INVALID_FAULT_FALLBACK_ONLY", "openai_fault_fallback_only must be a boolean")
	}
	if enabled && (platform != PlatformOpenAI || accountType != AccountTypeAPIKey) {
		return infraerrors.BadRequest("INVALID_FAULT_FALLBACK_ONLY", "fault fallback only is supported for OpenAI API-key accounts")
	}
	return nil
}

// Older clients replace extra without knowing this fork-specific flag. Require
// an explicit false to disable the reserve, rather than silently promoting it.
func preserveOpenAIFaultFallbackExtra(account *Account, extra map[string]any) map[string]any {
	if account == nil {
		return extra
	}
	if _, supplied := extra[OpenAIFaultFallbackOnlyExtraKey]; supplied {
		return extra
	}
	if current, exists := account.Extra[OpenAIFaultFallbackOnlyExtraKey]; exists {
		extra = maps.Clone(extra)
		if extra == nil {
			extra = make(map[string]any)
		}
		extra[OpenAIFaultFallbackOnlyExtraKey] = current
	}
	return extra
}
