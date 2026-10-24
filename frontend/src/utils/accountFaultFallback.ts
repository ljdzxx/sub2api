export const OPENAI_FAULT_FALLBACK_ONLY = 'openai_fault_fallback_only'

export function withAccountFaultFallback(
  extra: Record<string, unknown> | null | undefined,
  enabled: boolean
): Record<string, unknown> {
  // Keep an explicit false so disabling survives APIs that merge extra fields.
  return { ...extra, [OPENAI_FAULT_FALLBACK_ONLY]: enabled }
}
