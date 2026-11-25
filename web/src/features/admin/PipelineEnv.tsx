// Pipeline > Environment: the variables a Kera-run script gets. A secret is write-only: the server never
// sends its value, so this page can only say that it is set and replace it.
import { useState } from 'react'
import { Lock, Pencil, Plus, Trash2 } from 'lucide-react'
import { ApiError } from '@/api/client'
import { toast } from '@/stores/toast'
import { Badge, Btn, Card, Confirm, inputCls, Loading, tdCls, thCls } from './ui'
import { relative, when } from './settings/Integrations'
import { useEnv, usePipelineApi, type EnvVar } from './pipelines'

const errText = (e: unknown) => (e as Error).message

/** One variable. A secret shows "set" and an Update action that asks for a new value; its stored value is never rendered, whatever the payload holds. */
export function EnvRow({ v, onSave, onDelete }: { v: EnvVar; onSave: (value: string) => Promise<void>; onDelete: () => void }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const open = () => {
    setValue(v.secret ? '' : (v.value ?? ''))
    setError('')
    setEditing(true)
  }
  const save = async () => {
    setBusy(true)
    try {
      await onSave(value)
      setEditing(false)
    } catch (e) {
      setError(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <tr data-testid={`env-${v.name}`} data-secret={v.secret} className="align-top">
      <td className={tdCls}>
        <code className="break-all text-xs">{v.name}</code>
      </td>
      <td className={tdCls + ' min-w-[12rem]'}>
        {editing ? (
          <div className="space-y-1">
            <div className="flex flex-wrap gap-2">
              <input
                type={v.secret ? 'password' : 'text'}
                autoComplete={v.secret ? 'new-password' : 'off'}
                aria-label={v.secret ? `New value of ${v.name}` : `Value of ${v.name}`}
                value={value}
                autoFocus
                onChange={(e) => setValue(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && void save()}
                className={inputCls + ' flex-1'}
              />
              <Btn variant="primary" disabled={busy} onClick={() => void save()}>
                Save
              </Btn>
              <Btn disabled={busy} onClick={() => setEditing(false)}>
                Cancel
              </Btn>
            </div>
            {error && (
              <p role="alert" className="text-xs text-red-600 dark:text-red-400">
                {error}
              </p>
            )}
          </div>
        ) : v.secret ? (
          <span className="inline-flex items-center gap-1.5 text-muted">
            <Lock size={12} aria-hidden /> set
          </span>
        ) : v.value ? (
          <code className="whitespace-pre-wrap break-all text-xs">{v.value}</code>
        ) : (
          <span className="text-muted">(empty)</span>
        )}
      </td>
      <td className={tdCls}>{v.secret ? <Badge tone="amber">Secret</Badge> : <Badge>Plain</Badge>}</td>
      <td className={tdCls + ' hidden whitespace-nowrap text-xs text-muted sm:table-cell'} title={when(v.updated_at)}>
        {relative(v.updated_at)}
      </td>
      <td className={tdCls + ' whitespace-nowrap text-right'}>
        {!editing && (
          <Btn variant="ghost" className="px-2 py-1" aria-label={v.secret ? `Update ${v.name}` : `Edit ${v.name}`} title={v.secret ? 'Replace the value' : 'Edit'} onClick={open}>
            <Pencil size={14} aria-hidden />
            <span className="text-xs">{v.secret ? 'Update' : 'Edit'}</span>
          </Btn>
        )}
        <Btn variant="ghost" className="px-2 py-1 text-red-600 dark:text-red-400" aria-label={`Delete ${v.name}`} title="Delete" onClick={onDelete}>
          <Trash2 size={14} aria-hidden />
        </Btn>
      </td>
    </tr>
  )
}

/** Name, value and a Secret box (on by default). Saving a name that exists replaces its value. */
export function AddEnv({ existing, onAdd }: { existing: string[]; onAdd: (v: { name: string; value: string; secret: boolean }) => Promise<void> }) {
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [secret, setSecret] = useState(true)
  const [errors, setErrors] = useState<{ name?: string; value?: string }>({})
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    try {
      await onAdd({ name: name.trim(), value, secret })
      setName('')
      setValue('')
      setErrors({})
    } catch (e) {
      // the server names the field at fault ("The runner sets this one itself")
      if (e instanceof ApiError && (e.field === 'name' || e.field === 'value')) setErrors({ [e.field]: e.message })
      else toast(errText(e), 'error')
    } finally {
      setBusy(false)
    }
  }
  const replaces = existing.includes(name.trim())
  return (
    <form
      noValidate
      aria-label="Add variable"
      className="grid gap-3 border-t border-line p-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <label className="block space-y-1">
        <span className="block text-xs font-medium text-muted">Name</span>
        <input
          name="name"
          value={name}
          autoComplete="off"
          spellCheck={false}
          placeholder="API_TOKEN"
          aria-invalid={!!errors.name}
          onChange={(e) => (setName(e.target.value), setErrors((x) => ({ ...x, name: undefined })))}
          className={inputCls + (errors.name ? ' border-red-500! focus:border-red-500!' : '')}
        />
        {errors.name ? (
          <span role="alert" className="block text-xs text-red-600 dark:text-red-400">
            {errors.name}
          </span>
        ) : (
          <span className="block text-xs text-muted">{replaces ? 'This replaces the value it has now.' : 'KERA_URL, KERA_PIPELINE, KERA_PIPELINE_TOKEN, KERA_RUN, KERA_MARK, KERA_SCRIPT_DIR and KERA_TEST are set by the runner.'}</span>
        )}
      </label>
      <label className="block space-y-1">
        <span className="block text-xs font-medium text-muted">Value</span>
        <input
          name="value"
          type={secret ? 'password' : 'text'}
          value={value}
          autoComplete={secret ? 'new-password' : 'off'}
          spellCheck={false}
          aria-invalid={!!errors.value}
          onChange={(e) => (setValue(e.target.value), setErrors((x) => ({ ...x, value: undefined })))}
          className={inputCls + (errors.value ? ' border-red-500! focus:border-red-500!' : '')}
        />
        {errors.value && (
          <span role="alert" className="block text-xs text-red-600 dark:text-red-400">
            {errors.value}
          </span>
        )}
      </label>
      <div className="flex flex-wrap items-end gap-3 sm:flex-col sm:items-stretch sm:justify-end">
        <label className="inline-flex items-center gap-2 text-sm">
          <input type="checkbox" checked={secret} onChange={(e) => setSecret(e.target.checked)} className="size-4 accent-[var(--accent)]" />
          Secret
        </label>
        <Btn type="submit" variant="primary" disabled={busy || !name.trim()}>
          <Plus size={14} aria-hidden /> Add variable
        </Btn>
      </div>
    </form>
  )
}

export function EnvTab({ name }: { name: string }) {
  const q = useEnv(name)
  const pa = usePipelineApi()
  const [del, setDel] = useState<string | null>(null)
  const env = q.data?.env
  return (
    <>
      <Card title="Environment" hint="The script gets these as environment variables. A secret is stored encrypted and never shown again; the runner hides secret values in the logs.">
        {!env ? (
          <Loading q={q} />
        ) : (
          <>
            {env.length === 0 ? (
              <p className="px-5 py-4 text-sm text-muted">No variables yet.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm" data-testid="env-table">
                  <thead>
                    <tr>
                      <th className={thCls}>Name</th>
                      <th className={thCls}>Value</th>
                      <th className={thCls}>Kind</th>
                      <th className={thCls + ' hidden sm:table-cell'}>Updated</th>
                      <th className={thCls + ' w-px'}>
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {env.map((v) => (
                      <EnvRow key={v.name} v={v} onSave={async (value) => void (await pa.setEnv(name, { name: v.name, value, secret: v.secret }))} onDelete={() => setDel(v.name)} />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <AddEnv existing={env.map((v) => v.name)} onAdd={async (v) => void (await pa.setEnv(name, v))} />
          </>
        )}
      </Card>
      <Confirm
        open={del !== null}
        title={`Delete ${del ?? 'variable'}?`}
        body="Runs that start after this no longer get it. A run that is going keeps what it was given."
        onConfirm={async () => void (del && (await pa.deleteEnv(name, del)))}
        onClose={() => setDel(null)}
      />
    </>
  )
}
