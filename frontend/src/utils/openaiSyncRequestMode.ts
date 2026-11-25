export const OPENAI_SYNC_REQUEST_MODES = ['allow', 'deny', 'only'] as const
export type OpenAISyncRequestMode = typeof OPENAI_SYNC_REQUEST_MODES[number]

export function resolveOpenAISyncRequestMode(value: unknown): OpenAISyncRequestMode {
  return value === 'deny' || value === 'only' ? value : 'allow'
}
