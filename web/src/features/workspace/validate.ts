// Client-side validation for the workspace editors (unit-tested in
// validate.test.ts). The server checks again; serverFieldError maps its
// error codes back onto the same fields.
import { ApiError } from '@/api/client'
import { parseParams, type ModelForm } from './model-form'

export type Errors = Record<string, string>

/** New model ids: lowercase letters, digits, dot, dash, colon, underscore. */
export const MODEL_ID_RE = /^[a-z0-9][a-z0-9._:-]{0,127}$/
/** Prompt commands, as the server normalises them (it lowercases). */
export const COMMAND_RE = /^[a-z0-9_-]{1,64}$/i
export const KNOWLEDGE_MODES = ['', 'auto', 'retrieve', 'tools', 'full']

const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v)
const posInt = (v: unknown) => num(v) && Number.isInteger(v) && (v as number) > 0

/** Range checks on known generation params; unknown keys pass. */
const paramRules: Record<string, [check: (v: unknown) => boolean, msg: string]> = {
  temperature: [(v) => num(v) && (v as number) >= 0 && (v as number) <= 2, 'temperature must be a number from 0 to 2'],
  top_p: [(v) => num(v) && (v as number) >= 0 && (v as number) <= 1, 'top_p must be a number from 0 to 1'],
  min_p: [(v) => num(v) && (v as number) >= 0 && (v as number) <= 1, 'min_p must be a number from 0 to 1'],
  top_k: [posInt, 'top_k must be a positive whole number'],
  num_ctx: [posInt, 'num_ctx must be a positive whole number'],
  max_tokens: [posInt, 'max_tokens must be a positive whole number'],
  max_completion_tokens: [posInt, 'max_completion_tokens must be a positive whole number'],
  // Ollama: -1 = no limit, -2 = fill the context.
  num_predict: [(v) => v === -1 || v === -2 || posInt(v), 'num_predict must be a positive whole number, -1 or -2'],
}

/** The first problem with the params JSON text, or undefined. */
export function paramsError(text: string): string | undefined {
  let p
  try {
    p = parseParams(text)
  } catch (e) {
    return (e as Error).message
  }
  for (const [k, [ok, msg]] of Object.entries(paramRules)) if (k in p && !ok(p[k])) return msg
  return undefined
}

export function validateModel(f: ModelForm, o: { create: boolean; isPreset: boolean; existingIds: Iterable<string> }): Errors {
  const e: Errors = {}
  const id = f.id.trim()
  if (!id) e.id = 'Model id is required'
  else if (o.create && !MODEL_ID_RE.test(id)) e.id = 'Use lowercase letters, digits and . _ : - only'
  else if (o.create && new Set(o.existingIds).has(id)) e.id = 'A model with this id already exists'
  if (!f.name.trim()) e.name = 'Name is required'
  if (o.isPreset && !f.baseModelId) e.baseModelId = 'Choose a base model'
  const pe = paramsError(f.paramsText)
  if (pe) e.paramsText = pe
  const bad = f.knowledge.find((k) => !KNOWLEDGE_MODES.includes(k.mode ?? ''))
  if (bad) e.knowledge = `Unknown knowledge mode "${bad.mode}"`
  return e
}

export function validatePrompt(p: { id?: string; command: string; content: string }, others: { id: string; command: string }[]): Errors {
  const e: Errors = {}
  const c = p.command.trim().replace(/^\//, '')
  if (!c) e.command = 'Command is required'
  else if (!COMMAND_RE.test(c)) e.command = '1-64 letters, digits, - or _'
  else if (others.some((o) => o.id !== p.id && o.command.toLowerCase() === c.toLowerCase())) e.command = `You already have /${c.toLowerCase()}`
  if (!p.content.trim()) e.content = 'Prompt text is required'
  return e
}

export function validateCollection(c: { name: string }): Errors {
  return c.name.trim() ? {} : { name: 'Name is required' }
}

/** Server error codes that belong to one field of the form. */
export const serverCodes: Record<string, string> = {
  missing_id: 'id',
  model_exists: 'id',
  bad_base_model: 'baseModelId',
  bad_command: 'command',
  command_taken: 'command',
  missing_name: 'name',
}

/** [field, message] when a server error names a field the form shows. */
export function serverFieldError(err: unknown): [string, string] | undefined {
  if (err instanceof ApiError && err.code && serverCodes[err.code]) return [serverCodes[err.code], err.message]
  return undefined
}
