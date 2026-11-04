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
    favicon?: string
    defaultTheme?: 'light' | 'dark' | 'system'
    background?: 'aurora' | 'none'
  }
  features: {
    spend?: boolean
    memory?: boolean
    temporaryChat?: boolean
    codeInterpreter?: boolean
    webSearch?: boolean
    apiKeys?: boolean
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
  features: { memory: true, temporaryChat: true, apiKeys: true },
  defaultModels: [],
  modelOrder: [],
  promptSuggestions: [],
  toolDisplay: {},
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
    toolDisplay: raw.toolDisplay && typeof raw.toolDisplay === 'object' ? raw.toolDisplay : {},
    integrations: arr<Integration>(raw.integrations, []).filter((i) => i && (i.kind === 'oauthToolServer' || i.kind === 'extensionInstall')),
  }
  return cached
}

/** Test hook: forget the cached config. */
export function resetUIConfig() {
  cached = undefined
}
