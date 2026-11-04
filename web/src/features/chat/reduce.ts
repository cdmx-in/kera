// Pure reducer: applies one stream event to an assistant message. History
// loaded from the server has the same shape, so it renders through the same
// components as a live stream.

import type { FileRef, Message, Part, PendingCall, Source, StreamEvent, ToolStep } from '@/api/types'

type Steps = Extract<Part, { type: 'steps' }>
type Reasoning = Extract<Part, { type: 'reasoning' }>

const str = (v: unknown) => (typeof v === 'string' ? v : v == null ? '' : String(v))

export function toFileRef(f: Record<string, unknown>): FileRef {
  const meta = (f.meta ?? {}) as Record<string, unknown>
  const contentType = str(f.contentType ?? f.content_type ?? meta.content_type)
  return {
    id: str(f.id ?? f.file_id),
    name: str(f.name ?? f.filename ?? meta.name) || 'file',
    size: typeof f.size === 'number' ? f.size : typeof meta.size === 'number' ? meta.size : undefined,
    contentType: contentType || undefined,
    type: f.type === 'image' || contentType.startsWith('image/') ? 'image' : f.type === 'collection' ? 'collection' : 'file',
  }
}

function mergeBy<T>(a: T[], b: T[], key: (x: T) => string): T[] {
  const seen = new Set(a.map(key))
  return [...a, ...b.filter((x) => !seen.has(key(x)) && (seen.add(key(x)), true))]
}

function closeReasoning(parts: Part[], now: number): Part[] {
  const last = parts[parts.length - 1]
  if (last?.type !== 'reasoning' || last.done) return parts
  const r: Reasoning = { ...last, done: true, durationMs: last.durationMs ?? (last.startedAt ? now - last.startedAt : undefined) }
  return [...parts.slice(0, -1), r]
}

function mapStep(parts: Part[], callId: string, fn: (s: ToolStep) => ToolStep): Part[] {
  return parts.map((p) => (p.type === 'steps' && p.steps.some((s) => s.callId === callId) ? { ...p, steps: p.steps.map((s) => (s.callId === callId ? fn(s) : s)) } : p))
}

function runningStepId(parts: Part[]): string | undefined {
  const last = parts[parts.length - 1]
  if (last?.type !== 'steps') return undefined
  return [...last.steps].reverse().find((s) => s.status === 'running')?.callId
}

export function applyEvent(m: Message, ev: StreamEvent, now = Date.now()): Message {
  const next: Message = { ...m, seq: typeof ev.seq === 'number' ? ev.seq : m.seq }
  let parts = m.parts
  switch (ev.type) {
    case 'delta': {
      const text = str(ev.text ?? ev.content ?? ev.delta)
      if (!text) break
      parts = closeReasoning(parts, now)
      const last = parts[parts.length - 1]
      parts = last?.type === 'text' ? [...parts.slice(0, -1), { type: 'text', text: last.text + text }] : [...parts, { type: 'text', text }]
      next.statusLine = undefined
      break
    }
    case 'reasoning': {
      const text = str(ev.text ?? ev.content ?? ev.delta)
      const last = parts[parts.length - 1]
      if (last?.type === 'reasoning' && !last.done) {
        parts = [...parts.slice(0, -1), { ...last, text: last.text + text }]
      } else if (text) {
        parts = [...parts, { type: 'reasoning', text, done: false, startedAt: now }]
      }
      if (ev.done) {
        const l = parts[parts.length - 1]
        if (l?.type === 'reasoning') parts = [...parts.slice(0, -1), { ...l, done: true, durationMs: typeof ev.durationMs === 'number' ? ev.durationMs : l.startedAt ? now - l.startedAt : undefined }]
      }
      break
    }
    case 'tool_call': {
      parts = closeReasoning(parts, now)
      const step: ToolStep = { callId: str(ev.callId), name: str(ev.name), server: ev.server ? str(ev.server) : undefined, args: ev.args, status: 'running', statusLines: [] }
      const last = parts[parts.length - 1]
      parts = last?.type === 'steps' ? [...parts.slice(0, -1), { type: 'steps', steps: [...last.steps, step] } as Steps] : [...parts, { type: 'steps', steps: [step] }]
      break
    }
    case 'tool_result': {
      const files = Array.isArray(ev.files) ? (ev.files as Record<string, unknown>[]).map(toFileRef) : undefined
      parts = mapStep(parts, str(ev.callId), (s) => ({
        ...s,
        status: ev.status === 'error' ? 'error' : 'done',
        output: ev.preview != null ? str(ev.preview) : ev.output != null ? str(ev.output) : s.output,
        files: files ?? s.files,
        statusLines: s.statusLines.map((l) => ({ ...l, done: true })),
      }))
      break
    }
    case 'status': {
      const line = { id: ev.id ? str(ev.id) : undefined, action: ev.action ? str(ev.action) : undefined, description: str(ev.description), done: !!ev.done }
      const callId = ev.callId ? str(ev.callId) : runningStepId(parts)
      if (ev.hidden || !line.description) {
        if (ev.done && next.statusLine && next.statusLine.id === line.id) next.statusLine = undefined
        break
      }
      if (callId && ev.action !== 'compaction') {
        const urls = Array.isArray(ev.urls) ? (ev.urls as unknown[]).map(str) : undefined
        parts = mapStep(parts, callId, (s) => {
          // no id: a line finishes the still-open line of the same action ("Searching…" -> "Searched")
          const i = line.id ? s.statusLines.findIndex((l) => l.id === line.id) : line.action ? s.statusLines.findLastIndex((l) => !l.id && !l.done && l.action === line.action) : -1
          const statusLines = i >= 0 ? s.statusLines.map((l, j) => (j === i ? line : l)) : [...s.statusLines, line]
          return { ...s, statusLines, urls: urls ? [...new Set([...(s.urls ?? []), ...urls])] : s.urls }
        })
      } else {
        next.statusLine = line.done ? undefined : line
      }
      break
    }
    case 'sources':
      next.sources = mergeBy(m.sources, (Array.isArray(ev.sources) ? ev.sources : []) as Source[], (s) => s.url ?? s.fileId ?? s.id ?? s.name ?? '')
      break
    case 'files':
      next.files = mergeBy(m.files, (Array.isArray(ev.files) ? ev.files : []).map((f) => toFileRef(f as Record<string, unknown>)), (f) => f.id)
      break
    case 'block':
      if (ev.block && typeof ev.block === 'object') parts = [...closeReasoning(parts, now), { type: 'block', block: ev.block as Extract<Part, { type: 'block' }>['block'] }]
      break
    case 'snapshot':
      if (Array.isArray(ev.parts)) parts = ev.parts as Part[]
      if (Array.isArray(ev.files)) next.files = (ev.files as Record<string, unknown>[]).map(toFileRef)
      if (Array.isArray(ev.sources)) next.sources = ev.sources as Source[]
      if (Array.isArray(ev.pendingCalls)) next.pendingCalls = ev.pendingCalls as PendingCall[]
      if (typeof ev.status === 'string') next.status = ev.status as Message['status']
      break
    case 'ask_user':
      next.pendingCalls = [...m.pendingCalls.filter((c) => c.callId !== ev.callId), { kind: 'ask_user', callId: str(ev.callId), questions: (ev.questions ?? []) as Extract<PendingCall, { kind: 'ask_user' }>['questions'] }]
      break
    case 'confirm':
      next.pendingCalls = [
        ...m.pendingCalls.filter((c) => c.callId !== ev.callId),
        { kind: 'confirm', callId: str(ev.callId), title: ev.title ? str(ev.title) : undefined, message: str(ev.message), confirmLabel: ev.confirmLabel ? str(ev.confirmLabel) : undefined, cancelLabel: ev.cancelLabel ? str(ev.cancelLabel) : undefined },
      ]
      break
    case 'compaction': {
      const state = ev.state === 'done' ? 'done' : 'running'
      const i = parts.findIndex((p) => p.type === 'compaction')
      const part: Part = { type: 'compaction', state, summary: ev.summary ? str(ev.summary) : undefined, before: ev.before as number | undefined, after: ev.after as number | undefined }
      parts = i >= 0 ? parts.map((p, j) => (j === i ? { ...p, ...part } : p)) : [...parts, part]
      break
    }
    case 'follow_ups': {
      const list = ev.follow_ups ?? ev.followUps ?? ev.items
      if (Array.isArray(list)) next.followUps = list.map(str).filter(Boolean)
      break
    }
    case 'usage':
      next.usage = (ev.usage ?? { prompt_tokens: ev.prompt_tokens, completion_tokens: ev.completion_tokens }) as Message['usage']
      break
    case 'error':
      next.status = 'error'
      next.error = { code: ev.code ? str(ev.code) : undefined, message: str(ev.message) || 'Something went wrong' }
      next.statusLine = undefined
      break
    case 'done':
      parts = closeReasoning(parts, now).map((p) => (p.type === 'steps' && p.steps.some((s) => s.status === 'running') ? { ...p, steps: p.steps.map((s) => (s.status === 'running' ? { ...s, status: 'done' as const } : s)) } : p))
      if (next.status === 'streaming') next.status = ev.status === 'stopped' ? 'stopped' : 'done'
      next.statusLine = undefined
      break
  }
  if (parts !== m.parts) next.parts = parts
  return next
}

/** Final markdown text of a message (text parts joined). */
export function messageText(m: Pick<Message, 'parts'>): string {
  return m.parts
    .filter((p): p is Extract<Part, { type: 'text' }> => p.type === 'text')
    .map((p) => p.text)
    .join('')
}
