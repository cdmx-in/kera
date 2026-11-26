// Admin > Pipelines: the list, the create and edit form, and the one-time token
// dialog. The detail page with the flow run view is PipelineRuns.tsx.
import { useEffect, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router'
import { ArrowLeftRight, ChevronLeft, ChevronRight, Clock, Copy, FileText, FolderPlus, Info, KeyRound, Library, ListChecks, MapPin, Pencil, Play, Plus, Power, RefreshCw, Server, Terminal, UserRound, Workflow, type LucideIcon } from 'lucide-react'
import { useCollections } from '@/features/workspace/api'
import { Chip, Empty } from '@/features/workspace/ui'
import { cx, copyText } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import type { AdminUser } from './api'
import { attempt, Badge, Btn, Card, Confirm, inputCls, Loading, Page, tdCls, thCls } from './ui'
import { UserPicker } from './UserPicker'
import { StatusChip } from './status'
import { StagesEditor } from './StagesEditor'
import { bad, EditorDialog, useFormErrors, VField } from './settings/dialogKit'
import { relative, when } from './settings/Integrations'
import { HowItRuns } from './PipelineSchedule'
import {
  browserZone,
  byAttention,
  dockerPush,
  envFile,
  everyText,
  lines,
  runSummary,
  scheduleBody,
  scheduleChanged,
  scheduleFormOf,
  scheduleSummary,
  secretsUrl,
  stagesChanged,
  stateOf,
  useReportedSteps,
  useScript,
  usePipelineApi,
  usePipelines,
  validatePipeline,
  validateSchedule,
  validateStages,
  type Pipeline,
  type Run,
  type ScheduleForm,
  type Stage,
  type State,
  type StepStatus,
} from './pipelines'

export const pipelinesBase = '/admin/pipelines'
export const pipelineUrl = (name: string) => `${pipelinesBase}/${encodeURIComponent(name)}`
export const editUrl = (name: string) => `${pipelineUrl(name)}/edit`
/** A run of a pipeline, optionally with a step to bring into view (?step=seq). */
export const runUrl = (name: string, run?: number, step?: number) => `${pipelineUrl(name)}${run != null ? `/${run}` : ''}${step != null ? `?step=${step}` : ''}`
export const newUrl = `${pipelinesBase}?new`
export const runnersUrl = `${pipelinesBase}?runners`
export const scriptUrl = (name: string) => `${pipelineUrl(name)}?tab=script`

/** /admin/pipelines/:name/edit, and /admin/pipelines?new for a new one: the form had outgrown a dialog. */
export function PipelineEditPage({ name }: { name: string | null }) {
  const nav = useNavigate()
  const q = usePipelines()
  const [shown, setShown] = useState<{ pipeline: Pipeline; token: string } | null>(null)
  const p = name ? q.data?.find((x) => x.name === name) : null
  if (name && !p)
    return (
      <Page>
        <Link to={pipelinesBase} className="inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
          <ChevronLeft size={14} aria-hidden /> Pipelines
        </Link>
        {q.isLoading || q.error ? (
          <Loading q={q} />
        ) : (
          <Empty icon={Workflow} title="No such pipeline">
            There is no pipeline named {name}.
          </Empty>
        )}
      </Page>
    )
  return (
    <>
      <PipelineForm
        page
        pipeline={p ?? null}
        onClose={() => nav(p ? pipelineUrl(p.name) : pipelinesBase)}
        onSaved={(saved, token) => {
          if (token) return setShown({ pipeline: saved, token })
          // a new Kera-run pipeline has no script yet: that is the next step
          if (!p && saved.run_mode === 'kera') {
            toast('Created. Add its script next.')
            return nav(scriptUrl(saved.name))
          }
          toast('Saved')
          nav(pipelineUrl(saved.name))
        }}
      />
      {shown && <TokenDialog pipeline={shown.pipeline} token={shown.token} onClose={() => nav(pipelineUrl(shown.pipeline.name))} />}
    </>
  )
}

/** The one status chip of the pipeline pages (see status.tsx): icon and upper-case word, never colour alone. */
export function StatusBadge({ status, title }: { status: State | StepStatus; title?: string }) {
  return <StatusChip status={status} title={title} />
}

/** "Expected every day; last good run 3 days ago." */
export const lateText = (p: Pick<Pipeline, 'every_minutes' | 'last_ok_at'>) => `Expected every ${everyText(p.every_minutes)}; ${p.last_ok_at ? `last good run ${relative(p.last_ok_at)}` : 'no good run yet'}.`

/** Text in a box with a Copy button. */
export function CopyBlock({ text, label }: { text: string; label: string }) {
  return (
    <div className="relative">
      <pre className="whitespace-pre-wrap break-words rounded-lg bg-bg-soft p-3 pr-24 text-xs">{text}</pre>
      <Btn className="absolute right-2 top-2 px-2 py-1 text-xs" aria-label={`Copy ${label}`} onClick={async () => (await copyText(text)) && toast('Copied')}>
        <Copy size={12} aria-hidden /> Copy
      </Btn>
    </div>
  )
}

// Token ---------------------------------------------------------------------------------

/** Shown once, after create or rotate: Kera keeps only a hash. Esc and the backdrop do not close it. */
export function TokenDialog({ pipeline, token, onClose }: { pipeline: Pipeline; token: string; onClose: () => void }) {
  const origin = location.origin
  return (
    <Dialog open persistent onClose={onClose} label="Pipeline token" testId="token-dialog" className="w-[min(40rem,calc(100vw-2rem))]">
      <DialogHeader title="Pipeline token" onClose={onClose} />
      <div className="max-h-[calc(100dvh-8rem)] space-y-3 overflow-y-auto p-4 text-sm">
        <div role="alert">
          <Section icon={KeyRound} tone="amber" title="1. Copy the token" hint="It is shown once; Kera keeps only a hash of it. Rotating it later stops this one working.">
            <CopyBlock text={token} label="token" />
          </Section>
        </div>
        <Section icon={FileText} tone="sky" title="2. Save it next to your script" hint="Put this in an env file (kera.env) next to your script or cron job.">
          <CopyBlock text={envFile(pipeline.name, origin, token)} label="env file" />
        </Section>
        <Section icon={Terminal} tone="violet" title="3. Push a folder" hint="From cron, CI or by hand. Every run then shows up here.">
          <CopyBlock text={dockerPush(pipeline, origin)} label="docker command" />
        </Section>
        <div className="flex justify-end">
          <Btn variant="primary" onClick={onClose} data-testid="token-done">
            I have copied it
          </Btn>
        </div>
      </div>
    </Dialog>
  )
}

// Form ----------------------------------------------------------------------------------

const formFields = ['name', 'title', 'targets', 'new_collections', 'every_minutes', 'owner_id', 'run_mode', 'frequency', 'at_minute', 'weekday', 'timezone', 'scheduled', 'stages']

export const tones = {
  accent: 'bg-accent-soft text-accent',
  amber: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
  sky: 'bg-sky-500/15 text-sky-700 dark:text-sky-300',
  violet: 'bg-violet-500/15 text-violet-700 dark:text-violet-300',
}

/** A panel of the form: coloured icon tile, title and one line of help. */
export function Section({ id, icon: Icon, tone, title, hint, aside, children }: { id?: string; icon: LucideIcon; tone: keyof typeof tones; title: string; hint: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section id={id} className="scroll-mt-4 space-y-3 rounded-xl border border-line bg-surface-2/40 p-4">
      <header className="flex items-start gap-3">
        <span className={cx('grid size-8 shrink-0 place-items-center rounded-lg', tones[tone])}>
          <Icon size={16} aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-semibold">{title}</h3>
          <p className="text-xs text-muted">{hint}</p>
        </div>
        {aside}
      </header>
      {children}
    </section>
  )
}

/** Where a pipeline's script lives, as its last run reported it. Kera never starts a script, so this is read-only. */
export function WhereItRuns({ run, kera }: { run: Pick<Run, 'source' | 'started_at'> | null; kera?: boolean }) {
  const s = run?.source ?? {}
  const rows: [string, string | undefined, boolean][] = [
    ['Machine', [s.user, s.host].filter(Boolean).join('@') || undefined, false],
    ['Folder', s.dir, true],
    ['Command', s.command, true],
    ['Reported by', s.client, false],
  ]
  const shown = rows.filter(([, v]) => v)
  return (
    <div data-testid="where-it-runs">
      {shown.length === 0 ? (
        <p className="text-sm text-muted">
          {kera ? (
            'No run has started yet.'
          ) : (
            <>
              Not reported yet. A run from the kera command (current version) or a script that sends <code>source</code> when it opens a run fills this in.
            </>
          )}
        </p>
      ) : (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
          {shown.map(([k, v, mono]) => (
            <div key={k} className="contents">
              <dt className="text-muted">{k}</dt>
              <dd className={cx('min-w-0 break-all', mono && 'font-mono text-xs leading-5')}>{v}</dd>
            </div>
          ))}
          {run && (
            <div className="contents">
              <dt className="text-muted">Last run</dt>
              <dd title={when(run.started_at)}>{relative(run.started_at)}</dd>
            </div>
          )}
        </dl>
      )}
      <p className="mt-3 text-xs text-muted">
        {kera ? 'Run by Kera: the script version and the runner come from Kera. Change the script on its Script tab and the timetable under How it runs.' : 'Kera does not start this. A cron job, CI or a person on that machine does; change the schedule or the script there.'}
      </p>
    </div>
  )
}

/** Create (pipeline = null) or edit. onSaved gets the pipeline and, for a new one, its token. */
export function PipelineForm({ pipeline, onClose, onSaved, page }: { pipeline: Pipeline | null; onClose: () => void; onSaved: (p: Pipeline, token?: string) => void; page?: boolean }) {
  const cols = useCollections()
  const pa = usePipelineApi()
  const [name, setName] = useState(pipeline?.name ?? '')
  const [title, setTitle] = useState(pipeline?.title ?? '')
  const [every, setEvery] = useState(String(pipeline?.every_minutes ?? 1440))
  const [enabled, setEnabled] = useState(pipeline?.enabled ?? true)
  // a collection that was deleted is dropped on save
  const [targets, setTargets] = useState<string[]>(pipeline?.targets.filter((t) => !t.missing).map((t) => t.id) ?? [])
  const [fresh, setFresh] = useState('')
  const [owner, setOwner] = useState<AdminUser | null>(null)
  const [adopt, setAdopt] = useState(pipeline?.adopt ?? false)
  const [stages, setStages] = useState<Stage[]>(() => pipeline?.stages ?? [])
  // what the saved runs really reported: the stage labels are attached to these
  const reported = useReportedSteps(pipeline?.name ?? '', !!pipeline)
  const [ask, setAsk] = useState(false)
  const [sched, setSched] = useState<ScheduleForm>(() => scheduleFormOf(pipeline, browserZone()))
  const kera = sched.mode === 'kera'
  // whether a script is saved: the timetable cannot be switched on without one (the server refuses with no_script)
  const script = useScript(pipeline?.name ?? '', !!pipeline && kera)
  const noScript = kera && (!pipeline || (script.data !== undefined && script.data.current === null))
  const form = useFormErrors({ ...validatePipeline({ name, every, targets, fresh: lines(fresh), mode: sched.mode }, !pipeline), ...validateSchedule(sched), ...validateStages(stages) }, !!pipeline)
  const dropped = pipeline?.targets.filter((t) => t.missing).length ?? 0
  const n = Number(every)
  const current = owner ?? pipeline?.owner

  const go = async () => {
    const body = { title, targets, new_collections: lines(fresh), every_minutes: n, enabled }
    let saved: Pipeline
    let token: string | undefined
    if (pipeline) {
      saved = await pa.update(pipeline.name, owner ? { ...body, owner_id: owner.id } : body)
      // after the update: a Kera-run pipeline's expected interval is set from its timetable, and this must have the last word
      if (scheduleChanged(pipeline, sched)) saved = await pa.schedule(pipeline.name, scheduleBody(sched))
      if (stagesChanged(pipeline.stages ?? [], stages)) saved = await pa.setStages(pipeline.name, stages)
    } else {
      const r = await pa.create({ name, ...body })
      saved = r.pipeline
      token = r.token
      // nothing is scheduled yet: there is no script. A Kera-run pipeline does not need the token (its runs carry their own), so the one-time dialog is skipped
      if (kera && (await attempt(async () => void (saved = await pa.schedule(saved.name, { ...scheduleBody(sched), scheduled: false }))))) token = undefined
    }
    if (adopt !== (pipeline?.adopt ?? false)) await attempt(() => pa.once(saved.name, { adopt }))
    onSaved(saved, token)
  }
  // turning adopt on asks first: it lets the next run take over files nobody owns
  const submit = async () => {
    if (adopt && !pipeline?.adopt) return setAsk(true)
    await go()
  }
  const pick = (id: string, on: boolean) => {
    setTargets(on ? [...targets, id] : targets.filter((x) => x !== id))
    form.touch('targets')
  }

  const presets: [string, number][] = [['Hourly', 60], ['Daily', 1440], ['Weekly', 10080], ['No expectation', 0]]
  // the page can be opened at #how-it-runs (the call to action on a pipeline started from outside)
  useEffect(() => {
    // #how-it-runs (the call to action on a pipeline started from outside) and #stages (add a description to a step)
    const id = location.hash.slice(1)
    if (id) document.getElementById(id)?.scrollIntoView({ block: 'start' })
  }, [])
  const saved = pipeline?.run_mode === 'kera' && pipeline.schedule.scheduled && pipeline.schedule.next_run_at ? pipeline.schedule : null
  return (
    <>
      <EditorDialog
        title={pipeline ? `Edit ${pipeline.name}` : 'New pipeline'}
        saveLabel={pipeline ? 'Save changes' : 'Create pipeline'}
        width="w-[min(60rem,calc(100vw-2rem))]"
        page={
          page
            ? {
                back: (
                  <Link to={pipeline ? pipelineUrl(pipeline.name) : pipelinesBase} className="inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
                    <ChevronLeft size={14} aria-hidden /> {pipeline ? pipeline.title || pipeline.name : 'Pipelines'}
                  </Link>
                ),
              }
            : undefined
        }
        managed={pipeline?.managed}
        form={form}
        fields={formFields}
        onClose={onClose}
        onSubmit={submit}
      >
        {!pipeline && (
          <p className="flex items-start gap-2.5 rounded-xl border border-accent/30 bg-accent-soft px-3.5 py-3 text-sm">
            <Info size={16} className="mt-0.5 shrink-0 text-accent" aria-hidden />
            {kera ? (
              <span>
                After you save, you add the <b>script</b> on its Script tab and switch the timetable on. A run carries its own credentials, so there is no token to copy; you can still rotate one later for pushes from outside.
              </span>
            ) : (
              <span>
                After you save, Kera shows this pipeline&apos;s <b>token once</b>. Put it in the cron job, CI or script that fills the collections; every run then shows up here as a flow of steps with logs.
              </span>
            )}
          </p>
        )}
        <div className="grid items-start gap-4 lg:grid-cols-2">
          <div className="space-y-4">
            <Section icon={Workflow} tone="accent" title="Details" hint="What the pipeline is called.">
              <div className="grid gap-3 sm:grid-cols-2">
                <VField label="Name" hint={pipeline ? 'The name cannot change.' : 'Used in URLs and commands: lowercase letters, digits and dashes.'} error={form.err('name')}>
                  <input name="name" value={name} disabled={!!pipeline} autoComplete="off" placeholder="docs-sync" onChange={(e) => setName(e.target.value)} className={inputCls + bad(form.err('name'))} />
                </VField>
                <VField label="Title" hint="The friendly name in the list. Optional." error={form.err('title')}>
                  <input name="title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Product docs" className={inputCls} />
                </VField>
              </div>
            </Section>

            <Section
              id="how-it-runs"
              icon={Play}
              tone="accent"
              title="How it runs"
              hint="Who starts the runs: your own scheduler, or Kera with a script and timetable stored here."
            >
              <HowItRuns
                value={sched}
                onChange={(patch) => setSched((s) => ({ ...s, ...patch }))}
                form={form}
                saved={saved}
                scriptHref={pipeline ? scriptUrl(pipeline.name) : null}
                noScript={noScript}
              />
            </Section>

            <Section icon={Clock} tone="violet" title="Schedule" hint={kera ? 'Kera starts the runs, so a good run is expected as often as the timetable says.' : 'Your cron job or CI starts the runs. Kera only watches that they happen.'}>
              {kera ? (
                <p className="text-sm text-muted" data-testid="expected-follows">
                  Expected every: follows the timetable above. A run that is a little overdue shows Late.
                </p>
              ) : (
                <>
                  <div className="flex flex-wrap gap-1.5" role="group" aria-label="Common schedules">
                    {presets.map(([label, v]) => (
                      <button
                        key={v}
                        type="button"
                        aria-pressed={n === v}
                        onClick={() => (setEvery(String(v)), form.touch('every_minutes'))}
                        className={cx('rounded-full border px-3 py-1 text-xs font-medium transition-colors', n === v ? 'border-accent bg-accent-soft text-accent' : 'border-line text-muted hover:bg-surface-2 hover:text-fg')}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                  <VField
                    label="Expected every (minutes)"
                    hint={Number.isInteger(n) && n > 0 ? `A good run is expected every ${everyText(n)}; a little later it shows Late.` : 'Kera never shows Late. 1440 is daily, 10080 weekly.'}
                    error={form.err('every_minutes')}
                  >
                    <input name="every_minutes" type="number" min={0} step={1} value={every} onChange={(e) => setEvery(e.target.value)} className={inputCls + bad(form.err('every_minutes'))} />
                  </VField>
                </>
              )}
              <label className="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-surface p-3 has-[:checked]:border-green-500/50 has-[:checked]:bg-green-500/5">
                <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="mt-0.5 size-4 accent-[var(--accent)]" />
                <span className="min-w-0">
                  <span className="flex items-center gap-1.5 text-sm font-medium">
                    <Power size={14} className={enabled ? 'text-green-600 dark:text-green-400' : 'text-muted'} aria-hidden /> Enabled
                  </span>
                  <span className="block text-xs text-muted">Switched off, the token is refused and the list says Off.</span>
                </span>
              </label>
            </Section>

            {pipeline && current && (
              <Section icon={UserRound} tone="violet" title="Owner" hint="Holds the uploaded files. Changing it moves them, and the collections the old owner held.">
                <p className="flex flex-wrap items-center gap-x-2 text-sm">
                  <span className="font-medium">{current.name || current.email}</span>
                  <span className="text-muted">{current.email}</span>
                  {owner && (
                    <Btn variant="ghost" className="px-2 py-0.5 text-xs" onClick={() => setOwner(null)}>
                      Undo
                    </Btn>
                  )}
                </p>
                <UserPicker onPick={setOwner} placeholder="Change owner: search by name or email" />
                {form.err('owner_id') && (
                  <span role="alert" className="block text-xs text-red-600 dark:text-red-400">
                    {form.err('owner_id')}
                  </span>
                )}
                <p className="text-xs text-muted">A user who owns a pipeline cannot be deleted.</p>
              </Section>
            )}
          </div>

          <div className="space-y-4">
            <Section
              icon={Library}
              tone="sky"
              title="Collections"
              hint="Where the synced files go. The pipeline can write to these and nothing else. Optional: a script that only runs tasks or reports runs needs none."
              aside={targets.length > 0 ? <Badge tone="accent">{targets.length} selected</Badge> : undefined}
            >
              <fieldset className="space-y-1.5">
                <legend className="sr-only">Collections this pipeline writes to</legend>
                <div className={'max-h-60 space-y-1.5 overflow-y-auto rounded-lg' + bad(form.err('targets'))}>
                  {cols.data?.length ? (
                    cols.data.map((c) => (
                      <label key={c.id} className="flex cursor-pointer items-center gap-2.5 rounded-lg border border-line bg-surface px-3 py-2 text-sm hover:bg-surface-2 has-[:checked]:border-accent has-[:checked]:bg-accent-soft">
                        <input type="checkbox" checked={targets.includes(c.id)} onChange={(e) => pick(c.id, e.target.checked)} className="size-4 accent-[var(--accent)]" />
                        <span className="min-w-0 flex-1 truncate">{c.name}</span>
                      </label>
                    ))
                  ) : (
                    <p className="rounded-lg border border-dashed border-line p-3 text-xs text-muted">{cols.isLoading ? 'Loading collections…' : 'No collections yet. Name a new one below.'}</p>
                  )}
                </div>
                {dropped > 0 && <p className="text-xs text-amber-700 dark:text-amber-400">{dropped === 1 ? 'A collection of this pipeline was deleted; saving drops it.' : `${dropped} collections of this pipeline were deleted; saving drops them.`}</p>}
              </fieldset>
              <VField
                label={
                  <span className="inline-flex items-center gap-1.5">
                    <FolderPlus size={13} aria-hidden /> Or create new collections
                  </span>
                }
                hint="One name per line. Created now and owned by you. Names must differ within a pipeline."
                error={form.err('targets') ?? form.err('new_collections')}
              >
                <textarea
                  name="targets"
                  rows={2}
                  value={fresh}
                  placeholder="Product docs"
                  onChange={(e) => (setFresh(e.target.value), form.touch('new_collections'))}
                  className={inputCls + bad(form.err('targets') ?? form.err('new_collections'))}
                />
              </VField>
            </Section>
            {pipeline && (
              <Section icon={MapPin} tone="sky" title="Where it runs" hint={`The machine, folder and command of the last run. Reported by the ${pipeline.run_mode === 'kera' ? 'runner' : 'script'}; not editable here.`}>
                <WhereItRuns run={pipeline.last_run} kera={pipeline.run_mode === 'kera'} />
              </Section>
            )}
          </div>
        </div>

        {pipeline && (
          <Section id="stages" icon={ListChecks} tone="violet" title="Stages" hint="The script decides which steps run. These are the steps its saved runs reported, in order; give each a title and a description for the run page. Saved with the rest of the form." aside={stages.length > 0 ? <Badge tone="accent">{stages.length}</Badge> : undefined}>
            <StagesEditor
              value={stages}
              onChange={(v) => (setStages(v), form.touch('stages'))}
              reported={pipeline ? reported.data?.steps : []}
              runs={reported.data?.runs ?? 0}
              loadError={reported.error?.message}
              urlOf={(run, seq) => runUrl(pipeline?.name ?? '', run, seq)}
              error={form.err('stages')}
            />
          </Section>
        )}

        <label className="flex cursor-pointer items-start gap-3 rounded-xl border border-amber-500/40 bg-amber-500/5 p-4 has-[:checked]:border-amber-500/70 has-[:checked]:bg-amber-500/10">
          <input type="checkbox" checked={adopt} onChange={(e) => setAdopt(e.target.checked)} className="mt-1 size-4 accent-[var(--accent)]" />
          <span className="min-w-0">
            <span className="flex items-center gap-1.5 text-sm font-semibold">
              <ArrowLeftRight size={14} className="text-amber-700 dark:text-amber-400" aria-hidden /> Adopt same-named files once
            </span>
            <span className="mt-0.5 block text-xs text-muted">
              The next run takes over files with the same name that were added by hand or imported, instead of uploading a second copy. Use it when you first move a folder you uploaded before. It clears itself after a good run.
            </span>
          </span>
        </label>
      </EditorDialog>
      <Confirm
        open={ask}
        title="Adopt same-named files once?"
        confirmLabel="Adopt once"
        onConfirm={go}
        onClose={() => setAsk(false)}
        body="The next run takes over files in these collections that have the same name as a file in your folder and that no pipeline owns. From then on this pipeline replaces and can remove them. Run once with --dry-run first to see how many."
      />
    </>
  )
}

// List ----------------------------------------------------------------------------------

export function PipelinesPage() {
  const q = usePipelines()
  const nav = useNavigate()
  const list = [...(q.data ?? [])].sort(byAttention)
  const add = (id: string) => (
    <Btn variant="primary" onClick={() => nav(newUrl)} data-testid={id}>
      <Plus size={14} aria-hidden /> New pipeline
    </Btn>
  )
  return (
    <Page
      title="Pipelines"
      actions={
        <>
          <Btn variant="ghost" onClick={() => void q.refetch()} aria-label="Refresh">
            <RefreshCw size={14} />
          </Btn>
          <Btn onClick={() => nav(runnersUrl)} data-testid="runners">
            <Server size={14} aria-hidden /> Runners
          </Btn>
          <Btn onClick={() => nav(secretsUrl)} data-testid="secrets">
            <KeyRound size={14} aria-hidden /> Secrets
          </Btn>
          {add('new-pipeline')}
        </>
      }
    >
      <p className="-mt-3 max-w-3xl text-[13px] text-muted">
        A pipeline keeps knowledge collections equal to a folder that lives somewhere else. Your cron job, CI or script starts it with the pipeline&apos;s token; each run shows as a flow of steps with logs.
      </p>
      {!q.data ? (
        <Loading q={q} />
      ) : list.length === 0 ? (
        <Empty icon={Workflow} title="No pipelines yet" action={add('new-pipeline-empty')}>
          Create one, pick the collection it fills, and run <code>kera pipeline push</code> from cron or CI with its token. Runs, steps, logs and errors show up here.
        </Empty>
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full text-sm" data-testid="pipelines">
              <thead>
                <tr>
                  <th className={thCls}>Pipeline</th>
                  <th className={thCls}>State</th>
                  <th className={cx(thCls, 'hidden md:table-cell')}>Schedule</th>
                  <th className={cx(thCls, 'hidden sm:table-cell')}>Last run</th>
                  <th className={cx(thCls, 'w-px')}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {list.map((p) => {
                  const state = stateOf(p)
                  const r = p.last_run
                  return (
                    <tr
                      key={p.name}
                      className="cursor-pointer align-top transition-colors hover:bg-surface-2"
                      data-testid={`pipeline-${p.name}`}
                      data-state={state}
                      onClick={(e) => !(e.target as HTMLElement).closest('a,button') && nav(`${pipelinesBase}/${encodeURIComponent(p.name)}`)}
                    >
                      <td className={cx(tdCls, 'min-w-[14rem]')}>
                        <Link to={`${pipelinesBase}/${encodeURIComponent(p.name)}`} className="font-medium text-accent hover:underline">
                          {p.title || p.name}
                        </Link>
                        <div className="text-xs text-muted">
                          <code>{p.name}</code>
                        </div>
                        <div className="mt-1 flex flex-wrap gap-1">
                          {p.run_mode === 'kera' && <Chip icon={Server} tone="accent">Run by Kera</Chip>}
                          {p.targets.map((t) => (
                            <Chip key={t.id} tone={t.missing ? 'red' : 'grey'} title={t.missing ? 'This collection was deleted' : undefined}>
                              {t.missing ? 'Deleted collection' : t.name}
                            </Chip>
                          ))}
                        </div>
                        <div className="mt-1 text-xs text-muted sm:hidden">{r ? `Last run ${relative(r.started_at)}` : 'Never run'}</div>
                      </td>
                      <td className={tdCls}>
                        <span className="flex flex-wrap gap-1">
                          <StatusBadge status={state} title={state === 'late' ? lateText(p) : undefined} />
                          {state === 'failed' && p.late && <StatusBadge status="late" title={lateText(p)} />}
                        </span>
                      </td>
                      <td className={cx(tdCls, 'hidden md:table-cell')}>
                        {p.run_mode === 'kera' ? (
                          p.schedule.scheduled ? (
                            <span title={p.schedule.next_run_at ? `Next run ${when(p.schedule.next_run_at)}` : undefined}>
                              {scheduleSummary(p.schedule)}
                              {p.schedule.next_run_at && <span className="block text-xs text-muted">Next run {relative(p.schedule.next_run_at)}</span>}
                            </span>
                          ) : (
                            <span className="text-muted">Not scheduled</span>
                          )
                        ) : p.every_minutes > 0 ? (
                          <span className="whitespace-nowrap">Expected every {everyText(p.every_minutes)}</span>
                        ) : (
                          <span className="text-muted">No expectation</span>
                        )}
                      </td>
                      <td className={cx(tdCls, 'hidden sm:table-cell')}>
                        {r ? (
                          <span title={when(r.started_at)}>
                            {relative(r.started_at)}
                            {r.dry_run && (
                              <>
                                {' '}
                                <Badge tone="accent">Dry run</Badge>
                              </>
                            )}
                            <span className="line-clamp-2 block break-words text-xs text-muted" title={r.status === 'failed' ? r.error : undefined}>
                              {runSummary(r)}
                            </span>
                          </span>
                        ) : (
                          <span className="text-muted">Never run</span>
                        )}
                      </td>
                      <td className={cx(tdCls, 'whitespace-nowrap text-right')}>
                        <Btn variant="ghost" className="px-2 py-1" aria-label={`Edit ${p.name}`} title="Edit" data-testid={`edit-${p.name}`} onClick={() => nav(editUrl(p.name))}>
                          <Pencil size={14} aria-hidden />
                        </Btn>
                        <ChevronRight size={16} className="ml-1 inline text-muted" aria-hidden />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </Page>
  )
}
