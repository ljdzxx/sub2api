package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAIFaultFallbackOnlyExtraKey is deliberately stored in the existing extra
// document so the fork needs no schema migration or generated ORM changes.
const OpenAIFaultFallbackOnlyExtraKey = "openai_fault_fallback_only"

const OpenAIFaultFallbackStatusCodesExtraKey = "openai_fault_fallback_status_codes"

// Defaults preserve existing installations; an explicit list replaces them,
// including an empty list, which disables every status-code trigger.
var defaultOpenAIFaultFallbackStatusCodes = func() []int {
	codes := []int{429}
	for code := 500; code <= 599; code++ {
		codes = append(codes, code)
	}
	return codes
}()

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
func WithOpenAIFaultFallback(ctx context.Context, failure *UpstreamFailoverError, auditMarks ...*CyberPolicyMark) context.Context {
	status := 0
	// This is the same mark consumed by recordCyberPolicyIfMarked to write the
	// Security Audit / Content Audit record. Audit storage is asynchronous, so
	// consult the in-flight evidence rather than querying the audit database.
	for _, mark := range auditMarks {
		if mark != nil {
			return context.WithValue(ctx, openAIFaultFallbackContextKey{}, status)
		}
	}
	if ctx.Err() == nil && failure != nil && failure.ShouldRetryNextAccount() &&
		(!failure.IsCredentialFailure() || failure.Reason == OpenAIUpstreamAccessStateReason) {
		// Use the same structured error.code / response.error.code detector as
		// forwarding. Never infer cyber_policy from message text or echoed input.
		if hit, _, _ := detectOpenAICyberPolicy(failure.ResponseBody); !hit {
			status = failure.StatusCode
		}
	}
	return context.WithValue(ctx, openAIFaultFallbackContextKey{}, status)
}

func openAIFaultFallbackAccountAllowed(ctx context.Context, account *Account) bool {
	if !account.IsOpenAIFaultFallbackOnly() {
		return true
	}
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	status, _ := ctx.Value(openAIFaultFallbackContextKey{}).(int)
	codes := defaultOpenAIFaultFallbackStatusCodes
	if raw, configured := account.Extra[OpenAIFaultFallbackStatusCodesExtraKey]; configured {
		var err error
		codes, err = parseOpenAIFaultFallbackStatusCodes(raw)
		if err != nil {
			return false // Malformed persisted configuration must fail closed.
		}
	}
	return slices.Contains(codes, status)
}

func parseOpenAIFaultFallbackStatusCodes(raw any) ([]int, error) {
	invalid := func() ([]int, error) {
		return nil, infraerrors.BadRequest("INVALID_FAULT_FALLBACK_STATUS_CODES", "openai_fault_fallback_status_codes must be an array of HTTP error codes (400-599) or three-character digit/x patterns matching that range")
	}
	var values []any
	switch typed := raw.(type) {
	case []int:
		values = make([]any, len(typed))
		for i, code := range typed {
			values[i] = code
		}
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for i, pattern := range typed {
			values[i] = pattern
		}
	default:
		return invalid()
	}
	if len(values) > 200 {
		return invalid()
	}
	codes := make([]int, 0, len(values))
	var seen [600]bool
	addCode := func(code int) {
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	for _, value := range values {
		var code int
		switch n := value.(type) {
		case string:
			pattern := strings.ToLower(strings.TrimSpace(n))
			if len(pattern) != 3 {
				return invalid()
			}
			for _, char := range pattern {
				if char != 'x' && (char < '0' || char > '9') {
					return invalid()
				}
			}
			matched := false
			for candidate := 400; candidate <= 599; candidate++ {
				digits := strconv.Itoa(candidate)
				if (pattern[0] == 'x' || pattern[0] == digits[0]) &&
					(pattern[1] == 'x' || pattern[1] == digits[1]) &&
					(pattern[2] == 'x' || pattern[2] == digits[2]) {
					addCode(candidate)
					matched = true
				}
			}
			if !matched {
				return invalid()
			}
			continue
		case int:
			code = n
		case float64:
			if n < 400 || n > 599 || n != float64(int(n)) {
				return invalid()
			}
			code = int(n)
		case json.Number:
			number, err := n.Int64()
			if err != nil || number < 400 || number > 599 {
				return invalid()
			}
			code = int(number)
		default:
			return invalid()
		}
		if code < 400 || code > 599 {
			return invalid()
		}
		addCode(code)
	}
	return codes, nil
}

func validateOpenAIFaultFallbackExtra(platform, accountType string, extra map[string]any) error {
	if raw, present := extra[OpenAIFaultFallbackStatusCodesExtraKey]; present {
		if _, err := parseOpenAIFaultFallbackStatusCodes(raw); err != nil {
			return err
		}
		if platform != PlatformOpenAI || accountType != AccountTypeAPIKey {
			return infraerrors.BadRequest("INVALID_FAULT_FALLBACK_STATUS_CODES", fmt.Sprintf("%s is only supported for OpenAI API-key accounts", OpenAIFaultFallbackStatusCodesExtraKey))
		}
	}
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
	for _, key := range []string{OpenAIFaultFallbackOnlyExtraKey, OpenAIFaultFallbackStatusCodesExtraKey} {
		if _, supplied := extra[key]; supplied {
			continue
		}
		if current, exists := account.Extra[key]; exists {
			extra = maps.Clone(extra)
			if extra == nil {
				extra = make(map[string]any)
			}
			extra[key] = current
		}
	}
	return extra
}
