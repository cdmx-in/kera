// Pure conversions between the model row API shape (GET /api/v1/models/model)
// and the editor form. Kept free of React so they are unit-tested.

export type Json = Record<string, unknown>

export interface KnowledgeRef {
  id?: string
  name?: string
  mode?: string
}

export interface Suggestion {
  title?: string[]
  content: string
}

export interface ModelRow {
  id: string
  user_id?: string | null
  base_model_id?: string | null
  connection_id?: string | null
  name: string
  params?: Json
  meta?: {
    description?: string
    profile_image_url?: string
    toolIds?: string[]
    builtinTools?: Record<string, boolean>
    disabledTools?: string[]
    knowledge?: KnowledgeRef[]
    capabilities?: Json
    suggestion_prompts?: Suggestion[] | null
    hidden?: boolean
    features?: Json
    defaultFeatures?: string[]
    fileContext?: string
    hooks?: string[]
    sort?: number
  }
  is_active?: boolean
  managed?: boolean
  created_at?: number
  updated_at?: number
}

/** web_search is native (provider tool), builtin (Kera's web tools) or off. */
export type WebSearchMode = 'native' | 'builtin' | 'off'
/** A builtin tool set is on, off, or left to the default. */
export type Toggle = 'default' | 'on' | 'off'

export interface ModelForm {
  id: string
  name: string
  baseModelId: string
  description: string
  image: string
  system: string
  paramsText: string // JSON object without "system"
  capabilities: Json
  webSearch: WebSearchMode
  codeInterpreter: boolean
  memory: boolean
  extraFeatures: Json // feature keys the editor has no control for, kept as they are
  defaultFeatures: string[]
  fileContext: 'tools' | 'full'
  servers: string[]
  builtins: Record<string, Toggle>
  knowledge: KnowledgeRef[]
  suggestions: Suggestion[]
  hidden: boolean
  active: boolean
}

export const BUILTINS = ['ask_user', 'memory', 'knowledge', 'files', 'web', 'code', 'time', 'chats', 'skills'] as const
export const CAPABILITIES = ['vision', 'file_upload', 'citations', 'status_updates', 'usage'] as const
export const FEATURES = ['web_search', 'code_interpreter', 'memory'] as const

const bool = (v: unknown) => v === true || v === 'true'

export function rowToForm(r: ModelRow): ModelForm {
  const m = r.meta ?? {}
  const params = { ...(r.params ?? {}) }
  const system = typeof params.system === 'string' ? params.system : ''
  delete params.system
  const f = { ...(m.features ?? {}) }
  const ws = f.web_search
  const webSearch: WebSearchMode = ws === 'native' ? 'native' : ws === 'builtin' || ws === true ? 'builtin' : 'off'
  const codeInterpreter = bool(f.code_interpreter)
  const memory = bool(f.memory)
  for (const k of FEATURES) delete f[k]
  const builtins: Record<string, Toggle> = {}
  for (const [k, v] of Object.entries(m.builtinTools ?? {})) builtins[k] = v ? 'on' : 'off'
  return {
    id: r.id,
    name: r.name ?? '',
    baseModelId: r.base_model_id ?? '',
    description: m.description ?? '',
    image: m.profile_image_url ?? '',
    system,
    paramsText: Object.keys(params).length ? JSON.stringify(params, null, 2) : '',
    capabilities: { ...(m.capabilities ?? {}) },
    webSearch,
    codeInterpreter,
    memory,
    extraFeatures: f,
    defaultFeatures: [...(m.defaultFeatures ?? [])],
    fileContext: m.fileContext === 'full' ? 'full' : 'tools',
    servers: (m.toolIds ?? []).filter((t) => !t.startsWith('builtin:')).map((t) => t.replace(/^server:/, '')),
    builtins,
    knowledge: (m.knowledge ?? []).map((k) => ({ ...k })),
    suggestions: (m.suggestion_prompts ?? []).map((s) => ({ title: s.title ? [...s.title] : [], content: s.content ?? '' })),
    hidden: !!m.hidden,
    active: r.is_active !== false,
  }
}

/** Parses the params textarea; "" is {}. Throws with a readable message. */
export function parseParams(text: string): Json {
  if (!text.trim()) return {}
  let v: unknown
  try {
    v = JSON.parse(text)
  } catch (e) {
    throw new Error(`Parameters are not valid JSON: ${(e as Error).message}`)
  }
  if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('Parameters must be a JSON object')
  return v as Json
}

/** Builds the create/update body. Throws when params are invalid. */
export function formToBody(f: ModelForm): Json {
  const params = parseParams(f.paramsText)
  delete params.system
  if (f.system.trim()) params.system = f.system
  const features: Json = { ...f.extraFeatures, web_search: f.webSearch === 'off' ? false : f.webSearch, code_interpreter: f.codeInterpreter, memory: f.memory }
  const builtinTools: Record<string, boolean> = {}
  for (const [k, v] of Object.entries(f.builtins)) if (v !== 'default') builtinTools[k] = v === 'on'
  const toolIds = [...f.servers.map((s) => `server:${s}`), ...Object.entries(builtinTools).filter(([, v]) => v).map(([k]) => `builtin:${k}`)]
  const knowledge = f.knowledge
    .filter((k) => k.id || k.name)
    .map((k) => ({ ...(k.id ? { id: k.id } : { id: '', name: k.name }), ...(k.mode && k.mode !== 'retrieve' ? { mode: k.mode } : {}) }))
  const suggestions = f.suggestions
    .filter((s) => s.content.trim())
    .map((s) => ({ title: (s.title ?? []).map((t) => t.trim()).filter(Boolean), content: s.content }))
  return {
    id: f.id.trim(),
    name: f.name.trim() || f.id.trim(),
    base_model_id: f.baseModelId,
    params,
    is_active: f.active,
    meta: {
      description: f.description,
      profile_image_url: f.image,
      capabilities: f.capabilities,
      features,
      defaultFeatures: f.defaultFeatures.filter((d) => d !== 'web_search' || f.webSearch !== 'off'),
      fileContext: f.fileContext,
      toolIds,
      builtinTools,
      knowledge,
      suggestion_prompts: suggestions,
      hidden: f.hidden,
    },
  }
}

/** A model id: letters, digits and . _ : - (no spaces). */
export function validModelId(id: string): boolean {
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(id)
}

/** Knowledge ingestion summary for a list row. */
export function ingestSummary(s?: { files: number; completed: number; pending: number; processing: number; failed: number }): string {
  if (!s || !s.files) return 'No files'
  const parts = [`${s.files} file${s.files === 1 ? '' : 's'}`]
  const busy = s.pending + s.processing
  if (busy) parts.push(`${busy} indexing`)
  if (s.failed) parts.push(`${s.failed} failed`)
  return parts.join(' · ')
}
