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
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params?.total == null ? key : `${key}:${params.total}` })
  }
})

describe('OpenAI experimental usage policy helpers', () => {
  it('accepts integer rules totaling exactly 100 percent', () => {
    expect(validateOpenAIExperimentalUsagePolicy(true, [
      { transfer_percent: 0, probability_percent: 20 },
      { transfer_percent: 10, probability_percent: 5 },
      { transfer_percent: 50, probability_percent: 75 }
    ])).toBeNull()
  })

  it('rejects fractions, out-of-range values, and invalid totals', () => {
    expect(validateOpenAIExperimentalUsagePolicy(true, [])).toBe('rulesRequired')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_percent: 10.5, probability_percent: 100 }])).toBe('transferPercentInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_percent: 10, probability_percent: 0 }])).toBe('probabilityPercentInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(true, [{ transfer_percent: 10, probability_percent: 99 }])).toBe('probabilityTotalInvalid')
    expect(validateOpenAIExperimentalUsagePolicy(false, [])).toBeNull()
    expect(validateOpenAIExperimentalUsagePolicy(false, [{ transfer_percent: 10, probability_percent: 99 }])).toBe('probabilityTotalInvalid')
  })

  it('clones rule rows without sharing objects', () => {
    const source = [{ transfer_percent: 50, probability_percent: 100 }]
    const cloned = cloneOpenAINormalInputCacheRules(source)
    cloned[0].transfer_percent = 25
    expect(source[0].transfer_percent).toBe(50)
  })
})

describe('OpenAIExperimentalUsagePolicyField', () => {
  it('adds, edits, and removes rules while showing the probability total', async () => {
    const wrapper = mount(OpenAIExperimentalUsagePolicyField, {
      props: {
        enabled: true,
        rules: [{ transfer_percent: 50, probability_percent: 100 }],
        'onUpdate:enabled': (value: boolean) => wrapper.setProps({ enabled: value }),
        'onUpdate:rules': (value) => wrapper.setProps({ rules: value })
      }
    })

    expect(wrapper.get('[data-testid="openai-cache-probability-total"]').text()).toContain('100')
    await wrapper.get('button.btn-secondary').trigger('click')
    expect(wrapper.props('rules')).toHaveLength(2)
    expect(wrapper.get('[data-testid="openai-cache-probability-total"]').text()).toContain('101')

    await wrapper.get('[data-testid="openai-cache-transfer-percent-1"]').setValue(25)
    await wrapper.get('[data-testid="openai-cache-probability-percent-1"]').setValue(20)
    expect(wrapper.props('rules')[1]).toEqual({ transfer_percent: 25, probability_percent: 20 })

    const removeButtons = wrapper.findAll('button[aria-label="admin.accounts.openai.experimentalUsagePolicy.removeRule"]')
    await removeButtons[1].trigger('click')
    expect(wrapper.props('rules')).toHaveLength(1)
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
    expect(wrapper.props('rules')).toEqual([{ transfer_percent: 0, probability_percent: 100 }])
  })
})
