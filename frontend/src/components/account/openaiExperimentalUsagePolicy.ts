import type { OpenAINormalInputCacheRule, OpenAINormalInputCacheTransferMode } from '@/types'

export const DEFAULT_OPENAI_NORMAL_INPUT_CACHE_RULES: OpenAINormalInputCacheRule[] = [
  { transfer_mode: 'percent', transfer_percent: 0, keep_tokens: 0, probability_percent: 100 }
]

export function normalizeOpenAINormalInputCacheTransferMode(
  mode: OpenAINormalInputCacheTransferMode | undefined
): OpenAINormalInputCacheTransferMode {
  return mode === 'exact' ? 'exact' : 'percent'
}

export function cloneOpenAINormalInputCacheRules(
  rules: OpenAINormalInputCacheRule[] | null | undefined
): OpenAINormalInputCacheRule[] {
  return (rules ?? []).map((rule) => ({
    transfer_mode: normalizeOpenAINormalInputCacheTransferMode(rule.transfer_mode),
    transfer_percent: rule.transfer_percent,
    keep_tokens: rule.keep_tokens ?? 0,
    probability_percent: rule.probability_percent
  }))
}

export function validateOpenAIExperimentalUsagePolicy(
  enabled: boolean,
  rules: OpenAINormalInputCacheRule[]
): string | null {
  if (rules.length === 0) return enabled ? 'rulesRequired' : null

  for (const rule of rules) {
    if (normalizeOpenAINormalInputCacheTransferMode(rule.transfer_mode) === 'exact') {
      const keepTokens = rule.keep_tokens ?? 0
      if (!Number.isInteger(keepTokens) || keepTokens < 0) {
        return 'keepTokensInvalid'
      }
    } else if (!Number.isInteger(rule.transfer_percent) || rule.transfer_percent < 0 || rule.transfer_percent > 100) {
      return 'transferPercentInvalid'
    }
    if (!Number.isInteger(rule.probability_percent) || rule.probability_percent < 1 || rule.probability_percent > 100) {
      return 'probabilityPercentInvalid'
    }
  }

  const probabilityTotal = rules.reduce((sum, rule) => sum + rule.probability_percent, 0)
  // Any probability left over keeps the raw usage, so only overlaps are invalid.
  return probabilityTotal <= 100 ? null : 'probabilityTotalInvalid'
}
