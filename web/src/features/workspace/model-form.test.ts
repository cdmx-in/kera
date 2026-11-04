import { describe, expect, it } from 'vitest'
import { formToBody, ingestSummary, parseParams, rowToForm, validModelId, type ModelRow } from './model-form'

const row: ModelRow = {
  id: 'hr-policies',
  name: 'HR Policies',
  base_model_id: 'gpt-x',
  params: { system: 'You answer HR questions.', num_ctx: 65536, think: false },
  meta: {
    description: 'Policies',
    toolIds: ['server:kb-tools', 'builtin:knowledge'],
    builtinTools: { knowledge: true, ask_user: false },
    knowledge: [{ id: '', name: 'Policies', mode: 'full' }],
    capabilities: { vision: true, custom: 1 },
    suggestion_prompts: [{ title: ['Leave', 'how many days'], content: 'How many leave days?' }],
    features: { web_search: 'native', code_interpreter: true, other: 'x' },
    defaultFeatures: ['web_search'],
    fileContext: 'full',
    hidden: true,
  },
  is_active: true,
  managed: true,
}

describe('model form', () => {
  it('round-trips a preset row', () => {
    const f = rowToForm(row)
    expect(f.system).toBe('You answer HR questions.')
    expect(JSON.parse(f.paramsText)).toEqual({ num_ctx: 65536, think: false })
    expect(f.servers).toEqual(['kb-tools'])
    expect(f.builtins).toEqual({ knowledge: 'on', ask_user: 'off' })
    expect(f.webSearch).toBe('native')
    expect(f.extraFeatures).toEqual({ other: 'x' })
    const b = formToBody(f) as { params: Record<string, unknown>; meta: Record<string, unknown>; base_model_id: string }
    expect(b.params).toEqual({ num_ctx: 65536, think: false, system: 'You answer HR questions.' })
    expect(b.base_model_id).toBe('gpt-x')
    expect(b.meta.toolIds).toEqual(['server:kb-tools', 'builtin:knowledge'])
    expect(b.meta.builtinTools).toEqual({ knowledge: true, ask_user: false })
    expect(b.meta.knowledge).toEqual([{ id: '', name: 'Policies', mode: 'full' }])
    expect(b.meta.features).toEqual({ other: 'x', web_search: 'native', code_interpreter: true, memory: false })
    expect(b.meta.capabilities).toEqual({ vision: true, custom: 1 })
    expect(b.meta.suggestion_prompts).toEqual([{ title: ['Leave', 'how many days'], content: 'How many leave days?' }])
    expect(b.meta.fileContext).toBe('full')
    expect(b.meta.hidden).toBe(true)
  })

  it('drops defaults and empty entries', () => {
    const f = rowToForm({ id: 'm', name: '' })
    f.builtins = { memory: 'default', time: 'on' }
    f.knowledge = [{ id: 'k1', mode: 'retrieve' }, {}]
    f.suggestions = [{ title: [' '], content: '' }]
    f.defaultFeatures = ['web_search', 'memory']
    const b = formToBody(f) as { name: string; params: unknown; meta: Record<string, unknown> }
    expect(b.name).toBe('m')
    expect(b.params).toEqual({})
    expect(b.meta.builtinTools).toEqual({ time: true })
    expect(b.meta.toolIds).toEqual(['builtin:time'])
    expect(b.meta.knowledge).toEqual([{ id: 'k1' }])
    expect(b.meta.suggestion_prompts).toEqual([])
    expect(b.meta.defaultFeatures).toEqual(['memory']) // web search is off
    expect((b.meta.features as Record<string, unknown>).web_search).toBe(false)
  })

  it('validates params and ids', () => {
    expect(parseParams('')).toEqual({})
    expect(() => parseParams('[1]')).toThrow(/object/)
    expect(() => parseParams('{')).toThrow(/valid JSON/)
    expect(validModelId('web.gpt-6:latest')).toBe(true)
    expect(validModelId('has space')).toBe(false)
  })

  it('summarises ingestion', () => {
    expect(ingestSummary(undefined)).toBe('No files')
    expect(ingestSummary({ files: 3, completed: 1, pending: 1, processing: 0, failed: 1 })).toBe('3 files · 1 indexing · 1 failed')
  })
})
