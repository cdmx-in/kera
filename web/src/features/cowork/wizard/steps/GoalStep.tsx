// Step 1: what the assistant should do, in a roomy box with files; ideas to start from and the name beside it.
import { useRef, useState, type Dispatch } from 'react'
import { FileUp, Loader2, Sparkles, X } from 'lucide-react'
import type { FileRef } from '@/api/types'
import { cx, formatBytes } from '@/lib/format'
import { usePrompts } from '@/features/workspace/api'
import { MicButton } from '@/features/chat/composer/MicButton'
import { filterPrompts, slashQuery } from '@/features/chat/composer/slash'
import type { useUploads } from '@/features/chat/composer/uploads'
import { ExtBadge } from '@/features/chat/messages/FileChip'
import { input } from '@/features/workspace/ui'
import type { Action, Draft } from '../draft'
import { GOAL_MAX_BYTES, TITLE_MAX, type Errors } from '../steps'
import { Block, RemovableChip, StepHeader } from '../ui'

/** Ideas to start from. They say what to do, not when: the schedule is its own step. */
export const EXAMPLES = [
  'Summarise the new support tickets and flag anything urgent',
  'Check the shared tracking sheet for rows with blank cells and list them',
  'Draft this week’s status report from my recent notes',
  'Research three competitors and write a one-page comparison',
]

/** Ideas stay on offer while the box is empty or still holds one of them, so one can be swapped for another. */
export const showExamples = (goal: string) => goal.trim() === '' || EXAMPLES.includes(goal)

export function GoalStep({ draft, dispatch, errors, uploads, edit }: { draft: Draft; dispatch: Dispatch<Action>; errors: Errors; uploads: ReturnType<typeof useUploads>; edit: boolean }) {
  const ta = useRef<HTMLTextAreaElement>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const latest = useRef(draft.goal) // dictation results arrive between renders
  latest.current = draft.goal
  const [dragging, setDragging] = useState(false)
  const [slashClosed, setSlashClosed] = useState(false)
  const [active, setActive] = useState(0)
  const set = (goal: string) => {
    latest.current = goal
    dispatch({ type: 'set', patch: { goal } })
    setSlashClosed(false)
  }

  // "/command" alone in the box lists saved prompts, as in the chat box; picking one replaces the text.
  const slash = slashQuery(draft.goal)
  const { data: prompts } = usePrompts(slash !== null)
  const matches = slash !== null && !slashClosed ? filterPrompts(prompts ?? [], slash) : []
  const open = matches.length > 0
  const sel = Math.min(active, matches.length - 1)

  const bytes = new TextEncoder().encode(draft.goal.trim()).length
  const files = uploads.items.length + draft.kept.length

  return (
    <div className="space-y-6">
      <StepHeader title="What should the assistant do?">Describe the goal the way you would brief a colleague. The assistant plans the work, uses the apps you pick next, and reports back. You choose when it runs on a later step.</StepHeader>

      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]" data-testid="goal-columns">
        <div className="min-w-0 space-y-6">
          <Block title="Goal">
            <div
              className={cx('rounded-2xl border bg-surface transition-colors focus-within:border-accent', errors.goal ? 'border-red-500' : dragging ? 'border-accent' : 'border-line')}
              onDragOver={(e) => {
                if (e.dataTransfer.types.includes('Files')) {
                  e.preventDefault()
                  setDragging(true)
                }
              }}
              onDragLeave={() => setDragging(false)}
              onDrop={(e) => {
                setDragging(false)
                if (e.dataTransfer.files.length) {
                  e.preventDefault()
                  uploads.add([...e.dataTransfer.files])
                }
              }}
            >
              <textarea
                ref={ta}
                id="wizard-goal"
                data-testid="task-goal"
                aria-label="Goal"
                aria-invalid={!!errors.goal}
                aria-describedby={errors.goal ? 'wizard-goal-error' : undefined}
                aria-autocomplete="list"
                aria-controls={open ? 'wizard-slash-list' : undefined}
                aria-activedescendant={open ? `wizard-slash-${matches[sel].id}` : undefined}
                value={draft.goal}
                rows={7}
                placeholder="For example: every morning, read the new support tickets, group them by customer and list what needs a reply today."
                onChange={(e) => set(e.target.value)}
                onPaste={(e) => {
                  const pasted = [...e.clipboardData.files]
                  if (pasted.length) {
                    e.preventDefault()
                    uploads.add(pasted)
                  }
                }}
                onKeyDown={(e) => {
                  if (!open || e.nativeEvent.isComposing || e.shiftKey) return
                  const n = matches.length
                  const keys: Record<string, () => void> = {
                    ArrowDown: () => setActive((sel + 1) % n),
                    ArrowUp: () => setActive((sel - 1 + n) % n),
                    Enter: () => (set(matches[sel].content), setActive(0)),
                    Tab: () => (set(matches[sel].content), setActive(0)),
                    Escape: () => setSlashClosed(true),
                  }
                  if (keys[e.key]) {
                    e.preventDefault()
                    keys[e.key]()
                  }
                }}
                className="block min-h-44 w-full resize-y xl:min-h-72 rounded-t-2xl bg-transparent px-4 py-3 text-[15px] leading-relaxed outline-none placeholder:text-muted focus-visible:outline-none"
              />
              {open && (
                <ul id="wizard-slash-list" role="listbox" aria-label="Saved prompts" data-testid="slash-menu" className="mx-2 mb-1 max-h-56 overflow-auto rounded-xl border border-line bg-surface p-1">
                  {matches.map((p, i) => (
                    <li
                      key={p.id}
                      id={`wizard-slash-${p.id}`}
                      role="option"
                      aria-selected={i === sel}
                      data-testid="slash-item"
                      onMouseDown={(e) => (e.preventDefault(), set(p.content))}
                      onMouseEnter={() => setActive(i)}
                      className={cx('flex cursor-pointer items-baseline gap-2 rounded-lg px-3 py-1.5 text-sm', i === sel && 'bg-surface-2')}
                    >
                      <span className="font-mono text-accent">/{p.command}</span>
                      <span className="truncate text-muted">{p.title}</span>
                    </li>
                  ))}
                </ul>
              )}
              <div className="flex items-center gap-1.5 border-t border-line px-2 py-1.5">
                <button type="button" data-testid="upload-files" onClick={() => fileInput.current?.click()} className="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-sm text-muted hover:bg-surface-2 hover:text-fg">
                  <FileUp size={15} aria-hidden /> Attach files
                </button>
                <input ref={fileInput} type="file" multiple hidden onChange={(e) => (uploads.add([...(e.target.files ?? [])]), (e.target.value = ''))} />
                <span className="flex-1" />
                <span className={cx('text-xs tabular-nums', bytes > GOAL_MAX_BYTES ? 'text-red-600 dark:text-red-400' : 'text-muted')} aria-hidden={bytes < GOAL_MAX_BYTES * 0.8}>
                  {bytes > GOAL_MAX_BYTES * 0.8 ? `${bytes.toLocaleString()} / ${GOAL_MAX_BYTES.toLocaleString()}` : ''}
                </span>
                <MicButton onText={(t) => set(latest.current ? `${latest.current.trimEnd()} ${t}` : t)} />
              </div>
            </div>
            {errors.goal && (
              <p id="wizard-goal-error" role="alert" data-testid="field-error" className="text-xs font-medium text-red-600 dark:text-red-400">
                {errors.goal}
              </p>
            )}
          </Block>

          {files > 0 && (
            <Block title="Files" hint="The assistant can read these while it works.">
              <ul className="flex flex-wrap gap-2" aria-label="Attachments">
                {draft.kept.map((f: FileRef) => (
                  <RemovableChip key={f.id} testId="kept-file" id={f.id} label={`Remove ${f.name}`} onRemove={() => dispatch({ type: 'removeKept', id: f.id })}>
                    {f.name}
                  </RemovableChip>
                ))}
                {uploads.items.map((a) => (
                  <li key={a.key} data-testid="attachment" data-status={a.status} data-name={a.name} title={a.error} className={cx('flex max-w-64 items-center gap-2 rounded-xl border p-1.5 pr-1', a.status === 'failed' ? 'border-red-500/50' : 'border-line')}>
                    {a.preview ? <img src={a.preview} alt="" className="size-8 rounded-md object-cover" /> : <ExtBadge name={a.name} />}
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm">{a.name}</span>
                      <span className={cx('block text-xs', a.status === 'failed' ? 'text-red-600 dark:text-red-400' : 'text-muted')}>
                        {a.status === 'uploading' ? 'Uploading…' : a.status === 'processing' ? 'Processing…' : a.status === 'failed' ? (a.error ?? 'Failed') : formatBytes(a.size)}
                      </span>
                    </span>
                    {(a.status === 'uploading' || a.status === 'processing') && <Loader2 size={14} className="animate-spin text-muted" aria-hidden />}
                    <button type="button" aria-label={`Remove ${a.name}`} onClick={() => uploads.remove(a.key)} className="grid size-6 place-items-center rounded-md text-muted hover:bg-surface-2">
                      <X size={13} aria-hidden />
                    </button>
                  </li>
                ))}
              </ul>
              {errors.files && (
                <p role="alert" data-testid="field-error" className="text-xs font-medium text-red-600 dark:text-red-400">
                  {errors.files}
                </p>
              )}
            </Block>
          )}

          {edit && (
            <details className="rounded-xl border border-line bg-surface px-4 py-3">
              <summary className="cursor-pointer text-sm font-semibold">Notes for the next run</summary>
              <textarea className={cx(input, 'mt-3 min-h-24')} value={draft.notes} onChange={(e) => dispatch({ type: 'set', patch: { notes: e.target.value } })} maxLength={4000} aria-label="Notes for the next run" data-testid="schedule-notes" />
              <p className="mt-1.5 text-xs text-muted">The assistant writes these itself at the end of each run, so the next one knows where it left off. Change the assistant and they are cleared. Saved with the goal.</p>
            </details>
          )}
        </div>
        {/* ideas and the optional name sit beside the box on a wide screen, under it otherwise */}
        <div className="min-w-0 space-y-6 xl:pt-7">
          {showExamples(draft.goal) && (
            <div>
              <p className="flex items-center gap-1.5 pb-1.5 text-xs font-medium text-muted">
                <Sparkles size={13} aria-hidden /> Need an idea? Start from an example
              </p>
              <ul className="flex flex-wrap gap-2 xl:flex-col" data-testid="examples">
                {EXAMPLES.map((ex) => (
                  <li key={ex} className="max-w-full">
                    <button
                      type="button"
                      data-testid="example"
                      onClick={() => {
                        set(ex)
                        ta.current?.focus()
                      }}
                      className={cx('max-w-full rounded-2xl border px-3 py-1.5 text-left text-sm transition-colors hover:border-accent/60 hover:bg-accent-soft motion-reduce:transition-none xl:w-full xl:rounded-xl xl:py-2', draft.goal === ex ? 'border-accent bg-accent-soft' : 'border-line bg-surface')}
                    >
                      {ex}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}

          <Block title="Name" hint="Optional. It heads the task in your list. Left empty, it uses the start of the goal.">
            <input className={input} value={draft.title} onChange={(e) => dispatch({ type: 'set', patch: { title: e.target.value } })} maxLength={TITLE_MAX} aria-label="Name" aria-invalid={!!errors.title} placeholder="Defaults to the start of the goal" data-testid="schedule-title" />
            {errors.title && (
              <p role="alert" data-testid="field-error" className="text-xs font-medium text-red-600 dark:text-red-400">
                {errors.title}
              </p>
            )}
          </Block>
        </div>
      </div>
    </div>
  )
}
