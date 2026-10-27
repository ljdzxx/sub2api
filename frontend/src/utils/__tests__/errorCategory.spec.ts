import { describe, expect, it } from 'vitest'
import { isSafetyPolicyErrorCode, mapErrorCategory } from '../errorCategory'

describe('safety policy error categories', () => {
  it.each([
    'cyber_policy', 'bio_policy', 'misalignment_policy_violation',
    'image_content_policy_violation', 'moderation_blocked', 'content_policy_violation',
    'content_policy', 'content_filter', 'cyber_policy_session_blocked',
    'session_blocked_by_cyber_policy',
  ])('groups %s independently of its historical phase', (code) => {
    for (const phase of ['request', 'upstream', 'internal']) {
      expect(mapErrorCategory(phase, code)).toBe('safety_policy')
    }
  })

  it('keeps ordinary errors and refusals out of the policy category', () => {
    expect(mapErrorCategory('request', 'invalid_request_error')).toBe('invalid_request')
    expect(mapErrorCategory('upstream', 'upstream_error')).toBe('upstream')
    expect(isSafetyPolicyErrorCode('data_residency_mismatch')).toBe(false)
    expect(isSafetyPolicyErrorCode('refusal')).toBe(false)
    expect(isSafetyPolicyErrorCode('error mentions bio_policy')).toBe(false)
    expect(mapErrorCategory('request', ' BIO_POLICY ')).toBe('safety_policy')
  })
})
