import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import FaultFallbackOnlyField from '../FaultFallbackOnlyField.vue'
import { OPENAI_FAULT_FALLBACK_ONLY, OPENAI_FAULT_FALLBACK_STATUS_CODES, parseFaultFallbackStatusCodes, formatFaultFallbackStatusCodes, withAccountFaultFallback } from '@/utils/accountFaultFallback'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en: { admin: { accounts: {
  faultFallbackOnly: () => 'Fault fallback only',
  faultFallbackOnlyHint: () => 'Only after an upstream failure',
  faultFallbackStatusCodes: () => 'Trigger status codes',
  faultFallbackStatusCodesHint: () => 'Codes replace defaults; confirmed cyber policy is excluded',
  faultFallbackStatusCodesInvalid: () => 'Invalid error codes'
} } } } })

describe('FaultFallbackOnlyField', () => {
  it('round-trips enabling and disabling while preserving other account settings', async () => {
    const extra = { quota_limit: 100, openai_compact_enabled: true }
    const wrapper = mount(FaultFallbackOnlyField, {
      props: { platform: 'openai', type: 'apikey', modelValue: false },
      global: { plugins: [i18n] }
    })
    await wrapper.get('input').setValue(true)
    const saved = withAccountFaultFallback(extra, wrapper.emitted('update:modelValue')![0][0] as boolean)
    expect(saved).toEqual({ ...extra, [OPENAI_FAULT_FALLBACK_ONLY]: true })
    await wrapper.setProps({ modelValue: saved[OPENAI_FAULT_FALLBACK_ONLY] as boolean })
    await wrapper.get('input').setValue(false)
    const disabled = withAccountFaultFallback(saved, wrapper.emitted('update:modelValue')![1][0] as boolean)
    expect(disabled).toEqual({ ...extra, [OPENAI_FAULT_FALLBACK_ONLY]: false })
    expect(extra).not.toHaveProperty(OPENAI_FAULT_FALLBACK_ONLY)
  })

  it.each([['openai', 'oauth'], ['anthropic', 'apikey'], ['grok', 'apikey']])(
    'does not offer unsupported configuration for %s/%s', (platform, type) => {
      const wrapper = mount(FaultFallbackOnlyField, { props: { platform, type }, global: { plugins: [i18n] } })
      expect(wrapper.find('input').exists()).toBe(false)
    }
  )

  it('edits and reloads custom trigger codes, including an explicit empty list', async () => {
    const wrapper = mount(FaultFallbackOnlyField, {
      props: { platform: 'openai', type: 'apikey', modelValue: true }, global: { plugins: [i18n] }
    })
    const input = wrapper.get('[data-testid="fault-fallback-status-codes"]')
    await input.setValue('401, 502-504')
    const value = wrapper.emitted('update:statusCodes')![0][0] as string
    const saved = withAccountFaultFallback({ quota_limit: 10 }, true, parseFaultFallbackStatusCodes(value)!)
    expect(saved[OPENAI_FAULT_FALLBACK_STATUS_CODES]).toEqual([401, 502, 503, 504])
    expect(formatFaultFallbackStatusCodes(saved)).toBe('401, 502-504')
    await wrapper.setProps({ statusCodes: formatFaultFallbackStatusCodes(saved) })
    await input.setValue('')
    const cleared = withAccountFaultFallback(saved, true, parseFaultFallbackStatusCodes('')!)
    expect(formatFaultFallbackStatusCodes(cleared)).toBe('')
    expect(cleared.quota_limit).toBe(10)
    await wrapper.setProps({ statusCodes: '503.5' })
    expect(wrapper.get('[role="alert"]').text()).toBe('Invalid error codes')
  })

  it('accepts error-code ranges and rejects invalid values without silently dropping them', () => {
    expect(parseFaultFallbackStatusCodes('401，403 502-504 503')).toEqual([401, 403, 502, 503, 504])
    expect(parseFaultFallbackStatusCodes('5xx')).toHaveLength(100)
    expect(parseFaultFallbackStatusCodes('50x')).toEqual([500, 501, 502, 503, 504, 505, 506, 507, 508, 509])
    expect(parseFaultFallbackStatusCodes('5x3')).toEqual([503, 513, 523, 533, 543, 553, 563, 573, 583, 593])
    expect(parseFaultFallbackStatusCodes('x03')).toEqual([403, 503])
    expect(parseFaultFallbackStatusCodes('xxx')).toHaveLength(200)
    expect(parseFaultFallbackStatusCodes('50X')).toEqual(parseFaultFallbackStatusCodes('50x'))
    for (const value of ['200', '600', '503.5', '504-502', '429, invalid', '3xx']) {
      expect(parseFaultFallbackStatusCodes(value)).toBeNull()
    }
    expect(formatFaultFallbackStatusCodes({})).toBe('429, 500-599')
  })
})
