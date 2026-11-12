package service

import (
	"math"
	"math/big"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// CacheReadAdjustment is persisted for admin audit only. Public usage DTOs must
// never embed it. Raw contains total input, including both cache buckets.
type CacheReadAdjustment struct {
	Version                     int                              `json:"version"`
	Percent                     float64                          `json:"percent"`
	AmplificationPercent        float64                          `json:"amplification_percent,omitempty"`
	AmplificationMinInputTokens int64                            `json:"amplification_min_input_tokens,omitempty"`
	TransferCacheWriteToInput   bool                             `json:"transfer_cache_write_to_input,omitempty"`
	AccountPolicy               *OpenAINormalInputCacheSelection `json:"account_policy,omitempty"`
	Raw                         OpenAIUsage                      `json:"raw"`
	AccountEffective            *OpenAIUsage                     `json:"account_effective,omitempty"`
	Effective                   OpenAIUsage                      `json:"effective"`
	UpstreamCost                float64                          `json:"upstream_cost"`
}

type OpenAICacheReadPolicy struct {
	DeductionPercent            float64
	AmplificationPercent        float64
	AmplificationMinInputTokens int64
	TransferCacheWriteToInput   bool
}

func (p OpenAICacheReadPolicy) HasAdjustments() bool {
	return p.DeductionPercent > 0 || p.AmplificationPercent > 0 || p.TransferCacheWriteToInput
}

func normalizeGroupCacheReadDeduction(g *Group) error {
	if !validCacheReadPercent(g.CacheReadDeductionPercent) {
		return infraerrors.BadRequest("INVALID_CACHE_READ_DEDUCTION", "cache hit-rate reduction must be between 0 and 100 percentage points, with at most two decimal places")
	}
	if !validCacheReadPercent(g.CacheReadAmplificationPercent) {
		return infraerrors.BadRequest("INVALID_CACHE_READ_AMPLIFICATION", "cache read amplification must be between 0 and 100 percent, with at most two decimal places")
	}
	if g.CacheReadAmplificationMinInputTokens < 0 {
		return infraerrors.BadRequest("INVALID_CACHE_READ_AMPLIFICATION_THRESHOLD", "cache read amplification input threshold must be greater than or equal to 0")
	}
	if g.Platform != PlatformOpenAI {
		g.OpenAIExperimentalFeaturesEnabled = false
		g.ForceResponseModelMatch = false
		g.CacheReadDeductionPercent = 0
		g.CacheReadAmplificationPercent = 0
		g.CacheReadAmplificationMinInputTokens = 0
		g.TransferCacheWriteToInput = false
	}
	return nil
}

func validCacheReadPercent(p float64) bool {
	return !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= 100 && math.Abs(p*100-math.Round(p*100)) <= 1e-8
}

func OpenAICacheReadPolicyForKey(key *APIKey) OpenAICacheReadPolicy {
	if key == nil || key.Group == nil || key.Group.Platform != PlatformOpenAI || !key.Group.OpenAIExperimentalFeaturesEnabled {
		return OpenAICacheReadPolicy{}
	}
	policy := OpenAICacheReadPolicy{
		DeductionPercent:            key.Group.CacheReadDeductionPercent,
		AmplificationPercent:        key.Group.CacheReadAmplificationPercent,
		AmplificationMinInputTokens: key.Group.CacheReadAmplificationMinInputTokens,
		TransferCacheWriteToInput:   key.Group.TransferCacheWriteToInput,
	}
	if !validCacheReadPercent(policy.DeductionPercent) {
		policy.DeductionPercent = 0
	}
	if !validCacheReadPercent(policy.AmplificationPercent) {
		policy.AmplificationPercent = 0
	}
	if policy.AmplificationMinInputTokens < 0 {
		policy.AmplificationMinInputTokens = 0
	}
	return policy
}

func OpenAICacheReadDeductionPercent(key *APIKey) float64 {
	return OpenAICacheReadPolicyForKey(key).DeductionPercent
}

func ApplyOpenAICacheReadPolicy(raw OpenAIUsage, policy OpenAICacheReadPolicy) OpenAIUsage {
	effective := DeductOpenAICacheRead(raw, policy.DeductionPercent)
	effective = AmplifyOpenAICacheRead(effective, policy.AmplificationPercent, policy.AmplificationMinInputTokens)
	return TransferOpenAICacheWriteToInput(effective, policy.TransferCacheWriteToInput)
}

// TransferOpenAICacheWriteToInput runs after the cache-read adjustments. OpenAI
// total input already includes cache creation, so clearing that bucket transfers
// the same tokens to ordinary input without changing the total.
func TransferOpenAICacheWriteToInput(raw OpenAIUsage, enabled bool) OpenAIUsage {
	if !enabled || raw.CacheCreationInputTokens == 0 {
		return raw
	}
	out := raw
	out.CacheCreationInputTokens = 0
	return out
}

// DeductOpenAICacheRead is a pure function: callers retain the upstream snapshot
// and never feed a previously adjusted value back into it. Percentage points
// are stored as hundredths; rounding happens only on the final token count.
func DeductOpenAICacheRead(raw OpenAIUsage, percent float64) OpenAIUsage {
	if percent <= 0 || percent > 100 || math.IsNaN(percent) || math.IsInf(percent, 0) {
		return raw
	}
	out := raw
	total := max(raw.InputTokens, 0)
	cached := min(max(raw.CacheReadInputTokens, 0), max(total-max(raw.CacheCreationInputTokens, 0), 0))
	bp := int(math.Round(percent * 100))
	// floor(C - T*bp/10000) == C - ceil(T*bp/10000). Split the
	// multiplication to remain safe even for a maximum-sized integer input.
	removed := (total/10000)*bp + ((total%10000)*bp+9999)/10000
	out.CacheReadInputTokens = max(cached-removed, 0)
	imageCached := min(max(raw.ImageCacheReadTokens, 0), min(cached, max(raw.ImageInputTokens, 0)))
	out.ImageCacheReadTokens = proportionalCacheTokens(imageCached, out.CacheReadInputTokens, cached)
	return out
}

// AmplifyOpenAICacheRead adds a percentage of the original total input to both
// total input and cache-read tokens. Ordinary input and cache creation therefore
// remain unchanged. The threshold is evaluated against the original total input.
func AmplifyOpenAICacheRead(raw OpenAIUsage, percent float64, minInputTokens int64) OpenAIUsage {
	if percent <= 0 || !validCacheReadPercent(percent) || minInputTokens < 0 {
		return raw
	}
	total := max(raw.InputTokens, 0)
	if int64(total) < minInputTokens {
		return raw
	}
	bp := int(math.Round(percent * 100))
	added := (total/10000)*bp + ((total%10000)*bp)/10000
	if added <= 0 {
		return raw
	}
	out := raw
	out.InputTokens = saturatingAddNonNegative(total, added)
	out.CacheReadInputTokens = saturatingAddNonNegative(max(raw.CacheReadInputTokens, 0), added)
	return out
}

func saturatingAddNonNegative(a, b int) int {
	maxInt := int(^uint(0) >> 1)
	if b > maxInt-a {
		return maxInt
	}
	return a + b
}

// The image bucket rounds down; the text bucket receives the remainder.
func proportionalCacheTokens(part, effective, total int) int {
	if total <= 0 || part <= 0 || effective <= 0 {
		return 0
	}
	var n, d big.Int
	n.Mul(big.NewInt(int64(part)), big.NewInt(int64(effective)))
	d.SetInt64(int64(total))
	return int(n.Quo(&n, &d).Int64())
}

func openAIUsageTokens(u OpenAIUsage) UsageTokens {
	return UsageTokens{
		InputTokens:          max(u.InputTokens-u.CacheReadInputTokens-u.CacheCreationInputTokens, 0),
		ImageInputTokens:     max(u.ImageInputTokens-u.ImageCacheReadTokens, 0),
		ImageCacheReadTokens: u.ImageCacheReadTokens,
		OutputTokens:         u.OutputTokens, CacheCreationTokens: u.CacheCreationInputTokens,
		CacheReadTokens: u.CacheReadInputTokens, ImageOutputTokens: u.ImageOutputTokens,
	}
}
