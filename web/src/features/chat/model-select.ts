import type { Model } from '@/api/types'

/**
 * Picks the model for a chat: an explicit ?models= id (hidden allowed), else the
 * chat's last model, else the configured defaults, else the first visible model.
 */
export function resolveModel(models: Model[], opts: { requested?: string[]; chatModel?: string; defaults?: string[] }): string {
  const has = (id?: string) => !!id && models.some((m) => m.id === id)
  const visible = (id?: string) => !!id && models.some((m) => m.id === id && !m.hidden)
  const req = opts.requested?.find(has)
  if (req) return req
  if (has(opts.chatModel)) return opts.chatModel!
  const def = opts.defaults?.find(visible)
  if (def) return def
  return models.find((m) => !m.hidden)?.id ?? ''
}
