// The new-task wizard: Goal, Assistant, Apps and tools, Schedule (for people who may schedule), Review and test.
// The step is in the URL (?step=), so Back, a reload and a link all land on it; the draft survives a reload and the
// round trip of connecting an app (sessionStorage). The assistant, its switches and the apps live in the composer
// store, as before, because the chat engine's request reads them from there.
import { useEffect, useReducer, useRef, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, ArrowRight, Loader2, RotateCcw } from 'lucide-react'
import { api, ApiError } from '@/api/client'
import { useMe, useModels, useTools } from '@/api/queries'
import { useChat } from '@/stores/chat'
import { useComposer } from '@/stores/composer'
import { toast } from '@/stores/toast'
import { cx, storageGet, storageSet } from '@/lib/format'
import { Spinner } from '@/ui/misc'
import { parseChatParams } from '@/app/search-params'
import { Confirm } from '@/features/admin/ui'
import { isChatModel } from '@/features/chat/model-select'
import { sendMessage } from '@/features/chat/actions'
import { useUploads } from '@/features/chat/composer/uploads'
import { TopBar } from '../TaskPage'
import { canSchedule, type Schedule } from '../lib'
import { Stepper } from './Stepper'
import type { LastTest } from './TestRun'
import { GoalStep } from './steps/GoalStep'
import { AssistantStep } from './steps/AssistantStep'
import { ToolsStep } from './steps/ToolsStep'
import { WhenStep } from './steps/WhenStep'
import { ReviewStep } from './steps/ReviewStep'
import { draftKey, initialDraft, parseSaved, reducer, taskBody, whenParam, type Picks } from './draft'
import { STEP_LABEL, canOpen, firstIncomplete, isComplete, stepFromParam, stepList, validateStep, type Context, type StepId } from './steps'
import { applyDefaults, chooseAssistant, useWizardSetup, type Preset } from './setup'
import { effectiveToolIds } from './tools'

/** Waits for what the first render needs (who you are, the assistants), then mounts the wizard. */
export default function Wizard({ job }: { job?: Schedule }) {
  const me = useMe()
  const models = useModels()
  if (!me.data || models.isPending) {
    return (
      <div className="grid h-full place-items-center text-muted">
        <Spinner size={20} />
      </div>
    )
  }
  return <WizardBody job={job} schedulable={canSchedule(me.data)} />
}

/** A copy of the address parameters with `change` applied. */
const withParams = (p: URLSearchParams, change: (n: URLSearchParams) => void) => {
  const n = new URLSearchParams(p)
  change(n)
  return n
}

/** Which step a server field belongs to, for an error the server gives back on save. */
const stepOfField: Record<string, StepId> = { title: 'what', prompt: 'what', files: 'what', model_id: 'assistant', frequency: 'when', at_minute: 'when', weekday: 'when', timezone: 'when' }

function WizardBody({ job, schedulable }: { job?: Schedule; schedulable: boolean }) {
  const [search, setSearch] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const models = useModels()
  const tools = useTools()
  const params = parseChatParams(search)
  const key = draftKey(job?.id)
  const edit = !!job
  const steps = stepList(schedulable)

  // what a reload or the return from connecting an app finds again
  const [saved] = useState(() => parseSaved(storageGet('session', key)))
  const [draft, dispatch] = useReducer(reducer, undefined, () => saved?.draft ?? initialDraft({ job, goal: params.q, when: whenParam(search.get('when')), zone: Intl.DateTimeFormat().resolvedOptions().timeZone, schedulable }))
  const fresh = useRef(initialDraft({ job, when: whenParam(search.get('when')), zone: draft.timezone, schedulable }))
  const uploads = useUploads()

  // a chat or task left open in the store must not receive the new goal
  useEffect(() => {
    const st = useChat.getState()
    if (st.chatId || st.currentId || st.mode) st.reset(null)
  }, [])

  const preset: Preset | undefined = saved ? { model_id: saved.model, features: saved.features, tool_ids: saved.toolIds } : job && { model_id: job.model_id, features: job.features, tool_ids: job.tool_ids }
  const ready = useWizardSetup(params.models, params.tools, preset)

  const model = useComposer((s) => s.model)
  const toolIds = useComposer((s) => s.toolIds)
  const features = useComposer((s) => s.features)
  const picked = models.data?.find((m) => m.id === model)
  const ctx: Context = { model, modelKnown: !!picked && isChatModel(picked), uploading: uploads.busy }
  const step = stepFromParam(search.get('step'), steps, draft, ctx)
  const at = steps.indexOf(step)

  // the address names the step; a missing, unknown or out-of-reach one is corrected in place
  useEffect(() => {
    if (!ready || search.get('step') === step) return
    setSearch((p) => withParams(p, (n) => n.set('step', step)), { replace: true })
  }, [ready, step, search, setSearch])
  // one-shot parameters are consumed once the picks are set; a failed Connect comes back as ?oauth_error=
  const oauthError = search.get('oauth_error')
  useEffect(() => {
    if (!ready || !(search.has('q') || search.has('tools') || oauthError)) return
    if (oauthError) toast(oauthError === 'provider_denied' ? 'Connection cancelled' : `Could not connect the app (${oauthError.replace(/_/g, ' ')})`, 'error')
    setSearch((p) => withParams(p, (n) => ['q', 'tools', 'oauth_error'].forEach((k) => n.delete(k))), { replace: true })
  }, [ready, search, oauthError, setSearch])

  // keep the draft: a reload, or the trip out to connect an app and back, finds it again
  const done = useRef(false)
  const upKey = uploads.ready.map((f) => f.id).join(',')
  useEffect(() => {
    if (!ready || done.current) return
    const kept = [...draft.kept, ...uploads.ready.filter((f) => !draft.kept.some((k) => k.id === f.id))]
    storageSet('session', key, JSON.stringify({ draft: { ...draft, kept }, model, toolIds, features }))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, draft, upKey, model, toolIds, features, key])

  // errors show for a step after Next was pressed on it
  const [attempted, setAttempted] = useState<StepId[]>([])
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [confirmReset, setConfirmReset] = useState(false)
  const [lastTest, setLastTest] = useState<LastTest>({ ranWith: '' })
  const errors = attempted.includes(step) ? validateStep(step, draft, ctx) : {}
  const body = useRef<HTMLDivElement>(null)

  const go = (to: StepId, replace = false) => {
    setSearch((p) => withParams(p, (n) => n.set('step', to)), { replace })
    // move focus to the new step's heading and the page back to its top
    requestAnimationFrame(() => {
      body.current?.scrollTo({ top: 0 })
      body.current?.querySelector<HTMLElement>('[data-step-title]')?.focus({ preventScroll: true })
    })
  }
  const next = () => {
    if (!isComplete(step, draft, ctx)) {
      setAttempted((a) => [...new Set([...a, step])])
      requestAnimationFrame(() => body.current?.querySelector<HTMLElement>('[aria-invalid="true"], textarea, input')?.focus())
      return
    }
    go(steps[at + 1])
  }

  const local = !!picked?.local
  const picks: Picks = { model, toolIds: effectiveToolIds(toolIds, tools.data, local), features, files: uploads.ready }
  const taskBodyNow = () => taskBody(draft, picks, edit)
  const signature = JSON.stringify(taskBodyNow())

  const submit = async () => {
    const bad = firstIncomplete(steps, draft, ctx)
    if (bad) {
      setAttempted((a) => [...new Set([...a, bad])])
      return go(bad)
    }
    setBusy(true)
    setSaveError('')
    try {
      if (draft.when === 'now') {
        // ponytail: ?q= only prefills; a task runs tools, so it never starts without a click.
        useChat.getState().reset(null, false, 'cowork')
        const r = await sendMessage(draft.goal.trim(), [...draft.kept, ...uploads.ready, ...draft.collections], draft.title.trim() || undefined)
        if (!r.ok || !r.chatId) return void useChat.getState().reset(null)
        done.current = true
        storageSet('session', key, null)
        navigate(`/cowork/${r.chatId}`)
        return
      }
      await api(job ? `/api/v1/cowork/schedules/${job.id}` : '/api/v1/cowork/schedules', { method: job ? 'PUT' : 'POST', json: taskBodyNow() })
      done.current = true
      storageSet('session', key, null)
      await qc.invalidateQueries({ queryKey: ['cowork', 'schedules'] })
      navigate('/cowork?tab=scheduled')
    } catch (e) {
      const msg = e instanceof Error ? e.message : 'Could not save the scheduled task'
      setSaveError(msg)
      toast(msg, 'error')
      const badStep = e instanceof ApiError && e.field ? stepOfField[e.field] : undefined
      if (badStep && steps.includes(badStep)) go(badStep)
    } finally {
      setBusy(false)
    }
  }

  const startOver = () => {
    storageSet('session', key, null)
    dispatch({ type: 'restore', draft: fresh.current })
    uploads.clear()
    setAttempted([])
    setConfirmReset(false)
    if (picked) applyDefaults(picked, tools.data)
    go(steps[0], true)
  }

  const label = draft.when === 'now' ? 'Start task' : edit ? 'Save changes' : 'Save schedule'
  const dirty = !!(draft.goal || draft.title || draft.kept.length || draft.collections.length || uploads.items.length)

  return (
    <div className="flex h-full flex-col" data-testid="new-task-form" data-step={step}>
      <TopBar>
        <h1 className="flex-1 text-[15px] font-semibold">
          <Link to={edit ? '/cowork?tab=scheduled' : '/cowork'} className="text-muted hover:text-fg">
            Cowork
          </Link>{' '}
          / {edit ? 'Edit scheduled task' : 'New task'}
        </h1>
      </TopBar>
      <div ref={body} className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-4xl px-4 pb-10 pt-2 sm:px-6">
          {!ready ? (
            <div className="grid place-items-center py-20 text-muted">
              <Spinner size={20} />
            </div>
          ) : (
            <>
              <Stepper steps={steps} current={step} done={(s) => isComplete(s, draft, ctx)} reachable={(s) => canOpen(s, steps, draft, ctx)} onGo={(s) => go(s)} />
              <p className="mt-2 text-xs text-muted sm:hidden" aria-hidden>
                Step {at + 1} of {steps.length}: {STEP_LABEL[step]}
              </p>
              <div key={step} className="mt-6 animate-fade-up" data-testid={`panel-${step}`}>
                {step === 'what' && <GoalStep draft={draft} dispatch={dispatch} errors={errors} uploads={uploads} edit={edit} />}
                {step === 'assistant' && <AssistantStep draft={draft} dispatch={dispatch} errors={errors} onPick={(m) => chooseAssistant(m, tools.data)} />}
                {step === 'tools' && <ToolsStep schedulable={steps.includes('when')} />}
                {step === 'when' && <WhenStep draft={draft} dispatch={dispatch} errors={errors} edit={edit} />}
                {step === 'review' && <ReviewStep draft={draft} steps={steps} goTo={(s) => go(s)} files={draft.kept.length + uploads.ready.length} testBody={taskBodyNow} signature={signature} lastTest={lastTest} onTest={setLastTest} saveError={saveError} />}
              </div>
            </>
          )}
        </div>
      </div>
      <footer className="shrink-0 border-t border-line bg-bg/95 backdrop-blur">
        <div className="mx-auto flex w-full max-w-4xl items-center gap-2 px-4 py-3 sm:px-6">
          {at > 0 ? (
            <button type="button" data-testid="wizard-back" onClick={() => go(steps[at - 1])} className="inline-flex items-center gap-1.5 rounded-lg border border-line bg-surface px-3.5 py-2 text-sm font-medium hover:bg-surface-2">
              <ArrowLeft size={15} aria-hidden /> Back
            </button>
          ) : (
            <Link to={edit ? '/cowork?tab=scheduled' : '/cowork'} className="rounded-lg px-3.5 py-2 text-sm font-medium text-muted hover:bg-surface-2 hover:text-fg">
              Cancel
            </Link>
          )}
          {!edit && dirty && (
            <button type="button" data-testid="start-over" title="Start over" onClick={() => setConfirmReset(true)} className="inline-flex items-center gap-1 rounded-lg px-2 py-2 text-xs text-muted hover:bg-surface-2 hover:text-fg">
              <RotateCcw size={12} aria-hidden /> <span className="max-sm:sr-only">Start over</span>
            </button>
          )}
          <p role="status" className="min-w-0 flex-1 truncate px-2 text-right text-xs text-red-600 dark:text-red-400">
            {Object.values(errors)[0]}
          </p>
          {step === 'review' ? (
            <button type="button" data-testid={draft.when === 'now' ? 'start-task' : 'save-schedule'} disabled={busy || !ready} onClick={() => void submit()} className={cx('inline-flex items-center gap-1.5 rounded-lg bg-accent px-5 py-2 text-sm font-semibold text-white hover:bg-accent-bright disabled:opacity-60')}>
              {busy && <Loader2 size={15} className="animate-spin" aria-hidden />} {label}
            </button>
          ) : (
            <button type="button" data-testid="wizard-next" disabled={!ready} onClick={next} className="inline-flex items-center gap-1.5 rounded-lg bg-accent px-5 py-2 text-sm font-semibold text-white hover:bg-accent-bright disabled:opacity-60">
              Next <ArrowRight size={15} aria-hidden />
            </button>
          )}
        </div>
      </footer>
      <Confirm open={confirmReset} title="Start over?" body="This clears the goal, files and choices on this page." onConfirm={startOver} onClose={() => setConfirmReset(false)} />
    </div>
  )
}
