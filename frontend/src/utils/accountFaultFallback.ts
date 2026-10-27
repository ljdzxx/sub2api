export const OPENAI_FAULT_FALLBACK_ONLY = 'openai_fault_fallback_only'
export const OPENAI_FAULT_FALLBACK_STATUS_CODES = 'openai_fault_fallback_status_codes'
export const OPENAI_FAULT_FALLBACK_KEYWORDS = 'openai_fault_fallback_keywords'
export const OPENAI_FAULT_FALLBACK_MATCH_MODE = 'openai_fault_fallback_match_mode'
export const DEFAULT_FAULT_FALLBACK_STATUS_CODES = '429, 500-599'
export type FaultFallbackMatchMode = 'any' | 'all'

export function parseFaultFallbackKeywords(input: string): string[] | null {
  const keywords = input.split(/\r?\n/).map(value => value.trim()).filter(Boolean)
  if (keywords.length > 100 || keywords.some(keyword => [...keyword].length > 500)) return null
  return keywords
}

export function formatFaultFallbackKeywords(extra: Record<string, unknown> | null | undefined): string {
  const raw = extra?.[OPENAI_FAULT_FALLBACK_KEYWORDS]
  if (raw === undefined) return ''
  return Array.isArray(raw) ? raw.join('\n') : String(raw)
}

export function getFaultFallbackMatchMode(extra: Record<string, unknown> | null | undefined): FaultFallbackMatchMode {
  return extra?.[OPENAI_FAULT_FALLBACK_MATCH_MODE] === 'all' ? 'all' : 'any'
}

export function parseFaultFallbackStatusCodes(input: string): number[] | null {
  if (!input.trim()) return []
  const codes = new Set<number>()
  for (const token of input.trim().split(/[,，\s]+/).filter(Boolean)) {
    let first: number
    let last: number
    if (/^[\dx]{3}$/i.test(token) && /x/i.test(token)) {
      const pattern = token.toLowerCase()
      let matched = false
      for (let code = 400; code <= 599; code++) {
        if ([...pattern].every((char, index) => char === 'x' || char === String(code)[index])) {
          codes.add(code)
          matched = true
        }
      }
      if (!matched) return null
      continue
    } else if (/^\d{3}-\d{3}$/.test(token)) {
      [first, last] = token.split('-').map(Number)
    } else if (/^\d{3}$/.test(token)) {
      first = last = Number(token)
    } else {
      return null
    }
    if (first < 400 || last > 599 || first > last) return null
    for (let code = first; code <= last; code++) codes.add(code)
  }
  return [...codes].sort((a, b) => a - b)
}

export function formatFaultFallbackStatusCodes(extra: Record<string, unknown> | null | undefined): string {
  const raw = extra?.[OPENAI_FAULT_FALLBACK_STATUS_CODES]
  if (raw === undefined) return DEFAULT_FAULT_FALLBACK_STATUS_CODES
  if (!Array.isArray(raw)) return String(raw)
  const codes = [...new Set(raw)].sort((a, b) => Number(a) - Number(b))
  const tokens: string[] = []
  for (let i = 0; i < codes.length; i++) {
    const first = codes[i]
    let last = first
    while (i + 1 < codes.length && typeof last === 'number' && codes[i + 1] === last + 1) last = codes[++i]
    tokens.push(first === last ? String(first) : `${first}-${last}`)
  }
  return tokens.join(', ')
}

export function withAccountFaultFallback(
  extra: Record<string, unknown> | null | undefined,
  enabled: boolean,
  statusCodes?: number[],
  matching?: { keywords: string[]; matchMode: FaultFallbackMatchMode }
): Record<string, unknown> {
  // Keep an explicit false so disabling survives APIs that merge extra fields.
  return {
    ...extra,
    [OPENAI_FAULT_FALLBACK_ONLY]: enabled,
    ...(statusCodes === undefined ? {} : { [OPENAI_FAULT_FALLBACK_STATUS_CODES]: statusCodes }),
    ...(matching === undefined ? {} : {
      [OPENAI_FAULT_FALLBACK_KEYWORDS]: matching.keywords,
      [OPENAI_FAULT_FALLBACK_MATCH_MODE]: matching.matchMode
    })
  }
}
