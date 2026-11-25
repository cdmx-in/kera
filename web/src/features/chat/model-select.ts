import type { Model, Suggestion, ToolServer } from '@/api/types'
import type { Integration } from '@/lib/ui-config'
import { toolKey } from '@/features/tools/ids'

/** Embedding and rerank models are listed for admins only and never chat. */
export const isChatModel = (m: Model) => !m.kind || m.kind === 'chat'

/**
 * Picks the model for a chat: an explicit ?models= id (hidden allowed), else the
 * chat's last model, else the configured defaults, else the first visible model.
 */
export function resolveModel(all: Model[], opts: { requested?: string[]; chatModel?: string; defaults?: string[] }): string {
  const models = all.filter(isChatModel)
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

/**
 * The composer model after the model list (or the URL) changed, or null to keep it. An automatic
 * pick (defaults or the first-model fallback) is redone on every change, so a default missing from a
 * partial list (a model server slow to answer) wins once it is back; a user's or chat's choice stays.
 */
export function nextModel(
  models: Model[],
  s: { current: string; auto: boolean; requested: string[]; inChat: boolean; defaults: string[] },
): { id: string; auto: boolean } | null {
  const chatModel = s.inChat ? s.current : undefined
  const id = resolveModel(models, { requested: s.requested, chatModel, defaults: s.defaults })
  const usable = models.some((m) => m.id === s.current && isChatModel(m))
  if (s.requested.length === 0 && s.current && !s.auto && usable) return null
  return { id, auto: !s.requested.includes(id) && id !== chatModel }
}

/** The tools a model starts with: its own, plus connected apps that switch on by themselves (an integration's autoEnable, a server's auto_enable). */
export function defaultToolIds(m: Model, servers: ToolServer[] | undefined, integrations: Integration[]): string[] {
  const connected = (id: string) => servers?.some((t) => t.id === id && t.authenticated)
  const auto = [
    ...integrations.flatMap((i) => (i.kind === 'oauthToolServer' && i.autoEnable && connected(i.toolServerId) ? [i.toolServerId] : [])),
    ...(servers ?? []).filter((t) => t.auto_enable && t.authenticated).map((t) => t.id),
  ]
  return [...(m.toolIds ?? []).map(toolKey), ...auto]
}

/** The ability switches a model starts with (its own defaults). */
export const defaultFeatures = (m: Model): Record<string, boolean> => Object.fromEntries((m.features ?? []).map((f) => [f.id, !!f.default]))
