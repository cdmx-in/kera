import { describe, expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import { rowToForm } from './model-form'
import { paramsError, serverFieldError, validateCollection, validateModel, validatePrompt } from './validate'

const form = (o: Partial<ReturnType<typeof rowToForm>> = {}) => ({ ...rowToForm({ id: 'hr', name: 'HR', base_model_id: 'gpt' }), ...o })

describe('validateModel', () => {
  const opts = { create: true, isPreset: true, existingIds: ['gpt', 'astra'] }
  it('accepts a valid preset', () => {
    expect(validateModel(form(), opts)).toEqual({})
  })
  it('requires id, name and base model', () => {
    expect(validateModel(form({ id: ' ', name: '', baseModelId: '' }), opts)).toEqual({ id: 'Model id is required', name: 'Name is required', baseModelId: 'Choose a base model' })
  })
  it('checks the id format and uniqueness on create only', () => {
    expect(validateModel(form({ id: 'HR Bot' }), opts).id).toMatch(/lowercase/)
    expect(validateModel(form({ id: '-x' }), opts).id).toMatch(/lowercase/)
    expect(validateModel(form({ id: 'astra' }), opts).id).toMatch(/already exists/)
    expect(validateModel(form({ id: 'qwen3:27b-q4_k.m' }), opts)).toEqual({})
    expect(validateModel(form({ id: 'Legacy_ID' }), { ...opts, create: false })).toEqual({})
    expect(validateModel(form({ id: 'astra' }), { ...opts, create: false })).toEqual({})
  })
  it('does not need a base model for base model settings', () => {
    expect(validateModel(form({ baseModelId: '' }), { ...opts, isPreset: false })).toEqual({})
  })
  it('checks knowledge modes', () => {
    expect(validateModel(form({ knowledge: [{ id: 'k', mode: 'full' }, { id: 'j', mode: '' }] }), opts)).toEqual({})
    expect(validateModel(form({ knowledge: [{ id: 'k', mode: 'everything' }] }), opts).knowledge).toMatch(/everything/)
  })
})

describe('paramsError', () => {
  it('parses JSON', () => {
    expect(paramsError('')).toBeUndefined()
    expect(paramsError('{"temperature": 0.3, "num_ctx": 65536, "think": false}')).toBeUndefined()
    expect(paramsError('{oops')).toMatch(/not valid JSON/)
    expect(paramsError('[1]')).toMatch(/JSON object/)
  })
  it('checks ranges', () => {
    expect(paramsError('{"temperature": 2.5}')).toMatch(/0 to 2/)
    expect(paramsError('{"temperature": "hot"}')).toMatch(/0 to 2/)
    expect(paramsError('{"top_p": 1.1}')).toMatch(/0 to 1/)
    expect(paramsError('{"num_ctx": 0}')).toMatch(/num_ctx/)
    expect(paramsError('{"num_ctx": 1.5}')).toMatch(/num_ctx/)
    expect(paramsError('{"max_tokens": -1}')).toMatch(/max_tokens/)
    expect(paramsError('{"num_predict": -1}')).toBeUndefined()
    expect(paramsError('{"num_predict": -3}')).toMatch(/num_predict/)
  })
})

describe('validatePrompt', () => {
  const others = [{ id: 'p1', command: 'summary' }]
  it('checks the command', () => {
    expect(validatePrompt({ command: 'tr', content: 'x' }, others)).toEqual({})
    expect(validatePrompt({ command: '/tr', content: 'x' }, others)).toEqual({})
    expect(validatePrompt({ command: '', content: '' }, others)).toEqual({ command: 'Command is required', content: 'Prompt text is required' })
    expect(validatePrompt({ command: 'two words', content: 'x' }, others).command).toMatch(/letters/)
    expect(validatePrompt({ command: 'Summary', content: 'x' }, others).command).toMatch(/already have/)
    expect(validatePrompt({ id: 'p1', command: 'summary', content: 'x' }, others)).toEqual({})
  })
})

describe('validateCollection', () => {
  it('requires a name', () => {
    expect(validateCollection({ name: ' ' })).toEqual({ name: 'Name is required' })
    expect(validateCollection({ name: 'Docs' })).toEqual({})
  })
})

describe('serverFieldError', () => {
  it('maps known codes to fields', () => {
    expect(serverFieldError(new ApiError(409, 'a model with this id already exists', 'model_exists'))).toEqual(['id', 'a model with this id already exists'])
    expect(serverFieldError(new ApiError(500, 'boom', 'internal'))).toBeUndefined()
    expect(serverFieldError(new Error('x'))).toBeUndefined()
  })
})
