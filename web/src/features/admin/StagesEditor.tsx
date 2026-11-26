// The stages section of the pipeline edit form. The script decides which steps exist; this page lists the ones
// recent runs really reported, in the order they ran, with where each starts in the code, and gives each a title
// and a description that the run page shows instead of the raw name.
import { Link } from 'react-router'
import { ExternalLink, Trash2, TriangleAlert } from 'lucide-react'
import { cx } from '@/lib/format'
import { Btn, inputCls } from './ui'
import { countChips, duration, sentenceCase, stageLimits, stageRows, stepRan, type ReportedStep, type Stage } from './pipelines'

const ranText: Partial<Record<ReportedStep['status'], string>> = { ok: 'Ran in', warning: 'Warning in', failed: 'Failed in', running: 'Running in' }

/** What the runs say about one step: where the code starts it and how it last went. */
function Reported({ step, urlOf }: { step: ReportedStep; urlOf: (run: number, seq: number) => string }) {
  const took = duration(step.started_at, step.finished_at)
  const counts = countChips({ kera: false, counts: step.counts })
  return (
    <>
      {step.code_at ? (
        <span className="mt-1 block break-all text-xs text-fg" data-testid="stage-code">
          Starts at <code>{step.code_at}</code>
        </span>
      ) : (
        <span className="mt-1 block text-xs text-muted">The script does not send its place in the code.</span>
      )}
      <span className={cx('mt-1 block text-xs', step.status === 'failed' ? 'text-bad' : step.status === 'warning' ? 'text-warn-ink' : 'text-muted')} data-testid="stage-seen">
        {stepRan(step) ? (
          [`${ranText[step.status] ?? 'Ran in'} run #${step.run}`, took, ...counts.map((c) => c.text)].filter(Boolean).join(' · ')
        ) : (
          <>Declared in run #{step.run}, but no saved run has reached it.</>
        )}{' '}
        <Link to={urlOf(step.run, step.seq)} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 whitespace-nowrap font-medium text-accent underline-offset-2 hover:underline">
          {stepRan(step) ? 'View log' : 'View run'} <ExternalLink size={11} aria-hidden />
        </Link>
      </span>
    </>
  )
}

/**
 * `reported`: the script steps of the saved runs, in run order (undefined while they load, or when they could not be
 * loaded: then no label is called unused). `runs`: how many runs that is. The list is edited in place; the form's Save sends it.
 */
export function StagesEditor({
  value,
  onChange,
  reported,
  runs,
  loadError,
  urlOf,
  error,
}: {
  value: Stage[]
  onChange: (v: Stage[]) => void
  reported: ReportedStep[] | undefined
  runs: number
  loadError?: string
  urlOf: (run: number, seq: number) => string
  error?: string
}) {
  const rows = stageRows(value, reported)
  const unlabelled = (reported ?? []).filter((r) => !value.some((s) => s.name === r.name))
  const set = (name: string, patch: Partial<Stage>) => onChange(value.map((s) => (s.name === name ? { ...s, ...patch } : s)))
  const add = (names: string[]) => onChange([...value, ...names.map((name) => ({ name, title: '', about: '' }))].slice(0, stageLimits.max))
  const prompt = reported === undefined ? (loadError ? 'Steps unavailable' : 'Loading the steps of recent runs…') : unlabelled.length ? `Add a step without a label (${unlabelled.length})…` : reported.length ? 'Every reported step has a label' : 'No steps reported yet'
  return (
    <div className="space-y-3" data-testid="stages-editor">
      <select
        aria-label="Add a step that runs have reported"
        data-testid="add-stage"
        value=""
        disabled={unlabelled.length === 0}
        onChange={(e) => e.target.value && add(e.target.value === '*' ? unlabelled.map((r) => r.name) : [e.target.value.slice(1)])}
        className={`${inputCls} max-w-full sm:w-auto sm:min-w-[22rem]`}
      >
        <option value="">{prompt}</option>
        {unlabelled.map((r) => (
          <option key={r.name} value={`=${r.name}`}>
            {r.name} · run #{r.run}
            {r.code_at ? ` · ${r.code_at}` : ''}
          </option>
        ))}
        {unlabelled.length > 1 && <option value="*">All {unlabelled.length} of them</option>}
      </select>
      {loadError && (
        <p role="alert" className="text-xs text-red-600 dark:text-red-400">
          The steps of recent runs could not be loaded: {loadError}
        </p>
      )}
      {rows.length === 0 ? (
        <p className="rounded-lg border border-dashed border-line p-4 text-sm text-muted" data-testid="no-stages">
          {reported !== undefined && reported.length === 0
            ? 'No steps yet. The script decides its steps; they appear here after a run has reported them.'
            : 'No labels yet. Until a step has one, the run page shows its name in sentence case.'}
        </p>
      ) : (
        <ul className="grid gap-2.5" aria-label="Stages">
          {rows.map(({ stage: s, step, place }) => (
            <li
              key={s.name}
              className={cx('grid gap-x-3 gap-y-2 rounded-xl border bg-surface p-3 md:grid-cols-[minmax(0,17rem)_minmax(0,1fr)_minmax(0,1.6fr)_auto]', reported !== undefined && !step ? 'border-warn' : 'border-line')}
              data-testid="stage-row"
              data-reported={step ? 'yes' : reported === undefined ? 'unknown' : 'no'}
            >
              <div className="flex min-w-0 gap-2.5 self-start">
                <span aria-hidden className={cx('mt-0.5 grid size-6 shrink-0 place-items-center rounded-full text-xs font-semibold', step ? 'bg-accent-soft text-accent' : 'text-muted')}>
                  {step ? place : reported === undefined ? '' : <TriangleAlert size={15} className="text-warn" />}
                </span>
                <div className="min-w-0">
                  <code className="block break-all text-[13px] font-semibold" title="The name the script reports">
                    {s.name}
                  </code>
                  {step && <Reported step={step} urlOf={urlOf} />}
                  {!step && reported !== undefined && (
                    <span className="mt-1 block text-xs text-warn-ink" data-testid="stage-unused">
                      {runs === 0 ? 'No run yet, so this label is not shown anywhere.' : `None of the ${runs === 1 ? 'saved run reports' : `${runs} saved runs report`} this step, so this label is not shown anywhere.`}
                    </span>
                  )}
                </div>
              </div>
              <label className="block min-w-0 space-y-1">
                <span className="block text-xs font-medium text-muted">Title</span>
                <input value={s.title} onChange={(e) => set(s.name, { title: e.target.value })} placeholder={sentenceCase(s.name)} maxLength={stageLimits.title} className={inputCls} />
              </label>
              <label className="block min-w-0 space-y-1">
                <span className="block text-xs font-medium text-muted">Description</span>
                <textarea value={s.about} onChange={(e) => set(s.name, { about: e.target.value })} rows={2} maxLength={stageLimits.about} placeholder="What this step does, in a line" className={`${inputCls} resize-y`} />
              </label>
              <Btn variant="ghost" className="self-start px-2 py-1.5 md:mt-5" aria-label={`Remove the label of ${s.name}`} title="Remove this label (the step itself is the script's)" onClick={() => onChange(value.filter((x) => x.name !== s.name))}>
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
