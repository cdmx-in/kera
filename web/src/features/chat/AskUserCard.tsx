import { useState } from 'react'
import type { PendingCall } from '@/api/types'
import { cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { answerCall } from './stream'

type Ask = Extract<PendingCall, { kind: 'ask_user' }>

/** Questions from the model, shown above the composer until answered. */
export function AskUserCard({ messageId, call }: { messageId: string; call: Ask }) {
  const [page, setPage] = useState(0)
  const [picked, setPicked] = useState<string[][]>(() => call.questions.map(() => []))
  const [other, setOther] = useState<string[]>(() => call.questions.map(() => ''))
  const [busy, setBusy] = useState(false)
  const q = call.questions[page]
  if (!q) return null
  const last = page === call.questions.length - 1
  const answered = (i: number) => picked[i].length > 0 || other[i].trim() !== ''

  const toggle = (label: string) =>
    setPicked((p) => p.map((sel, i) => (i !== page ? sel : q.multiSelect ? (sel.includes(label) ? sel.filter((s) => s !== label) : [...sel, label]) : [label])))

  const submit = async () => {
    setBusy(true)
    const value = call.questions.map((qq, i) => ({ question: qq.question, answers: [...picked[i], ...(other[i].trim() ? [other[i].trim()] : [])] }))
    try {
      await answerCall(messageId, call.callId, value)
    } catch (e) {
      toast(`Could not send the answer: ${(e as Error).message}`, 'error')
      setBusy(false)
    }
  }

  return (
    <section data-testid="ask-user-card" aria-label="Question from the assistant" className="mb-2 rounded-2xl border border-accent/40 bg-surface p-4 shadow-pop">
      {call.questions.length > 1 && (
        <p className="mb-1 text-xs text-muted">
          Question {page + 1} of {call.questions.length}
        </p>
      )}
      {q.header && <p className="text-xs font-semibold uppercase tracking-wide text-accent">{q.header}</p>}
      <h3 className="mb-3 font-medium">{q.question}</h3>
      <div className="grid gap-2 sm:grid-cols-2">
        {q.options.map((o) => {
          const on = picked[page].includes(o.label)
          return (
            <button
              key={o.label}
              type="button"
              data-testid="ask-option"
              aria-pressed={on}
              onClick={() => toggle(o.label)}
              className={cx('rounded-xl border px-3 py-2 text-left text-sm transition-colors', on ? 'border-accent bg-accent-soft' : 'border-line hover:bg-surface-2')}
            >
              <span className="flex items-center gap-2 font-medium">
                {o.label}
                {o.recommended && <span className="rounded-full bg-accent px-1.5 py-0.5 text-[10px] font-semibold text-white">Recommended</span>}
              </span>
              {o.description && <span className="mt-0.5 block text-xs text-muted">{o.description}</span>}
            </button>
          )
        })}
      </div>
      {q.allowOther !== false && (
        <input
          value={other[page]}
          onChange={(e) => setOther((p) => p.map((v, i) => (i === page ? e.target.value : v)))}
          placeholder="Other…"
          aria-label="Other answer"
          className="mt-2 w-full rounded-xl border border-line bg-transparent px-3 py-2 text-sm outline-none focus:border-accent"
        />
      )}
      <div className="mt-3 flex justify-end gap-2">
        {page > 0 && (
          <button type="button" onClick={() => setPage(page - 1)} className="rounded-lg px-3 py-1.5 text-sm hover:bg-surface-2">
            Back
          </button>
        )}
        {last ? (
          <button type="button" data-testid="ask-submit" disabled={busy || !call.questions.every((_, i) => answered(i))} onClick={submit} className="rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50">
            Submit
          </button>
        ) : (
          <button type="button" disabled={!answered(page)} onClick={() => setPage(page + 1)} className="rounded-lg bg-accent px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50">
            Next
          </button>
        )}
      </div>
    </section>
  )
}
