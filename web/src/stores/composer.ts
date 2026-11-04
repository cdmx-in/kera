// Per-chat composer choices: model, feature pills and selected tools.
import { create } from 'zustand'

interface ComposerStore {
  model: string
  /** the model came from the defaults or the first-model fallback, not from the user or the chat */
  modelAuto: boolean
  features: Record<string, boolean>
  toolIds: string[]
  setModel(id: string, auto?: boolean): void
  setFeature(id: string, on: boolean): void
  setFeatures(f: Record<string, boolean>): void
  setToolIds(ids: string[]): void
  toggleTool(id: string, on: boolean): void
}

export const useComposer = create<ComposerStore>((set) => ({
  model: '',
  modelAuto: false,
  features: {},
  toolIds: [],
  setModel: (model, auto = false) => set({ model, modelAuto: auto }),
  setFeature: (id, on) => set((s) => ({ features: { ...s.features, [id]: on } })),
  setFeatures: (features) => set({ features }),
  setToolIds: (toolIds) => set({ toolIds: [...new Set(toolIds)] }),
  toggleTool: (id, on) => set((s) => ({ toolIds: on ? [...new Set([...s.toolIds, id])] : s.toolIds.filter((t) => t !== id) })),
}))
