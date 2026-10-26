package service

import (
	"math"
	"math/big"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// CacheReadAdjustment is persisted for admin audit only. Public usage DTOs must
// never embed it. Raw contains total input, including both cache buckets.
type CacheReadAdjustment struct {
	Version      int         `json:"version"`
	Percent      float64     `json:"percent"`
	Raw          OpenAIUsage `json:"raw"`
	Effective    OpenAIUsage `json:"effective"`
	UpstreamCost float64     `json:"upstream_cost"`
}

func normalizeGroupCacheReadDeduction(g *Group) error {
	p := g.CacheReadDeductionPercent
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 100 || math.Abs(p*100-math.Round(p*100)) > 1e-8 {
		return infraerrors.BadRequest("INVALID_CACHE_READ_DEDUCTION", "cache read deduction must be between 0 and 100, with at most two decimal places")
	}
	if g.Platform != PlatformOpenAI {
		g.CacheReadDeductionPercent = 0
	}
	return nil
}

func OpenAICacheReadDeductionPercent(key *APIKey) float64 {
	if key == nil || key.Group == nil || key.Group.Platform != PlatformOpenAI {
		return 0
	}
	p := key.Group.CacheReadDeductionPercent
	if math.IsNaN(p) || math.IsInf(p, 0) || p <= 0 || p > 100 {
		return 0
	}
	return p
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
