// window.__UI__ is rendered into index.html by the server (GET /api/v1/ui/config
// has the same JSON). Every field is optional here; uiConfig() fills defaults so
// the app runs unbranded when the object is absent (dev server, tests).

export type ToolKind = 'search' | 'read' | 'code' | 'ask' | 'write' | 'memory' | 'other'

export interface ToolDisplay {
  label: string
  icon?: string
  kind?: ToolKind
  plural?: string
}

export interface OAuthToolServerIntegration {
  id: string
  kind: 'oauthToolServer'
  toolServerId: string
  label: string
  icon?: string
  autoEnable?: boolean
  showInPlusMenu?: boolean
}

export interface ExtensionInstallIntegration {
  id: string
  kind: 'extensionInstall'
  label: string
  version: string
  downloadUrl: string
  markerAttr: string
  platforms?: string[]
  steps?: { install?: string; update?: string }
  commands?: { windows?: string; posix?: string }
}

export type Integration = OAuthToolServerIntegration | ExtensionInstallIntegration

export interface UIConfig {
  brand: {
    name: string
    tagline?: string
    logo?: { light?: string; dark?: string }
    /** Small square logo (sidebar, login card); falls back to favicon. */
    mark?: string
    favicon?: string
    faviconAlt?: string[]
    appleTouchIcon?: string
    pwaIcons?: string[]
    splash?: { logo?: string; logoDark?: string; wordmark?: string; style?: 'orbit' | 'plain' }
    accent?: { base?: string; bright?: string }
    grey?: { hue?: number; chroma?: number; scale?: string[] }
    defaultTheme?: 'light' | 'dark' | 'system'
    background?: 'aurora' | 'none'
    login?: { wordmark?: string; style?: 'glass-card' | 'plain' }
  }
  features: {
    memory?: boolean
    codeInterpreter?: boolean
    webSearch?: boolean
  }
  defaultModels: string[]
  modelOrder: string[]
  promptSuggestions: { title?: string | string[]; content: string }[]
  toolDisplay: Record<string, ToolDisplay>
  integrations: Integration[]
}

declare global {
  interface Window {
    __UI__?: Partial<UIConfig>
  }
}

const defaults: UIConfig = {
  brand: { name: 'Kera', defaultTheme: 'system', background: 'none' },
  features: { memory: true },
  defaultModels: [],
  modelOrder: [],
  promptSuggestions: [],
  // core tools; the server's tool_display (and tool-server display) override these
  toolDisplay: {
    search_web: { label: 'web search', kind: 'search', plural: 'web searches' },
    fetch_url: { label: 'page read', icon: 'globe', kind: 'read' },
    ask_user: { label: 'question to you', kind: 'ask', plural: 'questions to you' },
    list_knowledge: { label: 'knowledge base list', icon: 'book', kind: 'read' },
    search_knowledge_files: { label: 'file lookup', icon: 'file', kind: 'search' },
    query_knowledge_files: { label: 'knowledge search', kind: 'search', plural: 'knowledge searches' },
    view_knowledge_file: { label: 'document read', icon: 'file', kind: 'read' },
    view_file: { label: 'file read', icon: 'file', kind: 'read' },
    grep_chat_files: { label: 'file search', icon: 'file', kind: 'search', plural: 'file searches' },
    execute_code: { label: 'code run', kind: 'code' },
    get_current_timestamp: { label: 'clock check', kind: 'other' },
    search_memories: { label: 'memory search', kind: 'memory', plural: 'memory searches' },
    add_memory: { label: 'memory save', kind: 'memory' },
  },
  integrations: [],
}

let cached: UIConfig | undefined

export function uiConfig(): UIConfig {
  if (cached) return cached
  const raw = (typeof window !== 'undefined' && window.__UI__) || {}
  const arr = <T,>(v: unknown, d: T[]): T[] => (Array.isArray(v) ? (v as T[]) : d)
  cached = {
    brand: { ...defaults.brand, ...(raw.brand ?? {}) },
    features: { ...defaults.features, ...(raw.features ?? {}) },
    defaultModels: arr(raw.defaultModels, defaults.defaultModels),
    modelOrder: arr(raw.modelOrder, defaults.modelOrder),
    promptSuggestions: arr(raw.promptSuggestions, defaults.promptSuggestions),
    toolDisplay: { ...defaults.toolDisplay, ...(raw.toolDisplay && typeof raw.toolDisplay === 'object' ? raw.toolDisplay : {}) },
    integrations: arr<Integration>(raw.integrations, []).filter((i) => i && (i.kind === 'oauthToolServer' || i.kind === 'extensionInstall')),
  }
  return cached
}

/** Test hook: forget the cached config. */
export function resetUIConfig() {
  cached = undefined
}
