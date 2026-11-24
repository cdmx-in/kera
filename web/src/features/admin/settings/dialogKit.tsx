// Shared editor-dialog plumbing for connections, tool servers, hook servers
// and OAuth providers: inline field errors (client rules from validate.ts plus
// the server's {field} errors), Save disabled until valid, Enter submits, and
// a Test button that sends the unsaved draft to the matching /test endpoint.
import { useState, type ReactNode } from 'react'
import { Activity, CheckCircle2, CircleAlert, Loader2, XCircle } from 'lucide-react'
import { api, ApiError } from '@/api/client'
import { cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Btn, managedWarning } from '../ui'
import type { Errors } from './validate'

/** Red border for an input with an error. */
export const bad = (error?: string) => (error ? ' border-red-500! focus:border-red-500!' : '')

/** Field with an inline error under the control (replaces the hint while shown). */
export function VField({ label, hint, error, children, className }: { label: ReactNode; hint?: ReactNode; error?: string; children: ReactNode; className?: string }) {
  return (
    <label className={cx('block space-y-1', className)}>
      <span className="block text-xs font-medium text-muted">{label}</span>
      {children}
      {error ? (
        <span role="alert" className="block text-xs text-red-600 dark:text-red-400">
          {error}
        </span>
      ) : (
        hint && <span className="block text-xs text-muted">{hint}</span>
      )}
    </label>
  )
}

/** Which errors show: a field's once it was edited or left, all after a submit attempt; server errors always. */
export function useFormErrors(errors: Errors, showAll = false) {
  const [touched, setTouched] = useState<Record<string, boolean>>({})
  const [all, setAll] = useState(showAll)
  const [server, setServer] = useState<Errors>({})
  const keys = Object.keys(errors)
  return {
    errors,
    valid: keys.length === 0,
    /** some error is not shown yet */
    hidden: !all && keys.some((k) => !touched[k]),
    err: (f: string): string | undefined => server[f] ?? (all || touched[f] ? errors[f] : undefined),
    touch: (f: string) => {
      if (!f) return
      setTouched((t) => (t[f] ? t : { ...t, [f]: true }))
      setServer((s) => {
        if (!(f in s)) return s
        const next = { ...s }
        delete next[f]
        return next
      })
    },
    revealAll: () => setAll(true),
    setServer,
  }
}
export type FormErrors = ReturnType<typeof useFormErrors>

/** What the /test endpoints answer (always HTTP 200; ok says whether it passed). */
export interface ProbeResult {
  ok: boolean
  ms: number
  error?: string
  note?: string
  via?: string
  status?: number
  models?: string[]
  tools?: string[]
  checks?: { name: string; ok: boolean; detail?: string }[]
}

export type TestState = { state: 'running' } | { state: 'done'; r: ProbeResult } | null

/** Runs a draft test; a 400 naming a field lands inline on that field. */
export function useDraftTest(path: string, form: FormErrors) {
  const [state, setState] = useState<TestState>(null)
  const run = async (body: unknown) => {
    setState({ state: 'running' })
    try {
      setState({ state: 'done', r: await api<ProbeResult>(path, { method: 'POST', json: body }) })
    } catch (e) {
      if (e instanceof ApiError && e.field) {
        form.setServer({ [e.field]: e.message })
        setState(null)
      } else setState({ state: 'done', r: { ok: false, ms: 0, error: (e as Error).message } })
    }
  }
  return { state, run, clear: () => setState((s) => (s?.state === 'done' ? null : s)) }
}

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

/** One-line verdict for a test result. */
export function testSummary(r: ProbeResult): string {
  if (!r.ok) return r.checks?.length ? `${r.checks.filter((c) => c.ok).length} of ${plural(r.checks.length, 'check')} passed` : r.error || 'Failed'
  const parts = [r.models ? `Connected · ${plural(r.models.length, 'model')}` : r.tools ? plural(r.tools.length, 'tool') : r.checks ? `All ${plural(r.checks.length, 'check')} passed` : r.status ? `HTTP ${r.status}${r.via ? ` via ${r.via}` : ''}` : 'OK']
  if (r.note) parts[0] = r.note
  return [...parts, `${r.ms} ms`].join(' · ')
}

export function TestPanel({ r }: { r: ProbeResult }) {
  const names = r.models ?? r.tools ?? []
  const tone = !r.ok ? 'red' : r.note ? 'amber' : 'green'
  const box = {
    red: 'border-red-300 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300',
    amber: 'border-amber-300 bg-amber-50 text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300',
    green: 'border-green-300 bg-green-50 text-green-800 dark:border-green-900 dark:bg-green-950 dark:text-green-300',
  }[tone]
  const Icon = tone === 'red' ? XCircle : tone === 'amber' ? CircleAlert : CheckCircle2
  return (
    <div role="status" data-testid="test-result" data-ok={r.ok} className={cx('space-y-2 rounded-lg border p-3 text-sm', box)}>
      <div className="flex items-start gap-2 font-medium">
        <Icon size={16} className="mt-0.5 shrink-0" aria-hidden />
        <span className="min-w-0 break-words">{testSummary(r)}</span>
      </div>
      {!r.ok && r.checks?.length && r.error ? <p className="text-xs break-words">{r.error}</p> : null}
      {r.checks && (
        <ul className="space-y-1 text-xs">
          {r.checks.map((c) => (
            <li key={c.name} className="flex items-start gap-1.5">
              {c.ok ? <CheckCircle2 size={13} className="mt-px shrink-0 text-green-600" aria-label="passed" /> : <XCircle size={13} className="mt-px shrink-0 text-red-600" aria-label="failed" />}
              <span className="min-w-0 break-words">
                {c.name}
                {c.detail && <span className="opacity-75"> — {c.detail}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
      {names.length > 0 && (
        <div className="flex max-h-28 flex-wrap gap-1 overflow-y-auto">
          {names.slice(0, 60).map((n) => (
            <code key={n} className="rounded bg-white/60 px-1.5 py-0.5 text-xs dark:bg-black/30">
              {n}
            </code>
          ))}
          {names.length > 60 && <span className="text-xs">+{names.length - 60} more</span>}
        </div>
      )}
    </div>
  )
}

/**
 * Dialog + form + footer for the editors. Save stays disabled while a rule
 * fails; Enter (or Test) on an invalid form reveals every error. `fields`
 * lists the names rendered inline; a server error on any other field is a toast.
 */
export function EditorDialog(props: {
  title: string
  managed?: boolean
  wide?: boolean
  /** a full width class, for a two-pane form */
  width?: string
  saveLabel?: string
  form: FormErrors
  fields: string[]
  onClose: () => void
  /** throws ApiError on failure */
  onSubmit: () => Promise<void>
  test?: ReturnType<typeof useDraftTest>
  onTest?: () => void
  children: ReactNode
}) {
  const { form, test } = props
  const [saving, setSaving] = useState(false)
  const submit = async () => {
    if (!form.valid) return form.revealAll()
    setSaving(true)
    try {
      await props.onSubmit()
    } catch (e) {
      if (e instanceof ApiError && e.field && props.fields.includes(e.field)) form.setServer({ [e.field]: e.message })
      else toast((e as Error).message, 'error')
    } finally {
      setSaving(false)
    }
  }
  const runTest = () => {
    // the id does not matter for a test; anything else must be valid first
    if (Object.keys(form.errors).some((k) => k !== 'id')) return form.revealAll()
    props.onTest?.()
  }
  const running = test?.state?.state === 'running'
  const nameOf = (t: EventTarget) => (t as HTMLInputElement).name ?? ''
  return (
    <Dialog open onClose={props.onClose} label={props.title} className={props.width ?? (props.wide ? 'w-[min(48rem,calc(100vw-2rem))]' : 'w-[min(40rem,calc(100vw-2rem))]')}>
      <DialogHeader title={props.title} onClose={props.onClose} />
      <form
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
        onChange={(e) => {
          form.touch(nameOf(e.target))
          test?.clear()
        }}
        onBlur={(e) => form.touch(nameOf(e.target))}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !form.valid && e.target instanceof HTMLInputElement) form.revealAll()
        }}
        className="max-h-[calc(100dvh-8rem)] space-y-3 overflow-y-auto p-4"
      >
        {props.managed && <p className="text-xs text-amber-700 dark:text-amber-400">{managedWarning}</p>}
        {props.children}
        {test?.state?.state === 'done' && <TestPanel r={test.state.r} />}
        <div className="sticky -bottom-4 z-10 -mx-4 -mb-4 flex flex-wrap items-center justify-end gap-2 border-t border-line bg-surface px-4 py-3">
          {props.onTest && (
            <Btn onClick={runTest} disabled={running} className="mr-auto" data-testid="test-draft" title="Try the unsaved settings">
              {running ? <Loader2 size={14} className="animate-spin" /> : <Activity size={14} />} Test
            </Btn>
          )}
          {!form.valid && form.hidden && <span className="text-xs text-muted">Fill in the required fields</span>}
          <Btn onClick={props.onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary" disabled={!form.valid || saving} data-testid="save-server">
            {saving && <Loader2 size={14} className="animate-spin" />} {props.saveLabel ?? 'Save'}
          </Btn>
        </div>
      </form>
    </Dialog>
  )
}
