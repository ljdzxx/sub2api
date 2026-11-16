import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PaymentMethodSelector from '@/components/payment/PaymentMethodSelector.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}))

describe('PaymentMethodSelector', () => {
  it('prevents provider changes while an order is being submitted', async () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: '12:wxpay', disabled: true,
        methods: [{ id: '14:wxpay', provider_instance_id: '14', provider_name: 'Y Pay', type: 'wxpay', fee_rate: 0, available: true }],
      },
    })
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('select')).toBeUndefined()
  })

  it('keeps same-type methods separate and emits the exact provider option', async () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: '12:wxpay',
        methods: [
          { id: '12:wxpay', provider_instance_id: '12', provider_name: 'X Pay', type: 'wxpay', fee_rate: 0, available: true },
          { id: '12:alipay', provider_instance_id: '12', provider_name: 'X Pay', type: 'alipay', fee_rate: 0, available: true },
          { id: '14:wxpay', provider_instance_id: '14', provider_name: 'Y Pay', type: 'wxpay', fee_rate: 0, available: true },
          { id: '14:alipay', provider_instance_id: '14', provider_name: 'Y Pay', type: 'alipay', fee_rate: 0, available: true },
        ],
      },
    })
    expect(wrapper.findAll('[data-testid="payment-method-grid"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('X Pay')
    expect(wrapper.text()).toContain('Y Pay')
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(4)
    expect(buttons[0]!.classes()).toContain('border-[#09BB07]')
    expect(buttons[2]!.classes()).not.toContain('border-[#09BB07]')
    await buttons[2]!.trigger('click')
    expect(wrapper.emitted('select')).toEqual([['14:wxpay']])
  })

  it('wraps large custom method collections without letting labels widen the selector', () => {
    const methods = Array.from({ length: 12 }, (_, index) => ({
      type: `custom_${index}`,
      display_name: `CUSTOM_PAYMENT_METHOD_${index}`,
      fee_rate: 0,
      available: true,
    }))

    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'custom_0',
        methods,
      },
    })

    const grid = wrapper.get('[data-testid="payment-method-grid"]')
    expect(grid.classes()).toEqual(expect.arrayContaining(['grid', 'sm:grid-cols-3', 'lg:grid-cols-4']))
    expect(grid.classes()).not.toContain('sm:flex')

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(methods.length)
    expect(buttons.every(button => button.classes().includes('min-w-0'))).toBe(true)
    expect(buttons.every((button, index) => button.attributes('title') === methods[index].display_name)).toBe(true)
    expect(wrapper.findAll('[data-testid="payment-method-label"]').every(label => label.classes().includes('truncate'))).toBe(true)
  })

  it('shows the configured display name for custom EasyPay methods', () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'ldc',
        methods: [{ type: 'ldc', display_name: 'LDC Pay', fee_rate: 0, available: true }],
      },
    })

    expect(wrapper.text()).toContain('LDC Pay')
    expect(wrapper.text()).not.toContain('ldc')
    expect(wrapper.text()).not.toContain('payment.methods.ldc')
  })

  it('uses the generic selected style for custom methods that contain built-in names', () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'card_alipay',
        methods: [{ type: 'card_alipay', display_name: 'Card Pay', fee_rate: 0, available: true }],
      },
    })

    const button = wrapper.get('button')
    expect(button.classes()).toContain('border-primary-500')
    expect(button.classes()).not.toContain('border-[#02A9F1]')
  })
})
