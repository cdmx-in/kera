import type { Model, Suggestion } from '@/api/types'

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

/** The start-screen prompts: the model's own when it has any, else the global ones (at most 6). */
export function pickSuggestions(model: Model | undefined, global: Suggestion[]): Suggestion[] {
  const own = model?.suggestionPrompts?.length ? model.suggestionPrompts : (model?.info?.meta?.suggestion_prompts ?? [])
  return (own.length ? own : global).slice(0, 6)
}
