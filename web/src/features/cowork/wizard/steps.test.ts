import { describe, expect, it } from 'vitest'
import { initialDraft, type Draft } from './draft'
import { canOpen, firstIncomplete, isComplete, stepFromParam, stepList, validateStep, type Context } from './steps'

const base: Draft = { ...initialDraft({ zone: 'UTC', schedulable: true }), goal: 'do it' }
const ctx: Context = { model: 'm1', modelKnown: true, uploading: false }
const all = stepList(true)

describe('stepList', () => {
  it('has no Schedule step for people who may not schedule', () => {
    expect(all).toEqual(['what', 'assistant', 'tools', 'when', 'review'])
    expect(stepList(false)).toEqual(['what', 'assistant', 'tools', 'review'])
  })
})

describe('validateStep', () => {
  it('needs a goal, within the limits, and no upload still going', () => {
    expect(validateStep('what', base, ctx)).toEqual({})
    expect(validateStep('what', { ...base, goal: '   ' }, ctx).goal).toMatch(/Describe what/)
    expect(validateStep('what', { ...base, goal: 'x'.repeat(50001) }, ctx).goal).toMatch(/too long/)
    expect(validateStep('what', { ...base, goal: 'é'.repeat(30000) }, ctx).goal).toMatch(/too long/) // the server counts bytes
    expect(validateStep('what', { ...base, title: 'x'.repeat(121) }, ctx).title).toMatch(/120/)
    expect(validateStep('what', base, { ...ctx, uploading: true }).files).toMatch(/finish uploading/)
  })
  it('needs an assistant the person may pick', () => {
    expect(validateStep('assistant', base, ctx)).toEqual({})
    expect(validateStep('assistant', base, { ...ctx, model: '' }).model).toMatch(/Choose/)
    expect(validateStep('assistant', base, { ...ctx, modelKnown: false }).model).toMatch(/Choose/)
  })
  it('asks nothing of the apps step', () => {
    expect(validateStep('tools', { ...base, goal: '' }, { ...ctx, model: '' })).toEqual({})
  })
  it('checks the timing only when it is a schedule', () => {
    expect(validateStep('when', { ...base, when: 'now', time: '' }, ctx)).toEqual({})
    expect(validateStep('when', { ...base, when: 'daily', time: '' }, ctx).time).toMatch(/time of day/)
    expect(validateStep('when', { ...base, when: 'weekly', time: '25:00' }, ctx).time).toBeDefined()
    expect(validateStep('when', { ...base, when: 'weekdays', time: '07:05' }, ctx)).toEqual({})
    expect(validateStep('when', { ...base, when: 'hourly', minute: 60 }, ctx).minute).toMatch(/0 to 59/)
    expect(validateStep('when', { ...base, when: 'hourly', minute: 59, time: '' }, ctx)).toEqual({})
    expect(validateStep('when', { ...base, when: 'daily', timezone: '' }, ctx).timezone).toBeDefined()
  })
  it('has nothing of its own on the review step', () => {
    expect(validateStep('review', { ...base, goal: '' }, ctx)).toEqual({})
  })
})

describe('reaching a step', () => {
  const noGoal = { ...base, goal: '' }
  it('opens a step only when every step before it is complete', () => {
    expect(canOpen('what', all, noGoal, ctx)).toBe(true)
    expect(canOpen('assistant', all, noGoal, ctx)).toBe(false)
    expect(canOpen('review', all, base, ctx)).toBe(true)
    expect(canOpen('review', all, base, { ...ctx, model: '' })).toBe(false)
    expect(canOpen('review', all, { ...base, when: 'daily', time: '' }, ctx)).toBe(false)
  })
  it('finds the first step that is not complete', () => {
    expect(firstIncomplete(all, base, ctx)).toBeUndefined()
    expect(firstIncomplete(all, noGoal, ctx)).toBe('what')
    expect(firstIncomplete(all, base, { ...ctx, model: '' })).toBe('assistant')
    expect(isComplete('what', base, ctx)).toBe(true)
  })
})

describe('stepFromParam (the ?step= of the address)', () => {
  it('opens the step the address names', () => {
    expect(stepFromParam('tools', all, base, ctx)).toBe('tools')
    expect(stepFromParam('review', all, base, ctx)).toBe('review')
  })
  it('opens the first step when there is none, or an unknown one', () => {
    expect(stepFromParam(null, all, base, ctx)).toBe('what')
    expect(stepFromParam('nope', all, base, ctx)).toBe('what')
  })
  it('does not open a step you could not reach: it falls back to the first one that is not complete', () => {
    expect(stepFromParam('tools', all, { ...base, goal: '' }, ctx)).toBe('what')
    expect(stepFromParam('review', all, base, { ...ctx, model: '' })).toBe('assistant')
  })
  it('knows no Schedule step for people who may not schedule', () => {
    expect(stepFromParam('when', stepList(false), base, ctx)).toBe('what')
  })
})
