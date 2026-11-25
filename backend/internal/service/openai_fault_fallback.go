package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/sync/singleflight"
)

// OpenAIFaultFallbackOnlyExtraKey is deliberately stored in the existing extra
// document so the fork needs no schema migration or generated ORM changes.
const OpenAIFaultFallbackOnlyExtraKey = "openai_fault_fallback_only"

const OpenAIFaultFallbackStatusCodesExtraKey = "openai_fault_fallback_status_codes"
const OpenAIFaultFallbackKeywordsExtraKey = "openai_fault_fallback_keywords"
const OpenAIFaultFallbackMatchModeExtraKey = "openai_fault_fallback_match_mode"

const (
	openAIFaultFallbackMatchAny = "any"
	openAIFaultFallbackMatchAll = "all"
)

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
type openAIFaultFallbackPreferredOnlyContextKey struct{}

type openAIFaultFallbackTrigger struct {
	status  int
	message string
}

// openAIFaultFallbackCandidateTTL 是进程内兜底候选缓存的有效期。
// 缓存的是候选账号的匹配条件，而不是某次失败的判定结果：判定依赖具体的
// 状态码与消息，按判定缓存会把一种消息的结果套到同一分组的另一种消息上。
// 30 秒足以把高频 400 的重复查询收敛成每实例每 30 秒一次。
const openAIFaultFallbackCandidateTTL = 30 * time.Second

// openAIFaultFallbackCandidate 只保留匹配所需的字段，不持有 Account，
// 避免把凭据等无关数据长期留在进程内存里。
type openAIFaultFallbackCandidate struct {
	statusCodes []int
	keywords    []string
	matchMode   string
}

func (c openAIFaultFallbackCandidate) matches(status int, lowerMessage string) bool {
	statusMatched := slices.Contains(c.statusCodes, status)
	keywordMatched := slices.ContainsFunc(c.keywords, func(keyword string) bool {
		return strings.Contains(lowerMessage, strings.ToLower(keyword))
	})
	if c.matchMode == openAIFaultFallbackMatchAll {
		return statusMatched && keywordMatched
	}
	return statusMatched || keywordMatched
}

// openAIFaultFallbackCandidateCache 是按实例生效的候选列表缓存。
// 多实例各自持有一份，作用只是把同一个查询从「每次 400」摊薄到「每 30 秒」。
type openAIFaultFallbackCandidateCache struct {
	mu         sync.Mutex
	candidates []openAIFaultFallbackCandidate
	expiresAt  time.Time
	refresh    singleflight.Group
}

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
	trigger := openAIFaultFallbackTrigger{}
	// This is the same mark consumed by recordCyberPolicyIfMarked to write the
	// Security Audit / Content Audit record. Audit storage is asynchronous, so
	// consult the in-flight evidence rather than querying the audit database.
	for _, mark := range auditMarks {
		if mark != nil {
			return context.WithValue(ctx, openAIFaultFallbackContextKey{}, trigger)
		}
	}
	if ctx.Err() == nil && failure != nil && failure.ShouldRetryNextAccount() &&
		(!failure.IsCredentialFailure() || failure.Reason == OpenAIUpstreamAccessStateReason) {
		// Use the same structured error.code / response.error.code detector as
		// forwarding. Never infer cyber_policy from message text or echoed input.
		if hit, _, _ := detectOpenAICyberPolicy(failure.ResponseBody); !hit {
			trigger.status = failure.StatusCode
			// Reuse the HTTP/SSE error-message extractor, not the entire response
			// body or the rewritten client-facing message. Normalize once per selection.
			trigger.message = strings.ToLower(extractOpenAISSEErrorMessage(failure.ResponseBody))
		}
	}
	return context.WithValue(ctx, openAIFaultFallbackContextKey{}, trigger)
}

func openAIFaultFallbackAccountAllowed(ctx context.Context, account *Account) bool {
	// Only the preference probe uses this restriction. The original selection
	// still sees ordinary accounts and keeps all of its existing ranking rules.
	if openAIFaultFallbackPreferredOnly(ctx) {
		usesStatus, usesMessage := openAIFaultFallbackConditionsConfigured(account)
		if !usesStatus || !usesMessage {
			return false
		}
	}
	if !account.IsOpenAIFaultFallbackOnly() {
		return true
	}
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	trigger, ok := ctx.Value(openAIFaultFallbackContextKey{}).(openAIFaultFallbackTrigger)
	if !ok {
		return false
	}
	return openAIFaultFallbackTriggerMatches(account, trigger.status, trigger.message)
}

// openAIFaultFallbackTriggerMatches 判断一次已归一化的上游失败是否满足账号配置的
// 兜底条件。lowerMessage 必须已经是小写形式，与 WithOpenAIFaultFallback 保持一致。
// 持久化配置损坏时 fail closed。
func openAIFaultFallbackTriggerMatches(account *Account, status int, lowerMessage string) bool {
	if account == nil || !account.IsOpenAIFaultFallbackOnly() {
		return false
	}
	if status < 400 || status > 599 {
		return false
	}
	candidate, ok := openAIFaultFallbackCandidateFromAccount(account)
	if !ok {
		return false // Malformed persisted configuration must fail closed.
	}
	return candidate.matches(status, lowerMessage)
}

// openAIFaultFallbackCandidateFromAccount 把账号配置解析成可缓存的匹配条件。
// 解析失败返回 false，调用方按 fail closed 处理，与选择路径保持一致。
func openAIFaultFallbackCandidateFromAccount(account *Account) (openAIFaultFallbackCandidate, bool) {
	if account == nil || !account.IsOpenAIFaultFallbackOnly() {
		return openAIFaultFallbackCandidate{}, false
	}
	codes := defaultOpenAIFaultFallbackStatusCodes
	if raw, configured := account.Extra[OpenAIFaultFallbackStatusCodesExtraKey]; configured {
		parsed, err := parseOpenAIFaultFallbackStatusCodes(raw)
		if err != nil {
			return openAIFaultFallbackCandidate{}, false
		}
		codes = parsed
	}
	mode := openAIFaultFallbackMatchAny
	if raw, configured := account.Extra[OpenAIFaultFallbackMatchModeExtraKey]; configured {
		parsed, err := parseOpenAIFaultFallbackMatchMode(raw)
		if err != nil {
			return openAIFaultFallbackCandidate{}, false
		}
		mode = parsed
	}
	var keywords []string
	if raw, configured := account.Extra[OpenAIFaultFallbackKeywordsExtraKey]; configured {
		parsed, err := parseOpenAIFaultFallbackKeywords(raw)
		if err != nil {
			return openAIFaultFallbackCandidate{}, false
		}
		keywords = parsed
	}
	return openAIFaultFallbackCandidate{statusCodes: codes, keywords: keywords, matchMode: mode}, true
}

// openAIFaultFallbackCandidates 返回进程内缓存的兜底候选列表，缓存未命中时才查库。
// 空结果同样缓存：没有候选是最常见的情况，不缓存就失去意义。
// 查询失败不写缓存，下次请求重试；本次调用方按 fail closed 处理。
func (s *OpenAIGatewayService) openAIFaultFallbackCandidates(ctx context.Context) []openAIFaultFallbackCandidate {
	if s == nil || s.accountRepo == nil {
		return nil
	}
	if cached, ok := s.faultFallbackCandidates.fresh(time.Now()); ok {
		return cached
	}
	// 并发 400 冷启动时只让一个请求查库，其余等待同一结果，避免同时打出 N 次查询。
	value, _, _ := s.faultFallbackCandidates.refresh.Do("candidates", func() (any, error) {
		if cached, ok := s.faultFallbackCandidates.fresh(time.Now()); ok {
			return cached, nil
		}
		accounts, err := s.accountRepo.FindByExtraField(ctx, OpenAIFaultFallbackOnlyExtraKey, true)
		if err != nil {
			return []openAIFaultFallbackCandidate(nil), err
		}
		candidates := make([]openAIFaultFallbackCandidate, 0, len(accounts))
		for i := range accounts {
			if candidate, ok := openAIFaultFallbackCandidateFromAccount(&accounts[i]); ok {
				candidates = append(candidates, candidate)
			}
		}
		s.faultFallbackCandidates.store(candidates, time.Now().Add(openAIFaultFallbackCandidateTTL))
		return candidates, nil
	})
	candidates, _ := value.([]openAIFaultFallbackCandidate)
	return candidates
}

func (c *openAIFaultFallbackCandidateCache) fresh(now time.Time) ([]openAIFaultFallbackCandidate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !now.Before(c.expiresAt) {
		return nil, false
	}
	return c.candidates, true
}

func (c *openAIFaultFallbackCandidateCache) store(candidates []openAIFaultFallbackCandidate, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.candidates = candidates
	c.expiresAt = expiresAt
}

// openAIFaultFallbackHasMatchingAccount 报告当前系统里是否存在能接受这次上游失败的
// 兜底账号。它只回答「这次 400 值不值得换号」，不改变兜底账号自身的调度规则。
func (s *OpenAIGatewayService) openAIFaultFallbackHasMatchingAccount(
	ctx context.Context, statusCode int, upstreamMsg string, upstreamBody []byte,
) bool {
	if s == nil || s.accountRepo == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	// 只扩展 400：其它状态码本来就由 shouldFailoverOpenAIUpstreamResponse 决定。
	if statusCode != http.StatusBadRequest {
		return false
	}
	// 请求级错误不因账号而变，换号只会把同一份内容重放给另一个上游。
	if hit, _, _ := detectOpenAICyberPolicy(upstreamBody); hit {
		return false
	}
	if isOpenAIContextWindowError(upstreamMsg, upstreamBody) {
		return false
	}
	message := strings.ToLower(extractOpenAISSEErrorMessage(upstreamBody))
	for _, candidate := range s.openAIFaultFallbackCandidates(ctx) {
		if candidate.matches(statusCode, message) {
			return true
		}
	}
	// 候选列表为空或不可用时 fail closed：宁可维持原样透传，也不扩大换号范围。
	return false
}

// shouldFailoverOpenAIUpstreamResponseWithFaultFallback 在常规判定之外补一条：
// 当 400 命中某个兜底账号的配置条件时，把它升级为可换号错误；没有匹配的兜底账号
// 时维持原样，400 仍然是不可重放的客户端错误。
func (s *OpenAIGatewayService) shouldFailoverOpenAIUpstreamResponseWithFaultFallback(
	ctx context.Context, account *Account, statusCode int, upstreamMsg string, upstreamBody []byte,
) bool {
	if s.shouldFailoverOpenAIUpstreamResponse(account, statusCode, upstreamMsg, upstreamBody) {
		return true
	}
	return s.openAIFaultFallbackHasMatchingAccount(ctx, statusCode, upstreamMsg, upstreamBody)
}

func openAIFaultFallbackPreferredOnly(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	preferredOnly, _ := ctx.Value(openAIFaultFallbackPreferredOnlyContextKey{}).(bool)
	return preferredOnly
}

// Rank configured conditions, not which branch of an OR happened to match.
// Keywords-only accounts and ordinary accounts retain their original behavior.
func openAIFaultFallbackConditionsConfigured(account *Account) (usesStatus, usesMessage bool) {
	if !account.IsOpenAIFaultFallbackOnly() {
		return false, false
	}
	codes := defaultOpenAIFaultFallbackStatusCodes
	if raw, configured := account.Extra[OpenAIFaultFallbackStatusCodesExtraKey]; configured {
		var err error
		codes, err = parseOpenAIFaultFallbackStatusCodes(raw)
		if err != nil {
			return false, false
		}
	}
	var keywords []string
	if raw, configured := account.Extra[OpenAIFaultFallbackKeywordsExtraKey]; configured {
		var err error
		keywords, err = parseOpenAIFaultFallbackKeywords(raw)
		if err != nil {
			return false, false
		}
	}
	return len(codes) > 0, len(keywords) > 0
}

// Keep the original result if it is ordinary, keywords-only, already specific,
// or no more specific reserve can serve the request. This avoids changing
// ordinary-account scores/priority or introducing a non-transitive comparator
// into a mixed pool of ordinary and fallback accounts.
func (s *OpenAIGatewayService) preferOpenAIFaultFallbackWithMessage(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	selection *AccountSelectionResult,
	decision OpenAIAccountScheduleDecision,
	selectPreferred func(context.Context) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error),
) (*AccountSelectionResult, OpenAIAccountScheduleDecision) {
	if selection == nil || selection.Account == nil || ctx.Err() != nil || openAIFaultFallbackPreferredOnly(ctx) {
		return selection, decision
	}
	usesStatus, usesMessage := openAIFaultFallbackConditionsConfigured(selection.Account)
	if !usesStatus || usesMessage {
		return selection, decision
	}
	preferred, preferredDecision, err := selectPreferred(context.WithValue(ctx, openAIFaultFallbackPreferredOnlyContextKey{}, true))
	if err != nil || preferred == nil || preferred.Account == nil || (selection.Acquired && !preferred.Acquired) {
		if preferred != nil && preferred.ReleaseFunc != nil {
			preferred.ReleaseFunc()
		}
		return selection, decision
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	// The probe has no session/previous-response affinity, so a failed probe
	// cannot overwrite a usable binding. Bind only the replacement we will use.
	if preferred.Acquired && sessionHash != "" {
		_ = s.bindOpenAIStickySessionDuringSelection(ContextWithSelectionProfitGate(ctx, preferred), groupID, sessionHash, preferred.Account.ID)
	}
	return preferred, preferredDecision
}

func parseOpenAIFaultFallbackMatchMode(raw any) (string, error) {
	mode, ok := raw.(string)
	if !ok || (mode != openAIFaultFallbackMatchAny && mode != openAIFaultFallbackMatchAll) {
		return "", infraerrors.BadRequest("INVALID_FAULT_FALLBACK_MATCH_MODE", "openai_fault_fallback_match_mode must be any or all")
	}
	return mode, nil
}

func parseOpenAIFaultFallbackKeywords(raw any) ([]string, error) {
	invalid := func() ([]string, error) {
		return nil, infraerrors.BadRequest("INVALID_FAULT_FALLBACK_KEYWORDS", "openai_fault_fallback_keywords must be an array of at most 100 strings, each at most 500 characters")
	}
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		if len(typed) > 100 {
			return invalid()
		}
		values = make([]string, len(typed))
		for i, value := range typed {
			keyword, ok := value.(string)
			if !ok {
				return invalid()
			}
			values[i] = keyword
		}
	default:
		return invalid()
	}
	if len(values) > 100 {
		return invalid()
	}
	keywords := make([]string, 0, len(values))
	for _, value := range values {
		keyword := strings.TrimSpace(value)
		if utf8.RuneCountInString(keyword) > 500 {
			return invalid()
		}
		if keyword != "" {
			keywords = append(keywords, keyword)
		}
	}
	return keywords, nil
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
	if raw, present := extra[OpenAIFaultFallbackKeywordsExtraKey]; present {
		if _, err := parseOpenAIFaultFallbackKeywords(raw); err != nil {
			return err
		}
		if platform != PlatformOpenAI || accountType != AccountTypeAPIKey {
			return infraerrors.BadRequest("INVALID_FAULT_FALLBACK_KEYWORDS", fmt.Sprintf("%s is only supported for OpenAI API-key accounts", OpenAIFaultFallbackKeywordsExtraKey))
		}
	}
	if raw, present := extra[OpenAIFaultFallbackMatchModeExtraKey]; present {
		if _, err := parseOpenAIFaultFallbackMatchMode(raw); err != nil {
			return err
		}
		if platform != PlatformOpenAI || accountType != AccountTypeAPIKey {
			return infraerrors.BadRequest("INVALID_FAULT_FALLBACK_MATCH_MODE", fmt.Sprintf("%s is only supported for OpenAI API-key accounts", OpenAIFaultFallbackMatchModeExtraKey))
		}
	}
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
	for _, key := range []string{OpenAIFaultFallbackOnlyExtraKey, OpenAIFaultFallbackStatusCodesExtraKey, OpenAIFaultFallbackKeywordsExtraKey, OpenAIFaultFallbackMatchModeExtraKey} {
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
