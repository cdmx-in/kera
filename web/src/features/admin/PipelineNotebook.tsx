// The notebook view of a script: cells (the real script, split at "#%% name" headers), a session toolbar and the problems panel.
// Everything here is presentation; the session, the checks and the cell text live in ScriptTab (PipelineScript.tsx, notebook.ts).
import { useEffect, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { Link } from 'react-router'
import { ArrowDown, ArrowUp, Check, CircleAlert, Clock, Eraser, FastForward, FileCode, Info, Loader2, Play, Plus, RotateCcw, Square, Trash2, TriangleAlert } from 'lucide-react'
import { cx } from '@/lib/format'
import { Badge, Btn, inputCls } from './ui'
import { CodeEditor, type EditorHandle } from './CodeEditor'
import type { NbCell } from './notebook'
import { runnersUrl } from './Pipelines'
import { atBottom, cellBusy, cellStatusLabel, sessionLine, sessionLive, type CellProblems, type CellRun, type CellStatus, type Problem, type Severity } from './pipelines'
import type { Session } from './notebook'

const statusIcon: Record<CellStatus, typeof Check> = { queued: Clock, running: Loader2, ok: Check, failed: CircleAlert, stopped: Square }
const statusTone: Record<CellStatus, 'grey' | 'accent' | 'green' | 'red' | 'amber'> = { queued: 'grey', running: 'accent', ok: 'green', failed: 'red', stopped: 'amber' }

/** Queued, Running, OK, Failed or Stopped: icon and word, never colour alone. */
export function CellStatusChip({ status }: { status: CellStatus }) {
  const Icon = statusIcon[status]
  return (
    <span data-testid="cell-status" data-status={status}>
      <Badge tone={statusTone[status]}>
        <Icon size={11} aria-hidden className={status === 'running' ? 'animate-spin' : ''} /> {cellStatusLabel[status]}
      </Badge>
    </span>
  )
}

const sevIcon = { error: CircleAlert, warning: TriangleAlert, info: Info }
const sevWord: Record<Severity, string> = { error: 'Error', warning: 'Warning', info: 'Note' }
const sevTone = { error: 'text-red-700 dark:text-red-400', warning: 'text-amber-700 dark:text-amber-300', info: 'text-sky-700 dark:text-sky-300' }

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`

/** Output of a cell: a scroll box that keeps following the end until the reader scrolls up. */
function Output({ text, label }: { text: string; label: string }) {
  const box = useRef<HTMLPreElement>(null)
  const follow = useRef(true)
  useEffect(() => {
    const el = box.current
    if (el && follow.current) el.scrollTop = el.scrollHeight
  }, [text])
  return (
    <pre ref={box} tabIndex={0} aria-label={label} onScroll={(e) => (follow.current = atBottom(e.currentTarget))} data-testid="cell-output" className="max-h-64 min-h-8 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-bg-soft p-2.5 font-mono text-xs leading-5">
      {text}
    </pre>
  )
}

const iconBtn = 'px-2 py-1'

export interface CellActions {
  onTitle: (v: string) => void
  onCode: (v: string) => void
  onRun: () => void
  onRunUpTo: () => void
  onStop: () => void
  onDelete: () => void
  onAddBelow: () => void
  onMove: (by: -1 | 1) => void
  onClear: () => void
  /** Shift+Enter: run it and go to the next cell */
  onRunNext: () => void
}

export function CellBlock({
  cell,
  index,
  count,
  startLine,
  step,
  problems,
  run,
  canMoveUp,
  canMoveDown,
  editorRef,
  titleRef,
  actions: a,
}: {
  cell: NbCell
  index: number
  count: number
  startLine: number
  /** the step name the runner will give it; null for the setup cell */
  step: string | null
  problems: CellProblems
  run: CellRun | undefined
  canMoveUp: boolean
  canMoveDown: boolean
  editorRef: (h: EditorHandle | null) => void
  titleRef: (el: HTMLInputElement | null) => void
  actions: CellActions
}) {
  const n = index + 1
  const busy = cellBusy(run?.status)
  const setup = cell.header === null
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== 'Enter') return
    if (e.shiftKey) {
      e.preventDefault()
      a.onRunNext()
    } else if (e.ctrlKey || e.metaKey) {
      e.preventDefault()
      a.onRun()
    }
  }
  const lines = cell.code.split('\n').length
  return (
    <section aria-label={setup ? 'Setup cell' : `Cell ${n}: ${cell.title || step}`} data-testid="cell" data-status={run?.status ?? 'idle'} className="space-y-2 rounded-xl border border-line bg-surface p-3">
      <header className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <span className="grid size-6 shrink-0 place-items-center rounded-md bg-surface-2 text-xs font-medium tabular-nums text-muted">{n}</span>
        {setup ? (
          <span className="text-sm font-medium" title="The text before the first #%% header. It runs first and is not a step.">
            setup
          </span>
        ) : (
          <label className="flex min-w-[10rem] flex-1 items-center gap-1.5">
            <span className="font-mono text-xs text-muted" aria-hidden>
              #%%
            </span>
            <input ref={titleRef} aria-label={`Name of cell ${n}`} value={cell.title} placeholder={step ?? 'name'} autoComplete="off" spellCheck={false} onChange={(e) => a.onTitle(e.target.value)} className={cx(inputCls, 'py-1')} />
          </label>
        )}
        {step && step !== cell.title && (
          <span className="font-mono text-xs text-muted" title="The step name in the run: one word">
            step {step}
          </span>
        )}
        {run && <CellStatusChip status={run.status} />}
        {run && !busy && run.sent !== cell.code && <Badge>Edited since it ran</Badge>}
        {problems.errors > 0 && (
          <span data-testid="cell-errors">
            <Badge tone="red">
              <CircleAlert size={11} aria-hidden /> {plural(problems.errors, 'error')}
            </Badge>
          </span>
        )}
        {problems.warnings > 0 && (
          <span data-testid="cell-warnings">
            <Badge tone="amber">
              <TriangleAlert size={11} aria-hidden /> {plural(problems.warnings, 'warning')}
            </Badge>
          </span>
        )}
        <span className="ml-auto flex flex-wrap items-center gap-0.5">
          {busy ? (
            <Btn variant="ghost" className={iconBtn} aria-label={`Stop cell ${n}`} title="Stop: a Python cell is interrupted and keeps its state; the shell is restarted and its variables are lost" onClick={a.onStop} data-testid="cell-stop">
              <Square size={14} aria-hidden />
            </Btn>
          ) : (
            <Btn variant="ghost" className={iconBtn} aria-label={`Run cell ${n}`} title="Run this cell (Ctrl+Enter)" onClick={a.onRun} data-testid="cell-run">
              <Play size={14} aria-hidden />
            </Btn>
          )}
          <Btn variant="ghost" className={iconBtn} aria-label={`Run up to cell ${n}`} title="Run every cell up to and including this one" onClick={a.onRunUpTo} data-testid="cell-run-upto">
            <FastForward size={14} aria-hidden />
          </Btn>
          <Btn variant="ghost" className={iconBtn} aria-label={`Move cell ${n} up`} title="Move up" disabled={!canMoveUp} onClick={() => a.onMove(-1)}>
            <ArrowUp size={14} aria-hidden />
          </Btn>
          <Btn variant="ghost" className={iconBtn} aria-label={`Move cell ${n} down`} title="Move down" disabled={!canMoveDown} onClick={() => a.onMove(1)}>
            <ArrowDown size={14} aria-hidden />
          </Btn>
          <Btn variant="ghost" className={iconBtn} aria-label={`Add a cell below cell ${n}`} title="Add a cell below" onClick={a.onAddBelow} data-testid="cell-add">
            <Plus size={14} aria-hidden />
          </Btn>
          <Btn variant="ghost" className={cx(iconBtn, 'text-red-600 dark:text-red-400')} aria-label={`Delete cell ${n}`} title="Delete this cell" disabled={count < 2} onClick={a.onDelete}>
            <Trash2 size={14} aria-hidden />
          </Btn>
        </span>
      </header>
      <CodeEditor label={`Code of cell ${n}`} value={cell.code} rows={lines} startLine={startLine} marks={Object.fromEntries(Object.entries(problems.lines).filter(([l]) => Number(l) >= 1))} editorRef={editorRef} onChange={a.onCode} onKeyDown={onKey} />
      {run && (run.output || busy) && (
        <div className="space-y-1" data-testid="cell-result">
          <Output text={run.output || (run.status === 'queued' ? 'Waiting for the runner…' : '')} label={`Output of cell ${n}`} />
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
            {run.exit_code !== null && run.status !== 'ok' && <span>Exit code {run.exit_code}</span>}
            {!busy && run.output && (
              <Btn variant="ghost" className="px-2 py-0.5 text-xs" onClick={a.onClear} aria-label={`Clear the output of cell ${n}`}>
                <Eraser size={12} aria-hidden /> Clear output
              </Btn>
            )}
          </div>
        </div>
      )}
    </section>
  )
}

export const notebookHelp = 'Cells run in a live session on a runner. State carries between cells. Uploads are planned, never done. Stop interrupts a Python cell (state kept) or restarts the shell (variables lost).'
export const realWarning = "Cells can read and change the pipeline's real workspace files. Uploads to collections are still only planned, never done."

/** Session controls, Run all, Check and the Raw file switch. */
export function NotebookToolbar({
  session,
  noRunner,
  starting,
  error,
  real,
  onReal,
  runningAll,
  onStart,
  onClose,
  onRunAll,
  onStopAll,
  checking,
  onCheck,
  raw,
  onRaw,
  extra,
}: {
  session: Session | null
  noRunner: boolean
  starting: boolean
  error: string
  real: boolean
  onReal: (v: boolean) => void
  runningAll: boolean
  onStart: () => void
  onClose: () => void
  onRunAll: () => void
  onStopAll: () => void
  checking: boolean
  onCheck: () => void
  raw: boolean
  onRaw: (v: boolean) => void
  extra?: ReactNode
}) {
  const live = sessionLive(session)
  const sess = session ? { status: session.status, error: session.error } : null
  return (
    <div className="space-y-2.5 rounded-2xl border border-line bg-surface p-3.5" data-testid="notebook-toolbar">
      <div className="flex flex-wrap items-center gap-2">
        <Btn onClick={onStart} disabled={starting} data-testid="session-start">
          {starting ? <Loader2 size={14} className="animate-spin" aria-hidden /> : live ? <RotateCcw size={14} aria-hidden /> : <Play size={14} aria-hidden />} {live ? 'Restart session' : 'Start session'}
        </Btn>
        {live && (
          <Btn variant="ghost" onClick={onClose} data-testid="session-close">
            Close session
          </Btn>
        )}
        {runningAll ? (
          <Btn onClick={onStopAll} data-testid="run-all-stop">
            <Square size={14} aria-hidden /> Stop
          </Btn>
        ) : (
          <Btn variant="primary" onClick={onRunAll} disabled={raw} title={raw ? 'Leave the raw file view to run cells' : 'Run every cell in order; stops at the first one that fails'} data-testid="run-all">
            <Play size={14} aria-hidden /> Run all
          </Btn>
        )}
        <Btn onClick={onCheck} disabled={checking} data-testid="check">
          {checking ? <Loader2 size={14} className="animate-spin" aria-hidden /> : <Check size={14} aria-hidden />} Check
        </Btn>
        <label className="ml-auto inline-flex cursor-pointer items-center gap-2 text-sm">
          <input type="checkbox" checked={raw} onChange={(e) => onRaw(e.target.checked)} className="size-4 accent-[var(--accent)]" />
          <FileCode size={14} aria-hidden /> Raw file
        </label>
      </div>
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs" role="status" data-testid="session-line">
        <span className={cx(noRunner && 'text-amber-700 dark:text-amber-300')}>
          <span className="text-muted">Session: </span>
          {sessionLine(sess, noRunner)}
          {error && <span className="text-red-700 dark:text-red-400"> {error}</span>}
        </span>
        {noRunner && (
          <Link to={runnersUrl} className="underline">
            Open Runners
          </Link>
        )}
        {checking && <span className="text-muted">Checking…</span>}
        {extra}
      </p>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-2">
        <label className={cx('inline-flex items-center gap-2 text-sm', live ? 'opacity-60' : 'cursor-pointer')}>
          <input type="checkbox" checked={real} disabled={live} onChange={(e) => onReal(e.target.checked)} className="size-4 accent-[var(--accent)]" data-testid="real-workspace" />
          Use the real workspace
        </label>
        <p className="flex min-w-[14rem] flex-1 items-start gap-1.5 text-xs text-amber-800 dark:text-amber-300" data-testid="real-warning">
          <TriangleAlert size={13} className="mt-px shrink-0" aria-hidden />
          <span>
            {realWarning}
            {live && ' It can only be changed before a session starts.'}
          </span>
        </p>
      </div>
      <p className="text-xs text-muted">{notebookHelp}</p>
    </div>
  )
}

/** What the last check found, one row per problem; clicking one goes to its cell and line. */
export function ProblemsPanel({ problems, tools, checked, checking, hint, onPick }: { problems: Problem[]; tools: string[]; checked: boolean; checking: boolean; hint: string; onPick: (p: Problem) => void }) {
  const errors = problems.filter((p) => p.severity === 'error').length
  const warnings = problems.filter((p) => p.severity === 'warning').length
  return (
    <section aria-label="Problems" data-testid="problems" className="overflow-hidden rounded-2xl border border-line bg-surface">
      <header className="flex flex-wrap items-center gap-2 border-b border-line bg-surface-2/40 px-5 py-3">
        <h3 className="text-[15px] font-semibold tracking-tight">Problems</h3>
        {checked && <Badge tone={errors ? 'red' : warnings ? 'amber' : 'green'}>{errors || warnings ? [errors && plural(errors, 'error'), warnings && plural(warnings, 'warning')].filter(Boolean).join(', ') : 'None'}</Badge>}
        {checking && <span className="text-xs text-muted">Checking…</span>}
        {tools.length > 0 && <span className="ml-auto text-xs text-muted">Checked with {tools.join(', ')}</span>}
      </header>
      {hint === 'no_runner' ? (
        <p className="px-5 py-3 text-sm text-muted" data-testid="check-hint">
          Checks need a runner and none is online.{' '}
          <Link to={runnersUrl} className="underline">
            Open Runners
          </Link>
        </p>
      ) : hint ? (
        <p className="px-5 py-3 text-sm text-muted" data-testid="check-hint">
          The check did not finish: {hint}
        </p>
      ) : !checked ? (
        <p className="px-5 py-3 text-sm text-muted">{checking ? 'Checking…' : 'Not checked yet. Press Check, or stop typing for a moment.'}</p>
      ) : problems.length === 0 ? (
        <p className="px-5 py-3 text-sm text-muted">No problems found.</p>
      ) : (
        <ul className="max-h-72 divide-y divide-line overflow-y-auto">
          {problems.map((p, i) => {
            const Icon = sevIcon[p.severity]
            return (
              <li key={i}>
                <button type="button" onClick={() => onPick(p)} data-testid="problem" data-severity={p.severity} className="flex w-full flex-wrap items-baseline gap-x-2 gap-y-0.5 px-5 py-2 text-left text-sm hover:bg-surface-2/60">
                  <span className={cx('inline-flex items-center gap-1 text-xs font-medium', sevTone[p.severity])}>
                    <Icon size={13} aria-hidden /> {sevWord[p.severity]}
                  </span>
                  <span className="font-mono text-xs text-muted">
                    {p.file}:{p.line}
                    {p.col > 0 && `:${p.col}`}
                  </span>
                  <span className="min-w-0 flex-1 break-words">{p.message}</span>
                  <span className="text-xs text-muted">
                    {p.tool}
                    {p.code && ` ${p.code}`}
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
