// Step 5: everything that was chosen in one place, each part with a way back to change it, then the test and the final button.
import { useState } from 'react'
import { ShieldCheck } from 'lucide-react'
import { useModels, useTools } from '@/api/queries'
import { cx } from '@/lib/format'
import { useComposer } from '@/stores/composer'
import { ModelIcon } from '@/features/header/ModelPicker'
import { appLook } from '../ToolCard'
import { TestRun, type LastTest } from '../TestRun'
import { ReviewCard } from '../ReviewCard'
import { leftOut, selectedApps } from '../tools'
import { whenSentence, describeRun } from '../when'
import type { Draft } from '../draft'
import type { StepId } from '../steps'
import { Pill, StepHeader } from '../ui'
import { useNextRuns } from './WhenStep'

export function ReviewStep({ draft, steps, goTo, files, testBody, signature, lastTest, onTest, saveError }: { draft: Draft; steps: StepId[]; goTo: (s: StepId) => void; files: number; testBody: () => object; signature: string; lastTest: LastTest; onTest: (t: LastTest) => void; saveError?: string }) {
  const models = useModels()
  const tools = useTools()
  const model = useComposer((s) => s.model)
  const features = useComposer((s) => s.features)
  const toolIds = useComposer((s) => s.toolIds)
  const [more, setMore] = useState(false)
  const m = models.data?.find((x) => x.id === model)
  const local = !!m?.local
  const apps = selectedApps(toolIds, tools.data, local)
  const dropped = leftOut(toolIds, tools.data, local)
  const abilities = (m?.features ?? []).filter((f) => features[f.id])
  const goal = draft.goal.trim()
  const long = goal.length > 280 || goal.split('\n').length > 5
  const runs = useNextRuns(draft, steps.includes('when'), {})
  const edit = (s: StepId) => (steps.includes(s) ? () => goTo(s) : undefined)
  return (
    <div className="space-y-6">
      <StepHeader title="Review and test">Check the task, try it with a read-only test, then confirm. You can come back to any part with Edit.</StepHeader>

      {saveError && (
        <p role="alert" data-testid="save-error" className="rounded-xl border border-red-500/40 bg-bad-soft px-4 py-3 text-sm text-bad-ink">
          {saveError}
        </p>
      )}

      <div className="grid gap-3 lg:grid-cols-2">
        <ReviewCard title="Goal" testId="review-goal" onEdit={edit('what')}>
          {draft.title.trim() && <p className="font-medium">{draft.title.trim()}</p>}
          <p className={cx('whitespace-pre-wrap break-words', draft.title.trim() ? 'mt-1 text-muted' : 'font-medium', long && !more && 'line-clamp-5')}>{goal}</p>
          {long && (
            <button type="button" onClick={() => setMore(!more)} aria-expanded={more} className="mt-1 text-xs font-medium text-link underline">
              {more ? 'Show less' : 'Show all'}
            </button>
          )}
          {files > 0 && <p className="mt-2 text-xs text-muted">{files === 1 ? '1 file attached' : `${files} files attached`}</p>}
        </ReviewCard>

        <ReviewCard title="Assistant" testId="review-assistant" onEdit={edit('assistant')}>
          <div className="flex items-center gap-3">
            {m && <ModelIcon m={m} size="md" />}
            <div className="min-w-0">
              <p className="truncate font-medium">{m?.name ?? model}</p>
              {m?.local ? (
                <Pill tone="accent">
                  <ShieldCheck size={11} aria-hidden /> On server
                </Pill>
              ) : (
                <Pill tone="grey">Hosted</Pill>
              )}
            </div>
          </div>
          {abilities.length > 0 && <p className="mt-2 text-xs text-muted">Abilities: {abilities.map((f) => f.label).join(', ')}</p>}
          {draft.collections.length > 0 ? (
            <p className="mt-2 text-xs text-muted" data-testid="review-knowledge">
              Knowledge: {draft.collections.map((c) => c.name).join(', ')}
            </p>
          ) : null}
        </ReviewCard>

        <ReviewCard title="Apps and tools" testId="review-apps" onEdit={edit('tools')}>
          {apps.length ? (
            <ul className="flex flex-wrap gap-1.5">
              {apps.map((s) => (
                <li key={s.id} className="rounded-full border border-line bg-surface-2 px-2.5 py-0.5 text-xs">
                  {appLook(s).label}
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-muted">None. The assistant works with its own abilities.</p>
          )}
          {dropped.length > 0 && (
            <p className="mt-2 text-xs text-warn-ink" data-testid="review-left-out">
              Left out: {dropped.map((s) => appLook(s).label).join(', ')} (cannot be used with this assistant right now).
            </p>
          )}
        </ReviewCard>

        <ReviewCard title="When" testId="review-when" onEdit={edit('when')}>
          <p className="font-medium">{whenSentence(draft)}</p>
          {draft.when !== 'now' && runs.data?.[0] && <p className="mt-0.5 text-xs text-muted">Next run {describeRun(runs.data[0], draft.timezone, Date.now(), navigator.language)}</p>}
          {draft.when === 'now' && <p className="mt-0.5 text-xs text-muted">You can watch it work, and it asks you if it is blocked.</p>}
        </ReviewCard>
      </div>

      <TestRun body={testBody} signature={signature} last={lastTest} onChange={onTest} />
    </div>
  )
}
