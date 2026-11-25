// Starting state of the composer store for the wizard (assistant, its switches, the apps), and what picking another assistant does.
// The old form let useModelSetup re-apply an assistant's defaults whenever the list of apps changed; here it happens once, then
// only when the person picks another assistant, so a connection check can never undo their choices.
import { useEffect, useRef, useState } from 'react'
import { useModels, useTools } from '@/api/queries'
import type { Model, ToolServer } from '@/api/types'
import { uiConfig } from '@/lib/ui-config'
import { useComposer } from '@/stores/composer'
import { defaultFeatures, defaultToolIds, nextModel } from '@/features/chat/model-select'
import { toolKey } from '@/features/tools/ids'

/** What a saved task or a kept draft starts the picks as, instead of the assistant's defaults. */
export interface Preset {
  model_id: string
  /** null = the assistant's default switches */
  features: Record<string, boolean> | null
  tool_ids: string[]
}

/** The composer store's assistant, switches and apps for a new wizard; true once they are set. `urlModels` and `urlTools` are ?models= and ?tools=. */
export function useWizardSetup(urlModels: string[], urlTools: string[], preset?: Preset): boolean {
  const models = useModels()
  const tools = useTools()
  const [ready, setReady] = useState(false)
  const first = useRef({ urlModels, urlTools, preset })
  const loaded = !!models.data && !tools.isPending
  useEffect(() => {
    if (ready || !loaded || !models.data) return
    const { urlModels, urlTools, preset } = first.current
    const c = useComposer.getState()
    const next = nextModel(models.data, { current: c.model, auto: c.modelAuto, requested: preset ? [preset.model_id] : urlModels, inChat: false, defaults: uiConfig().defaultModels })
    if (next) c.setModel(next.id, next.auto)
    const m = models.data.find((x) => x.id === useComposer.getState().model)
    if (m) {
      const own = preset?.model_id === m.id ? preset : undefined
      c.setFeatures(own?.features ?? defaultFeatures(m))
      c.setToolIds([...(own ? own.tool_ids.map(toolKey) : defaultToolIds(m, tools.data, uiConfig().integrations)), ...urlTools])
    }
    setReady(true)
  }, [ready, loaded, models.data, tools.data])
  return ready
}

/** The assistant's own switches and apps, replacing what is chosen now. */
export function applyDefaults(m: Model, servers: ToolServer[] | undefined) {
  const c = useComposer.getState()
  c.setFeatures(defaultFeatures(m))
  c.setToolIds(defaultToolIds(m, servers, uiConfig().integrations))
}

/** Picks an assistant: it brings its own switches and apps, as in a new chat. Picking the one already chosen changes nothing. */
export function chooseAssistant(m: Model, servers: ToolServer[] | undefined) {
  if (useComposer.getState().model === m.id) return
  useComposer.getState().setModel(m.id)
  applyDefaults(m, servers)
}
