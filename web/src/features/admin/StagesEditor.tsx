// The stages section of the pipeline edit form: a title and a description for each step the script reports.
// The run view shows these instead of the raw step name; a step without one shows its name in sentence case.
import { useState } from 'react'
import { Plus, ScanSearch, Trash2 } from 'lucide-react'
import { Btn, inputCls } from './ui'
import { newStageNames, sentenceCase, stageLimits, type Stage } from './pipelines'

/**
 * `seen`: the script step names of the pipeline's latest run, for "Detect from the latest run" (undefined while they load).
 * The list is edited in place; the form's Save sends it.
 */
export function StagesEditor({ value, onChange, seen, error }: { value: Stage[]; onChange: (v: Stage[]) => void; seen: string[] | undefined; error?: string }) {
  const [adding, setAdding] = useState('')
  const fresh = newStageNames(seen?.map((name) => ({ name, kera: false })), value)
  const set = (i: number, patch: Partial<Stage>) => onChange(value.map((s, j) => (j === i ? { ...s, ...patch } : s)))
  const add = (names: string[]) => onChange([...value, ...names.filter((n) => !value.some((s) => s.name === n)).map((name) => ({ name, title: '', about: '' }))])
  const typed = adding.trim()
  const taken = value.some((s) => s.name === typed)
  return (
    <div className="space-y-3" data-testid="stages-editor">
      <div className="flex flex-wrap items-center gap-2">
        <Btn onClick={() => add(fresh)} disabled={fresh.length === 0} data-testid="detect-stages" title={seen === undefined ? 'Loading the latest run' : fresh.length ? undefined : 'Every step of the latest run is already listed'}>
          <ScanSearch size={14} aria-hidden /> Detect from the latest run{fresh.length ? ` (${fresh.length})` : ''}
        </Btn>
        <form
          className="ml-auto flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (typed && !taken && typed.length <= stageLimits.name) (add([typed]), setAdding(''))
          }}
        >
          <input value={adding} onChange={(e) => setAdding(e.target.value)} aria-label="Step name to add" placeholder="step-name" maxLength={stageLimits.name} className={`${inputCls} w-40 font-mono text-xs`} />
          <Btn type="submit" disabled={!typed || taken} aria-label="Add this step name">
            <Plus size={14} aria-hidden /> Add
          </Btn>
        </form>
      </div>
      {value.length === 0 ? (
        <p className="rounded-lg border border-dashed border-line p-4 text-sm text-muted" data-testid="no-stages">
          No stages yet. A step shows its name in sentence case until you give it a title and a description here.
        </p>
      ) : (
        <ul className="grid gap-2.5" aria-label="Stages">
          {value.map((s, i) => (
            <li key={s.name} className="grid gap-x-3 gap-y-2 rounded-xl border border-line bg-surface p-3 md:grid-cols-[minmax(0,13rem)_minmax(0,1fr)_minmax(0,1.6fr)_auto]" data-testid="stage-row">
              <div className="min-w-0 self-center">
                <span className="block text-[10.5px] font-bold uppercase tracking-[0.08em] text-muted">Step name</span>
                <code className="block break-all text-[13px]" title="The name the script reports">
                  {s.name}
                </code>
              </div>
              <label className="block min-w-0 space-y-1">
                <span className="block text-xs font-medium text-muted">Title</span>
                <input value={s.title} onChange={(e) => set(i, { title: e.target.value })} placeholder={sentenceCase(s.name)} maxLength={stageLimits.title} className={inputCls} />
              </label>
              <label className="block min-w-0 space-y-1">
                <span className="block text-xs font-medium text-muted">Description</span>
                <textarea value={s.about} onChange={(e) => set(i, { about: e.target.value })} rows={2} maxLength={stageLimits.about} placeholder="What this step does, in a line" className={`${inputCls} resize-y`} />
              </label>
              <Btn variant="ghost" className="self-start px-2 py-1.5 md:mt-5" aria-label={`Remove the stage ${s.name}`} onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <Trash2 size={14} aria-hidden />
              </Btn>
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p role="alert" className="text-xs text-red-600 dark:text-red-400" data-testid="stages-error">
          {error}
        </p>
      )}
    </div>
  )
}
