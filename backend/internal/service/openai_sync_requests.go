package service

import "context"

const OpenAISyncRequestModeExtraKey = "openai_sync_request_mode"

type OpenAISyncRequestMode string

const (
	OpenAISyncRequestModeAllow OpenAISyncRequestMode = "allow"
	OpenAISyncRequestModeDeny  OpenAISyncRequestMode = "deny"
	OpenAISyncRequestModeOnly  OpenAISyncRequestMode = "only"
)

type openAIClientStreamContextKey struct{}

// WithOpenAIClientStream preserves the client request mode across upstream
// protocol conversion and account failover. Unmarked internal requests are unaffected.
func WithOpenAIClientStream(ctx context.Context, stream bool) context.Context {
	return context.WithValue(ctx, openAIClientStreamContextKey{}, stream)
}

// GetOpenAISyncRequestMode defaults to allowing both sync and streaming requests.
func (a *Account) GetOpenAISyncRequestMode() OpenAISyncRequestMode {
	if a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeAPIKey {
		return OpenAISyncRequestModeAllow
	}
	mode, _ := a.Extra[OpenAISyncRequestModeExtraKey].(string)
	switch OpenAISyncRequestMode(mode) {
	case OpenAISyncRequestModeDeny:
		return OpenAISyncRequestModeDeny
	case OpenAISyncRequestModeOnly:
		return OpenAISyncRequestModeOnly
	default:
		return OpenAISyncRequestModeAllow
	}
}

func openAISyncRequestAccountFailureReason(ctx context.Context, account *Account) string {
	stream, known := ctx.Value(openAIClientStreamContextKey{}).(bool)
	if !known {
		return ""
	}
	switch account.GetOpenAISyncRequestMode() {
	case OpenAISyncRequestModeDeny:
		if !stream {
			return "sync_requests_disabled"
		}
	case OpenAISyncRequestModeOnly:
		if stream {
			return "sync_requests_only"
		}
	}
	return ""
}
