import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import FaultFallbackOnlyField from '../FaultFallbackOnlyField.vue'
import { OPENAI_FAULT_FALLBACK_ONLY, withAccountFaultFallback } from '@/utils/accountFaultFallback'

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en: { admin: { accounts: {
  faultFallbackOnly: () => 'Fault fallback only',
  faultFallbackOnlyHint: () => 'Only after an upstream failure'
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
})
