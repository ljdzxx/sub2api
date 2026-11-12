import type { OpenAINormalInputCacheRule } from '@/types'

export const DEFAULT_OPENAI_NORMAL_INPUT_CACHE_RULES: OpenAINormalInputCacheRule[] = [
  { transfer_percent: 0, probability_percent: 100 }
]

export function cloneOpenAINormalInputCacheRules(
  rules: OpenAINormalInputCacheRule[] | null | undefined
): OpenAINormalInputCacheRule[] {
  return (rules ?? []).map((rule) => ({
    transfer_percent: rule.transfer_percent,
    probability_percent: rule.probability_percent
  }))
}

export function validateOpenAIExperimentalUsagePolicy(
  enabled: boolean,
  rules: OpenAINormalInputCacheRule[]
): string | null {
  if (rules.length === 0) return enabled ? 'rulesRequired' : null

  for (const rule of rules) {
    if (!Number.isInteger(rule.transfer_percent) || rule.transfer_percent < 0 || rule.transfer_percent > 100) {
      return 'transferPercentInvalid'
    }
    if (!Number.isInteger(rule.probability_percent) || rule.probability_percent < 1 || rule.probability_percent > 100) {
      return 'probabilityPercentInvalid'
    }
  }

  const probabilityTotal = rules.reduce((sum, rule) => sum + rule.probability_percent, 0)
  return probabilityTotal === 100 ? null : 'probabilityTotalInvalid'
}
