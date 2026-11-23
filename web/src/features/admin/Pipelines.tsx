// Admin > Pipelines: the list, the create and edit form, and the one-time token
// dialog. The detail page with the flow run view is PipelineRuns.tsx.
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { Check as CheckIcon, Circle, CircleAlert, CircleDashed, CircleOff, Copy, Plus, RefreshCw, TriangleAlert, Workflow } from 'lucide-react'
import { useCollections } from '@/features/workspace/api'
import { Chip, Empty } from '@/features/workspace/ui'
import { cx, copyText } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import type { AdminUser } from './api'
import { attempt, Badge, Btn, Card, Check, Confirm, inputCls, Loading, Page, tdCls, thCls } from './ui'
import { UserPicker } from './UserPicker'
import { bad, EditorDialog, useFormErrors, VField } from './settings/dialogKit'
import { relative, when } from './settings/Integrations'
import { byAttention, envFile, everyText, lines, runSummary, shq, stateOf, statusLabel, toneOf, usePipelineApi, usePipelines, validatePipeline, type Pipeline, type State, type StepStatus } from './pipelines'

export const pipelinesBase = '/admin/pipelines'

const icons = { pending: Circle, never: Circle, ok: CheckIcon, warning: TriangleAlert, late: TriangleAlert, failed: CircleAlert, skipped: CircleDashed, off: CircleOff }

/** Status is never colour alone: an icon per status (a spinner while running) next to the word. */
export function StatusIcon({ status, size = 14 }: { status: State | StepStatus; size?: number }) {
  if (status === 'running') return <span aria-hidden className="inline-block shrink-0 animate-spin rounded-full border-2 border-current border-t-transparent" style={{ width: size - 2, height: size - 2 }} />
  const Icon = icons[status]
  return <Icon size={size} aria-hidden className="shrink-0" />
}

export function StatusBadge({ status, title }: { status: State | StepStatus; title?: string }) {
  return (
    <Badge tone={toneOf(status)} title={title}>
      <StatusIcon status={status} size={11} /> {statusLabel[status]}
    </Badge>
  )
}

/** "Expected every day; last good run 3 days ago." */
export const lateText = (p: Pick<Pipeline, 'every_minutes' | 'last_ok_at'>) => `Expected every ${everyText(p.every_minutes)}; ${p.last_ok_at ? `last good run ${relative(p.last_ok_at)}` : 'no good run yet'}.`

/** Text in a box with a Copy button. */
export function CopyBlock({ text, label }: { text: string; label: string }) {
  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-lg bg-bg-soft p-3 pr-20 text-xs">{text}</pre>
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
        <p role="alert" className="rounded-lg border border-amber-300 bg-amber-50 p-3 text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300">
          Copy the token now. It is shown once; Kera keeps only a hash of it. Rotating it later stops this one working.
        </p>
        <CopyBlock text={token} label="token" />
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">
            Put this in an env file (<code>kera.env</code>) next to your script or cron job
          </p>
          <CopyBlock text={envFile(pipeline.name, origin, token)} label="env file" />
        </div>
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted">Then push a folder, from cron, CI or by hand</p>
          <CopyBlock text={`docker run --rm --env-file kera.env -v "$PWD/docs:/docs:ro" \\\n  ghcr.io/cdmx-in/kera pipeline push${pipeline.targets[0] ? ` --collection ${shq(pipeline.targets[0].name)}` : ''} --dir /docs`} label="docker command" />
        </div>
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

const formFields = ['name', 'title', 'targets', 'new_collections', 'every_minutes', 'owner_id']

/** Create (pipeline = null) or edit. onSaved gets the pipeline and, for a new one, its token. */
export function PipelineForm({ pipeline, onClose, onSaved }: { pipeline: Pipeline | null; onClose: () => void; onSaved: (p: Pipeline, token?: string) => void }) {
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
  const [ask, setAsk] = useState(false)
  const form = useFormErrors(validatePipeline({ name, every, targets, fresh: lines(fresh) }, !pipeline), !!pipeline)
  const dropped = pipeline?.targets.filter((t) => t.missing).length ?? 0
  const n = Number(every)
  const current = owner ?? pipeline?.owner

  const go = async () => {
    const body = { title, targets, new_collections: lines(fresh), every_minutes: n, enabled }
    let saved: Pipeline
    let token: string | undefined
    if (pipeline) saved = await pa.update(pipeline.name, owner ? { ...body, owner_id: owner.id } : body)
    else {
      const r = await pa.create({ name, ...body })
      saved = r.pipeline
      token = r.token
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

  return (
    <>
      <EditorDialog title={pipeline ? `Edit ${pipeline.name}` : 'New pipeline'} managed={pipeline?.managed} form={form} fields={formFields} onClose={onClose} onSubmit={submit}>
        <div className="grid gap-3 sm:grid-cols-2">
          <VField label="Name" hint={pipeline ? 'The name cannot change.' : 'In URLs and commands: lowercase letters, digits and dashes.'} error={form.err('name')}>
            <input name="name" value={name} disabled={!!pipeline} autoComplete="off" placeholder="docs-sync" onChange={(e) => setName(e.target.value)} className={inputCls + bad(form.err('name'))} />
          </VField>
          <VField label="Title" hint="Shown in the list. Optional." error={form.err('title')}>
            <input name="title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Product docs" className={inputCls} />
          </VField>
        </div>

        <fieldset className="space-y-1">
          <legend className="text-xs font-medium text-muted">Collections this pipeline writes to</legend>
          <div className={'max-h-40 space-y-2 overflow-y-auto rounded-lg border border-line p-2' + bad(form.err('targets'))}>
            {cols.data?.length ? (
              cols.data.map((c) => (
                <div key={c.id}>
                  <Check checked={targets.includes(c.id)} onChange={(on) => pick(c.id, on)}>
                    {c.name}
                  </Check>
                </div>
              ))
            ) : (
              <p className="text-xs text-muted">{cols.isLoading ? 'Loading collections…' : 'No collections yet. Name a new one below.'}</p>
            )}
          </div>
          {dropped > 0 && <p className="text-xs text-amber-700 dark:text-amber-400">{dropped === 1 ? 'A collection of this pipeline was deleted; saving drops it.' : `${dropped} collections of this pipeline were deleted; saving drops them.`}</p>}
        </fieldset>
        <VField label="New collections" hint="One name per line. Created now, owned by you. Names must differ within a pipeline." error={form.err('targets') ?? form.err('new_collections')}>
          <textarea
            name="targets"
            rows={2}
            value={fresh}
            placeholder="Product docs"
            onChange={(e) => (setFresh(e.target.value), form.touch('new_collections'))}
            className={inputCls + bad(form.err('targets') ?? form.err('new_collections'))}
          />
        </VField>

        <div className="grid items-start gap-3 sm:grid-cols-2">
          <VField
            label="Expected every (minutes)"
            hint={Number.isInteger(n) && n > 0 ? `A good run is expected every ${everyText(n)}; a little later it shows Late.` : 'Kera never shows Late. 1440 is daily, 10080 weekly.'}
            error={form.err('every_minutes')}
          >
            <input name="every_minutes" type="number" min={0} step={1} value={every} onChange={(e) => setEvery(e.target.value)} className={inputCls + bad(form.err('every_minutes'))} />
          </VField>
          <div className="space-y-1 sm:pt-5">
            <Check checked={enabled} onChange={setEnabled}>
              Enabled
            </Check>
            <p className="text-xs text-muted">Switched off, the token is refused and the list says Off.</p>
          </div>
        </div>

        {pipeline && current && (
          <div className="space-y-1.5">
            <span className="block text-xs font-medium text-muted">Owner</span>
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
            <span className="block text-xs text-muted">The owner holds the uploaded files. Changing it moves them, and the collections the old owner held. A user who owns a pipeline cannot be deleted.</span>
          </div>
        )}

        <div className="space-y-1">
          <Check checked={adopt} onChange={setAdopt}>
            Adopt same-named files once
          </Check>
          <p className="text-xs text-muted">The next run takes over files with the same name that were added by hand or imported, instead of uploading a second copy. Use it when you first move a folder you uploaded before. It clears itself after a good run.</p>
        </div>
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
  const [creating, setCreating] = useState(false)
  const [shown, setShown] = useState<{ pipeline: Pipeline; token: string } | null>(null)
  const list = [...(q.data ?? [])].sort(byAttention)
  const add = (id: string) => (
    <Btn variant="primary" onClick={() => setCreating(true)} data-testid={id}>
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
                  <th className={cx(thCls, 'hidden md:table-cell')}>Expected every</th>
                  <th className={cx(thCls, 'hidden sm:table-cell')}>Last run</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {list.map((p) => {
                  const state = stateOf(p)
                  const r = p.last_run
                  return (
                    <tr key={p.name} className="align-top" data-testid={`pipeline-${p.name}`} data-state={state}>
                      <td className={cx(tdCls, 'min-w-0')}>
                        <Link to={`${pipelinesBase}/${encodeURIComponent(p.name)}`} className="font-medium hover:underline">
                          {p.title || p.name}
                        </Link>
                        <div className="text-xs text-muted">
                          <code>{p.name}</code>
                        </div>
                        <div className="mt-1 flex flex-wrap gap-1">
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
                      <td className={cx(tdCls, 'hidden whitespace-nowrap md:table-cell')}>{p.every_minutes > 0 ? everyText(p.every_minutes) : <span className="text-muted">No expectation</span>}</td>
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
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      {creating && (
        <PipelineForm
          pipeline={null}
          onClose={() => setCreating(false)}
          onSaved={(p, token) => {
            setCreating(false)
            if (token) setShown({ pipeline: p, token })
          }}
        />
      )}
      {shown && (
        <TokenDialog
          pipeline={shown.pipeline}
          token={shown.token}
          onClose={() => {
            setShown(null)
            nav(`${pipelinesBase}/${encodeURIComponent(shown.pipeline.name)}`)
          }}
        />
      )}
    </Page>
  )
}
