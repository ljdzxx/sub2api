package service

import (
	"context"
	"math/rand/v2"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
)

const openAIUsagePolicyExecutionGinKey = "openai_usage_policy_execution"

type openAIUsagePolicyExecutionContextKey struct{}

type OpenAINormalInputCacheSelection struct {
	Enabled            bool  `json:"enabled"`
	AccountID          int64 `json:"account_id"`
	TransferPercent    int   `json:"transfer_percent"`
	ProbabilityPercent int   `json:"probability_percent"`
}

type OpenAIUsagePolicySnapshot struct {
	Account OpenAINormalInputCacheSelection
	Group   OpenAICacheReadPolicy
}

func (s OpenAIUsagePolicySnapshot) HasAdjustments() bool {
	return s.Account.Enabled || s.Group.HasAdjustments()
}

func (s OpenAIUsagePolicySnapshot) Apply(raw OpenAIUsage) (OpenAIUsage, OpenAIUsage) {
	accountEffective := raw
	if s.Account.Enabled {
		accountEffective = TransferOpenAINormalInputToCacheWrite(raw, s.Account.TransferPercent)
	}
	return accountEffective, ApplyOpenAICacheReadPolicy(accountEffective, s.Group)
}

type OpenAIUsagePolicyExecution struct {
	mu      sync.RWMutex
	group   OpenAICacheReadPolicy
	account OpenAINormalInputCacheSelection
	roll    func(int) int
}

func NewOpenAIUsagePolicyExecution(key *APIKey) *OpenAIUsagePolicyExecution {
	return &OpenAIUsagePolicyExecution{group: OpenAICacheReadPolicyForKey(key), roll: rand.IntN}
}

func (e *OpenAIUsagePolicyExecution) BindAccount(account *Account) {
	e.bindAccount(account, false)
}

// BeginTurn selects a new account rule for an accepted WS response.create.
// Retries within that turn must use BindAccount to retain the same selection.
// The group policy remains the independent session configuration snapshot.
func (e *OpenAIUsagePolicyExecution) BeginTurn(account *Account) {
	e.bindAccount(account, true)
}

func (e *OpenAIUsagePolicyExecution) bindAccount(account *Account, newTurn bool) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if account == nil || !account.IsOpenAIApiKey() || !account.OpenAIExperimentalFeaturesEnabled {
		e.account = OpenAINormalInputCacheSelection{}
		return
	}
	if !newTurn && e.account.Enabled && e.account.AccountID == account.ID {
		return
	}
	roll := e.roll
	if roll == nil {
		roll = rand.IntN
	}
	rule, ok := selectOpenAINormalInputCacheRule(account.OpenAINormalInputCacheRules, roll(100))
	if !ok {
		e.account = OpenAINormalInputCacheSelection{}
		return
	}
	e.account = OpenAINormalInputCacheSelection{
		Enabled: true, AccountID: account.ID,
		TransferPercent: rule.TransferPercent, ProbabilityPercent: rule.ProbabilityPercent,
	}
}

func (e *OpenAIUsagePolicyExecution) Snapshot() OpenAIUsagePolicySnapshot {
	if e == nil {
		return OpenAIUsagePolicySnapshot{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return OpenAIUsagePolicySnapshot{Account: e.account, Group: e.group}
}

func EnsureOpenAIUsagePolicyExecution(c *gin.Context, key *APIKey) *OpenAIUsagePolicyExecution {
	if c == nil {
		return NewOpenAIUsagePolicyExecution(key)
	}
	if value, ok := c.Get(openAIUsagePolicyExecutionGinKey); ok {
		if execution, ok := value.(*OpenAIUsagePolicyExecution); ok && execution != nil {
			return execution
		}
	}
	if key == nil && c.Request != nil {
		if group, ok := c.Request.Context().Value(ctxkey.Group).(*Group); ok && group != nil {
			key = &APIKey{Group: group}
		}
	}
	execution := NewOpenAIUsagePolicyExecution(key)
	c.Set(openAIUsagePolicyExecutionGinKey, execution)
	if c.Request != nil {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAIUsagePolicyExecutionContextKey{}, execution))
	}
	return execution
}

func openAIUsagePolicyExecutionFromContext(ctx context.Context) *OpenAIUsagePolicyExecution {
	if ctx == nil {
		return nil
	}
	execution, _ := ctx.Value(openAIUsagePolicyExecutionContextKey{}).(*OpenAIUsagePolicyExecution)
	return execution
}

func BindOpenAIUsagePolicyAccount(c *gin.Context, account *Account) {
	if c == nil {
		return
	}
	EnsureOpenAIUsagePolicyExecution(c, nil).BindAccount(account)
}

func CaptureOpenAIUsagePolicy(c *gin.Context, result *OpenAIForwardResult) {
	if c == nil || result == nil {
		return
	}
	result.UsagePolicy = EnsureOpenAIUsagePolicyExecution(c, nil).Snapshot()
}

func selectOpenAINormalInputCacheRule(rules []domain.OpenAINormalInputCacheRule, roll int) (domain.OpenAINormalInputCacheRule, bool) {
	if roll < 0 || roll >= 100 {
		return domain.OpenAINormalInputCacheRule{}, false
	}
	cumulative := 0
	for _, rule := range rules {
		cumulative += rule.ProbabilityPercent
		if roll < cumulative {
			return rule, true
		}
	}
	return domain.OpenAINormalInputCacheRule{}, false
}

func validateOpenAIAccountUsagePolicy(platform, accountType string, enabled bool, rules []domain.OpenAINormalInputCacheRule) error {
	if platform != PlatformOpenAI || accountType != AccountTypeAPIKey {
		if enabled || len(rules) > 0 {
			return infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_USAGE_POLICY_SCOPE", "OpenAI experimental usage policy is only available for OpenAI API Key accounts")
		}
		return nil
	}
	if len(rules) == 0 {
		if !enabled {
			return nil
		}
		return infraerrors.BadRequest("OPENAI_ACCOUNT_USAGE_POLICY_RULES_REQUIRED", "at least one normal-input cache-write rule is required while the policy is enabled")
	}
	probabilityTotal := 0
	for _, rule := range rules {
		if rule.TransferPercent < 0 || rule.TransferPercent > 100 {
			return infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_USAGE_TRANSFER_PERCENT", "transfer_percent must be an integer between 0 and 100")
		}
		if rule.ProbabilityPercent <= 0 || rule.ProbabilityPercent > 100 {
			return infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_USAGE_PROBABILITY_PERCENT", "probability_percent must be an integer between 1 and 100")
		}
		probabilityTotal += rule.ProbabilityPercent
	}
	if probabilityTotal != 100 {
		return infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_USAGE_PROBABILITY_TOTAL", "probability_percent values must add up to exactly 100")
	}
	return nil
}

func TransferOpenAINormalInputToCacheWrite(raw OpenAIUsage, percent int) OpenAIUsage {
	if percent <= 0 || percent > 100 {
		return raw
	}
	ordinary := max(raw.InputTokens-raw.CacheReadInputTokens-raw.CacheCreationInputTokens, 0)
	transferred := (ordinary/100)*percent + ((ordinary%100)*percent)/100
	if transferred <= 0 {
		return raw
	}
	out := raw
	out.CacheCreationInputTokens = saturatingAddNonNegative(max(raw.CacheCreationInputTokens, 0), transferred)
	// Converted ordinary input uses the default short-write price. Preserve any
	// valid upstream long-write bucket instead of invalidating its TTL split.
	if raw.CacheCreationInputTokens > 0 && raw.CacheCreation30mInputTokens >= 0 &&
		raw.CacheCreation30mInputTokens <= raw.CacheCreationInputTokens &&
		raw.CacheCreation12hInputTokens == raw.CacheCreationInputTokens-raw.CacheCreation30mInputTokens {
		out.CacheCreation30mInputTokens = out.CacheCreationInputTokens - raw.CacheCreation12hInputTokens
	}
	return out
}
