// Run trace shapes and the small calculations the Traces page needs (GET /api/v1/admin/runs...).
import { compact, num } from './lib'

export interface TraceRun {
  run_id: string
  message_id: string
  user_id: string
  user_name: string
  user_email: string
  model_id: string
  started_at: string
  duration_ms: number
  rounds: number
  tool_calls: number
  tool_errors: number
  prompt_tokens: number
  cached_tokens: number
  completion_tokens: number
  failed: boolean
}

export type StepKind = 'model' | 'tool' | 'retrieval' | 'compaction'

export interface TraceStep {
  seq: number
  round: number
  kind: StepKind
  name: string
  started_at: string
  duration_ms: number
  prompt_tokens: number | null
  cached_tokens: number | null
  completion_tokens: number | null
  finish: string
  error: string
  detail: Record<string, unknown>
}

export interface TraceDetail {
  run_id: string
  message_id: string
  user_name: string
  user_email: string
  model_id: string
  started_at: string
  duration_ms: number
  steps: TraceStep[]
}

export type Tone = 'grey' | 'accent' | 'red' | 'amber' | 'green'

/** "64%" of the prompt served from the provider's cache, or "-" when no server said. */
export function cachedShare(prompt: number, cached: number): string {
  return prompt > 0 && cached > 0 ? `${Math.round((cached / prompt) * 100)}%` : '-'
}

/** "850 ms", "4.2 s", "1 min 5 s". */
export function ms(v: number): string {
  if (v < 1000) return `${Math.round(v)} ms`
  if (v < 60000) return `${(v / 1000).toFixed(v < 10000 ? 1 : 0)} s`
  const s = Math.round(v / 1000)
  return `${Math.floor(s / 60)} min ${s % 60} s`
}

const n = (v: unknown): number => (typeof v === 'number' ? v : 0)

/** The facts on a step's row: sizes, counts and flags worth seeing without opening it. */
export function stepChips(s: TraceStep): { text: string; tone: Tone }[] {
  const d = s.detail ?? {}
  const out: { text: string; tone: Tone }[] = []
  const add = (text: string, tone: Tone = 'grey') => out.push({ text, tone })
  if (s.kind === 'model') {
    if (s.prompt_tokens !== null) add(`${compact(s.prompt_tokens)} in`)
    if (s.cached_tokens !== null && s.prompt_tokens) add(`${cachedShare(s.prompt_tokens, s.cached_tokens)} cached`, 'green')
    if (s.completion_tokens !== null) add(`${compact(s.completion_tokens)} out`)
    if (n(d.prompt_chars)) add(`${compact(n(d.prompt_chars))} chars sent`)
    if (n(d.tools_offered)) add(`${n(d.tools_offered)} tools offered (${compact(n(d.tool_def_chars))} chars)`)
    if (n(d.tool_calls)) add(`${n(d.tool_calls)} tool ${n(d.tool_calls) === 1 ? 'call' : 'calls'}`, 'accent')
    if (d.truncated === true) add('cut off at the length limit', 'amber')
  } else if (s.kind === 'tool') {
    add(`${num(n(d.result_chars))} chars back`)
    if (d.truncated === true) add('result truncated', 'amber')
    if (d.is_error === true) add('error', 'red')
    if (d.known === false) add('unknown tool', 'red')
  } else if (s.kind === 'retrieval') {
    add(`${n(d.hits)} ${n(d.hits) === 1 ? 'passage' : 'passages'}`, n(d.hits) === 0 ? 'amber' : 'grey')
    if (n(d.full_docs)) add(`${n(d.full_docs)} whole documents`)
    if (d.top_score !== undefined) add(`top score ${n(d.top_score)}`)
    if (d.reranked === true) add('reranked')
  } else {
    add(`${compact(n(d.tokens_before))} to ${compact(n(d.tokens_after))} tokens`)
  }
  if (s.error) add(s.error.replace(/_/g, ' '), 'red')
  if (d.http_status) add(`HTTP ${n(d.http_status)}`, 'red')
  return out
}

/** Where a step's bar sits on the run's axis, as percentages (a step of 0 ms still gets a sliver). */
export function bar(s: Pick<TraceStep, 'started_at' | 'duration_ms'>, t0: number, span: number): { left: number; width: number } {
  const left = Math.min(99.6, Math.max(0, ((new Date(s.started_at).getTime() - t0) / span) * 100))
  return { left, width: Math.min(100 - left, Math.max(0.4, (s.duration_ms / span) * 100)) }
}
