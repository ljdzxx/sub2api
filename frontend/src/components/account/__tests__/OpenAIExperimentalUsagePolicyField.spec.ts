import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OpenAIExperimentalUsagePolicyField from '../OpenAIExperimentalUsagePolicyField.vue'
import {
  cloneOpenAINormalInputCacheRules,
  validateOpenAIExperimentalUsagePolicy
} from '../openaiExperimentalUsagePolicy'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (params?.total != null) return `${key}:${params.total}`
        if (params?.remaining != null) return `${key}:${params.remaining}`
        return key
      }
    })
  }
})

describe('OpenAI experimental usage policy helpers', () => {
  it('accepts integer rules totaling at most 100 percent', () => {
    expect(validateOpenAIExperimentalUsagePolicy(true, [
      { transfer_percent: 0, probability_percent: 20 },
      { transfer_percent: 10, probability_percent: 5 },
      { transfer_percent: 50, probability_percent: 75 }
    ])).toBeNull()
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_percent: 50, probability_percent: 90 }])).toBeNull()
    expect(validateOpenAIExperimentalUsagePolicy(true, [
      { transfer_mode: 'exact', transfer_percent: 0, keep_tokens: 3, probability_percent: 90 }
    ])).toBeNull()
  })

  it('rejects fractions, out-of-range values, and overlapping totals', () => {
    expect(validateOpenAIExperimentalUsagePolicy(true, [])).toBe('rulesRequired')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_percent: 10.5, probability_percent: 100 }])).toBe('transferPercentInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_mode: 'exact', transfer_percent: 0, keep_tokens: -1, probability_percent: 90 }])).toBe('keepTokensInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_mode: 'exact', transfer_percent: 0, keep_tokens: 1.5, probability_percent: 90 }])).toBe('keepTokensInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_percent: 10, probability_percent: 0 }])).toBe('probabilityPercentInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_percent: 10, probability_percent: 60 }, { transfer_percent: 20, probability_percent: 41 }])).toBe('probabilityTotalInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(false, [])).toBeNull()
    expect(validateOpenAIExperimentalUsagePolicy(false, [{ transfer_percent: 10, probability_percent: 101 }])).toBe('probabilityPercentInvalid')
  })

  it('clones rule rows without sharing objects and normalizes the transfer mode', () => {
    const source = [{ transfer_percent: 50, probability_percent: 100 }]
    const cloned = cloneOpenAINormalInputCacheRules(source)
    expect(cloned[0]).toEqual({
      transfer_mode: 'percent',
      transfer_percent: 50,
      keep_tokens: 0,
      probability_percent: 100
    })
    cloned[0].transfer_percent = 25
    expect(source[0].transfer_percent).toBe(50)
  })
})

describe('OpenAIExperimentalUsagePolicyField', () => {
  it('adds, edits, and removes rules while showing the probability total', async () => {
    const wrapper = mount(OpenAIExperimentalUsagePolicyField, {
      props: {
        enabled: true,
        rules: [{ transfer_mode: 'percent', transfer_percent: 50, keep_tokens: 0, probability_percent: 100 }],
        'onUpdate:enabled': (value: boolean) => wrapper.setProps({ enabled: value }),
        'onUpdate:rules': (value) => wrapper.setProps({ rules: value })
      }
    })

    expect(wrapper.get('[data-testid="openai-cache-probability-total"]').text()).toContain('100')
    expect(wrapper.find('[data-testid="openai-cache-probability-unmatched"]').exists()).toBe(false)

    await wrapper.get('button.btn-secondary').trigger('click')
    expect(wrapper.props('rules')).toHaveLength(2)
    expect(wrapper.get('[data-testid="openai-cache-probability-total"]').text()).toContain('101')

    await wrapper.get('[data-testid="openai-cache-transfer-percent-1"]').setValue(25)
    await wrapper.get('[data-testid="openai-cache-probability-percent-1"]').setValue(20)
    expect(wrapper.props('rules')[1]).toEqual({
      transfer_mode: 'percent',
      transfer_percent: 25,
      keep_tokens: 0,
      probability_percent: 20
    })

    const removeButtons = wrapper.findAll('button[aria-label="admin.accounts.openai.experimentalUsagePolicy.removeRule"]')
    await removeButtons[1].trigger('click')
    expect(wrapper.props('rules')).toHaveLength(1)
  })

  it('switches a rule to exact transfer and edits kept tokens', async () => {
    const wrapper = mount(OpenAIExperimentalUsagePolicyField, {
      props: {
        enabled: true,
        rules: [{ transfer_mode: 'percent', transfer_percent: 50, keep_tokens: 0, probability_percent: 90 }],
        'onUpdate:enabled': (value: boolean) => wrapper.setProps({ enabled: value }),
        'onUpdate:rules': (value) => wrapper.setProps({ rules: value })
      }
    })

    expect(wrapper.get('[data-testid="openai-cache-probability-unmatched"]').text()).toContain('10')

    await wrapper.get('[data-testid="openai-cache-transfer-mode-0"]').setValue('exact')
    expect(wrapper.props('rules')[0]).toEqual({
      transfer_mode: 'exact',
      transfer_percent: 50,
      keep_tokens: 0,
      probability_percent: 90
    })
    expect(wrapper.find('[data-testid="openai-cache-transfer-percent-0"]').exists()).toBe(false)

    await wrapper.get('[data-testid="openai-cache-keep-tokens-0"]').setValue(3)
    expect(wrapper.props('rules')[0]).toEqual({
      transfer_mode: 'exact',
      transfer_percent: 50,
      keep_tokens: 3,
      probability_percent: 90
    })
  })

  it('creates a default rule when an empty policy is enabled', async () => {
    const wrapper = mount(OpenAIExperimentalUsagePolicyField, {
      props: {
        enabled: false,
        rules: [],
        'onUpdate:enabled': (value: boolean) => wrapper.setProps({ enabled: value }),
        'onUpdate:rules': (value) => wrapper.setProps({ rules: value })
      }
    })

    await wrapper.get('[data-testid="openai-experimental-usage-policy-toggle"]').trigger('click')
    expect(wrapper.props('enabled')).toBe(true)
    expect(wrapper.props('rules')).toEqual([
      { transfer_mode: 'percent', transfer_percent: 0, keep_tokens: 0, probability_percent: 100 }
    ])
  })
})
