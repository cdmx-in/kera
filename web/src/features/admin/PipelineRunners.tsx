// Admin > Pipelines > Runners: the containers that execute the scripts of Kera-run pipelines.
// A runner's token is shown once, in RunnerTokenDialog, and never again: the list has only its prefix.
import { useState } from 'react'
import { Link } from 'react-router'
import { Check, ChevronLeft, CircleOff, FileText, KeyRound, Plus, RefreshCw, Server, Terminal, Trash2, TriangleAlert } from 'lucide-react'
import { Empty } from '@/features/workspace/ui'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Badge, Btn, Card, Confirm, inputCls, Loading, Page, tdCls, thCls } from './ui'
import { Notice } from './Notice'
import { CopyBlock, pipelinesBase, runnersUrl, Section } from './Pipelines'
import { EditorDialog, useFormErrors, VField, bad } from './settings/dialogKit'
import { relative, when } from './settings/Integrations'
import { noRunnerText, runnerEnvFile, useRunners, usePipelineApi, validateRunner, type Runner } from './pipelines'

/** The amber note for a page whose runs need a runner: shown only once the runners are known and none is online. */
export function NoRunnerHint({ runners }: { runners: Runner[] | undefined }) {
  if (!runners || runners.some((r) => r.online)) return null
  return (
    <Notice
      tone="amber"
      icon={TriangleAlert}
      role="status"
      testId="no-runner"
      actions={
        <Link to={runnersUrl} className="inline-flex items-center gap-1.5 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium hover:bg-surface-2">
          <Server size={14} aria-hidden /> Runners
        </Link>
      }
    >
      {noRunnerText}
    </Notice>
  )
}

/** Shown once, after create or rotate: Kera keeps only a hash. Esc and the backdrop do not close it. */
export function RunnerTokenDialog({ name, token, onClose }: { name: string; token: string; onClose: () => void }) {
  return (
    <Dialog open persistent onClose={onClose} label="Runner token" testId="runner-token-dialog" className="w-[min(40rem,calc(100vw-2rem))]">
      <DialogHeader title={`Runner token: ${name}`} onClose={onClose} />
      <div className="max-h-[calc(100dvh-8rem)] space-y-3 overflow-y-auto p-4 text-sm">
        <div role="alert">
          <Section icon={KeyRound} tone="amber" title="1. Copy the token" hint="It is shown once; Kera keeps only a hash of it. Rotating it later stops this one working.">
            <CopyBlock text={token} label="token" />
          </Section>
        </div>
        <Section icon={FileText} tone="sky" title="2. Give it to the runner" hint="Put this in the runner's env file or compose environment. KERA_URL must be an address the runner's container can reach.">
          <CopyBlock text={runnerEnvFile(location.origin, token)} label="runner env" />
        </Section>
        <Section icon={Terminal} tone="violet" title="3. Start the runner" hint="Build and start the container from sidecars/runner in the Kera repository (docker-compose.example.yml there). This page shows the runner as online within a minute.">
          <p className="text-xs text-muted">Mount only the folders the scripts must read or change: that is all a script can reach.</p>
        </Section>
        <div className="flex justify-end">
          <Btn variant="primary" onClick={onClose} data-testid="runner-token-done">
            I have copied it
          </Btn>
        </div>
      </div>
    </Dialog>
  )
}

export function OnlineBadge({ runner }: { runner: Pick<Runner, 'online' | 'last_seen_at'> }) {
  if (runner.online)
    return (
      <Badge tone="green">
        <Check size={11} aria-hidden /> Online
      </Badge>
    )
  return (
    <Badge tone="grey">
      <CircleOff size={11} aria-hidden /> {runner.last_seen_at ? 'Offline' : 'Never connected'}
    </Badge>
  )
}

/** The runners as a table; the token column is the prefix only. */
export function RunnerTable({ runners, onRotate, onDelete }: { runners: Runner[]; onRotate: (r: Runner) => void; onDelete: (r: Runner) => void }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm" data-testid="runners">
        <thead>
          <tr>
            <th className={thCls}>Runner</th>
            <th className={thCls}>Status</th>
            <th className={thCls + ' hidden sm:table-cell'}>Last seen</th>
            <th className={thCls + ' hidden md:table-cell'}>Version</th>
            <th className={thCls + ' hidden md:table-cell'}>Token</th>
            <th className={thCls + ' w-px'}>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {runners.map((r) => (
            <tr key={r.id} data-testid={`runner-${r.name}`} data-online={r.online}>
              <td className={tdCls + ' font-medium'}>{r.name}</td>
              <td className={tdCls}>
                <OnlineBadge runner={r} />
              </td>
              <td className={tdCls + ' hidden whitespace-nowrap sm:table-cell'}>{r.last_seen_at ? <span title={when(r.last_seen_at)}>{relative(r.last_seen_at)}</span> : <span className="text-muted">Never</span>}</td>
              <td className={tdCls + ' hidden md:table-cell'}>{r.version || <span className="text-muted">–</span>}</td>
              <td className={tdCls + ' hidden md:table-cell'}>
                <code className="text-xs">{r.token_prefix}…</code>
              </td>
              <td className={tdCls + ' whitespace-nowrap text-right'}>
                <Btn variant="ghost" className="px-2 py-1" aria-label={`Rotate token of ${r.name}`} title="Rotate token" onClick={() => onRotate(r)}>
                  <KeyRound size={14} aria-hidden />
                </Btn>
                <Btn variant="ghost" className="px-2 py-1 text-red-600 dark:text-red-400" aria-label={`Delete ${r.name}`} title="Delete" onClick={() => onDelete(r)}>
                  <Trash2 size={14} aria-hidden />
                </Btn>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function NewRunnerDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string, token: string) => void }) {
  const pa = usePipelineApi()
  const [name, setName] = useState('')
  const form = useFormErrors(validateRunner({ name }))
  return (
    <EditorDialog
      title="New runner"
      saveLabel="Create runner"
      form={form}
      fields={['name']}
      onClose={onClose}
      onSubmit={async () => {
        const r = await pa.createRunner(name.trim())
        onCreated(r.runner.name, r.token)
      }}
    >
      <VField label="Name" hint="Lowercase letters, digits and dashes, for example build-server. It shows on the runs this runner executes." error={form.err('name')}>
        <input name="name" value={name} autoComplete="off" autoFocus placeholder="build-server" onChange={(e) => setName(e.target.value)} className={inputCls + bad(form.err('name'))} />
      </VField>
    </EditorDialog>
  )
}

/** /admin/pipelines?runners */
export function RunnersPage() {
  const q = useRunners()
  const pa = usePipelineApi()
  const [creating, setCreating] = useState(false)
  const [shown, setShown] = useState<{ name: string; token: string } | null>(null)
  const [ask, setAsk] = useState<{ kind: 'rotate' | 'delete'; runner: Runner } | null>(null)
  const runners = q.data?.runners
  const add = (id: string) => (
    <Btn variant="primary" onClick={() => setCreating(true)} data-testid={id}>
      <Plus size={14} aria-hidden /> New runner
    </Btn>
  )
  return (
    <Page>
      <Link to={pipelinesBase} className="inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
        <ChevronLeft size={14} aria-hidden /> Pipelines
      </Link>
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="flex-1 text-lg font-semibold">Runners</h2>
        <Btn variant="ghost" onClick={() => void q.refetch()} aria-label="Refresh">
          <RefreshCw size={14} />
        </Btn>
        {add('new-runner')}
      </div>
      <p className="-mt-3 max-w-3xl text-[13px] text-muted">
        A runner is the separate container that executes the scripts of pipelines Kera runs. It connects to Kera with a token and picks up queued runs; Kera itself never runs a script.
      </p>
      {!runners ? (
        <Loading q={q} />
      ) : runners.length === 0 ? (
        <Empty icon={Server} title="No runners yet" action={add('new-runner-empty')}>
          Create one, copy its token (shown once) and start the runner container with it. Until a runner is online, the runs Kera queues fail after 10 minutes.
        </Empty>
      ) : (
        <>
          <NoRunnerHint runners={runners} />
          <Card>
            <RunnerTable runners={runners} onRotate={(r) => setAsk({ kind: 'rotate', runner: r })} onDelete={(r) => setAsk({ kind: 'delete', runner: r })} />
          </Card>
        </>
      )}

      {creating && (
        <NewRunnerDialog
          onClose={() => setCreating(false)}
          onCreated={(name, token) => {
            setCreating(false)
            setShown({ name, token })
          }}
        />
      )}
      {shown && <RunnerTokenDialog name={shown.name} token={shown.token} onClose={() => setShown(null)} />}
      <Confirm
        open={ask?.kind === 'rotate'}
        title={`Rotate the token of ${ask?.runner.name ?? ''}?`}
        confirmLabel="Rotate"
        body="The current token stops working at once: the runner is refused until it is given the new one, which is shown once."
        onConfirm={async () => {
          if (ask) setShown({ name: ask.runner.name, token: (await pa.rotateRunner(ask.runner.id)).token })
        }}
        onClose={() => setAsk(null)}
      />
      <Confirm
        open={ask?.kind === 'delete'}
        title={`Delete ${ask?.runner.name ?? 'runner'}?`}
        body="Its token stops working at once. Runs it already claimed can no longer be reported by it."
        onConfirm={async () => void (ask && (await pa.deleteRunner(ask.runner.id)))}
        onClose={() => setAsk(null)}
      />
    </Page>
  )
}
