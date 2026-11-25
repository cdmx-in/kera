// The wizard's steps: their order, what each one needs before Next, and how a URL names one.
import type { Draft } from './draft'

export type StepId = 'what' | 'assistant' | 'tools' | 'when' | 'review'

export const STEP_LABEL: Record<StepId, string> = { what: 'Goal', assistant: 'Assistant', tools: 'Apps and tools', when: 'Schedule', review: 'Review' }

/** People who may not schedule have no When step: their tasks start once, now. */
export function stepList(schedulable: boolean): StepId[] {
  return schedulable ? ['what', 'assistant', 'tools', 'when', 'review'] : ['what', 'assistant', 'tools', 'review']
}

/** What the page knows besides the draft: the assistant in the composer store and the uploads. */
export interface Context {
  model: string
  /** the assistant is one the user may pick (the list has loaded and has it) */
  modelKnown: boolean
  uploading: boolean
}

export type Errors = Partial<Record<'goal' | 'title' | 'files' | 'model' | 'time' | 'minute' | 'timezone', string>>

/** The server's limit on the goal, in bytes. */
export const GOAL_MAX_BYTES = 50000
export const TITLE_MAX = 120

const bytes = (s: string) => new TextEncoder().encode(s).length

/** Problems that stop Next on one step, by field. Empty = the step is complete. */
export function validateStep(id: StepId, d: Draft, c: Context): Errors {
  const e: Errors = {}
  if (id === 'what') {
    if (!d.goal.trim()) e.goal = 'Describe what the assistant should do.'
    else if (bytes(d.goal.trim()) > GOAL_MAX_BYTES) e.goal = 'The goal is too long: keep it under 50,000 characters.'
    if ([...d.title].length > TITLE_MAX) e.title = 'A name is up to 120 characters.'
    if (c.uploading) e.files = 'Wait for the files to finish uploading.'
  }
  if (id === 'assistant' && (!c.model || !c.modelKnown)) e.model = 'Choose an assistant.'
  if (id === 'when' && d.when !== 'now') {
    if (d.when === 'hourly') {
      if (!Number.isInteger(d.minute) || d.minute < 0 || d.minute > 59) e.minute = 'The minute is 0 to 59.'
    } else if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(d.time)) e.time = 'Pick a time of day.'
    if (!d.timezone) e.timezone = 'Pick a time zone.'
  }
  return e
}

export const isComplete = (id: StepId, d: Draft, c: Context) => Object.keys(validateStep(id, d, c)).length === 0

/** The first step in `steps` that is not complete, or undefined when all are. */
export function firstIncomplete(steps: StepId[], d: Draft, c: Context): StepId | undefined {
  return steps.find((s) => !isComplete(s, d, c))
}

/** A step can be opened when every step before it is complete. */
export function canOpen(id: StepId, steps: StepId[], d: Draft, c: Context): boolean {
  return steps.slice(0, steps.indexOf(id)).every((s) => isComplete(s, d, c))
}

/** The step a `?step=` value names: unknown or missing means the first, and a step that is out of reach falls back to the first step that is not complete. */
export function stepFromParam(param: string | null, steps: StepId[], d: Draft, c: Context): StepId {
  const wanted = steps.find((s) => s === param) ?? steps[0]
  if (canOpen(wanted, steps, d, c)) return wanted
  return firstIncomplete(steps, d, c) ?? wanted
}
