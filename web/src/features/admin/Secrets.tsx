// Admin > Pipelines > Secrets: the central store of named secrets that pipeline variables can point at.
// A value is write-only: it goes up from a form field, the field is emptied as soon as the request is done, and
// no route or component ever shows it again (the list has names, descriptions and who uses them).
import { useState } from 'react'
import { Link } from 'react-router'
import { ChevronLeft, Dices, KeyRound, Pencil, Plus, RefreshCw, RotateCw, Trash2 } from 'lucide-react'
import { ApiError } from '@/api/client'
import { toast } from '@/stores/toast'
import { Empty } from '@/features/workspace/ui'
import { Badge, Btn, Card, Confirm, inputCls, Loading, Page, tdCls, thCls } from './ui'
import { pipelinesBase, pipelineUrl } from './Pipelines'
import { relative, when } from './settings/Integrations'
import { usePipelineApi, useSecrets, validateSecret, type Secret } from './pipelines'

const errText = (e: unknown) => (e as Error).message

/** 32 random bytes as 64 hex characters. */
export function randomSecret(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(32)), (b) => b.toString(16).padStart(2, '0')).join('')
}

/** Where a secret is used: one chip per pipeline and variable, each linking to that pipeline's Environment tab. */
export function UsedBy({ list }: { list: Secret['used_by'] }) {
  if (list.length === 0) return <span className="text-muted">Not used</span>
  return (
    <span className="flex flex-wrap gap-1">
      {list.map((u) => (
        <Link key={`${u.pipeline}/${u.variable}`} to={`${pipelineUrl(u.pipeline)}?tab=env`} data-testid="used-by">
          <Badge tone="accent">
            {u.pipeline} / {u.variable}
          </Badge>
        </Link>
      ))}
    </span>
  )
}

type Mode = 'value' | 'description' | null

/** One secret. "Update value" opens an empty password field: the stored value is never prefilled or shown. */
export function SecretRow({ s, onSave, onDelete }: { s: Secret; onSave: (body: { value?: string; description?: string }) => Promise<void>; onDelete: () => void }) {
  const [mode, setMode] = useState<Mode>(null)
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const open = (m: Exclude<Mode, null>) => {
    setText(m === 'description' ? s.description : '')
    setError('')
    setMode(m)
  }
  const save = async () => {
    if (mode === 'value' && text === '') return setError('A secret needs a value')
    setBusy(true)
    try {
      await onSave(mode === 'value' ? { value: text } : { description: text })
      setMode(null)
    } catch (e) {
      setError(errText(e))
    } finally {
      if (mode === 'value') setText('') // the typed value does not outlive the request
      setBusy(false)
    }
  }
  return (
    <tr data-testid={`secret-${s.name}`} className="align-top">
      <td className={tdCls}>
        <code className="break-all text-xs font-medium">{s.name}</code>
      </td>
      <td className={tdCls + ' min-w-[14rem]'}>
        {mode ? (
          <div className="space-y-1">
            <div className="flex flex-wrap gap-2">
              <input
                type={mode === 'value' ? 'password' : 'text'}
                autoComplete={mode === 'value' ? 'new-password' : 'off'}
                spellCheck={false}
                autoFocus
                aria-label={mode === 'value' ? `New value of ${s.name}` : `Description of ${s.name}`}
                value={text}
                onChange={(e) => setText(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && void save()}
                className={inputCls + ' flex-1'}
              />
              <Btn variant="primary" disabled={busy} onClick={() => void save()}>
                Save
              </Btn>
              <Btn
                disabled={busy}
                onClick={() => {
                  setText('')
                  setMode(null)
                }}
              >
                Cancel
              </Btn>
            </div>
            {mode === 'value' && <p className="text-xs text-muted">Every pipeline that uses this secret gets the new value from its next run.</p>}
            {error && (
              <p role="alert" className="text-xs text-red-600 dark:text-red-400">
                {error}
              </p>
            )}
          </div>
        ) : s.description ? (
          <span className="text-sm">{s.description}</span>
        ) : (
          <span className="text-muted">–</span>
        )}
      </td>
      <td className={tdCls}>
        <UsedBy list={s.used_by} />
      </td>
      <td className={tdCls + ' hidden whitespace-nowrap text-xs text-muted md:table-cell'} title={when(s.updated_at)}>
        {relative(s.updated_at)}
        {s.updated_by && <span className="block">{s.updated_by}</span>}
      </td>
      <td className={tdCls + ' hidden whitespace-nowrap text-xs text-muted sm:table-cell'} title={s.last_used_at ? when(s.last_used_at) : undefined}>
        {s.last_used_at ? relative(s.last_used_at) : 'Never'}
      </td>
      <td className={tdCls + ' whitespace-nowrap text-right'}>
        {!mode && (
          <>
            <Btn variant="ghost" className="px-2 py-1" aria-label={`Update value of ${s.name}`} title="Replace the value" onClick={() => open('value')}>
              <RotateCw size={14} aria-hidden />
              <span className="text-xs">Update value</span>
            </Btn>
            <Btn variant="ghost" className="px-2 py-1" aria-label={`Edit description of ${s.name}`} title="Edit the description" onClick={() => open('description')}>
              <Pencil size={14} aria-hidden />
            </Btn>
          </>
        )}
        <Btn variant="ghost" className="px-2 py-1 text-red-600 dark:text-red-400" aria-label={`Delete ${s.name}`} title="Delete" onClick={onDelete}>
          <Trash2 size={14} aria-hidden />
        </Btn>
      </td>
    </tr>
  )
}

/** Name, description and a write-only value. "Generate random" fills the field so it can be copied before saving; once saved the field is emptied. */
export function AddSecret({ onAdd }: { onAdd: (v: { name: string; description: string; value: string }) => Promise<void> }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [value, setValue] = useState('')
  const [shown, setShown] = useState(false) // a generated value is readable until it is saved
  const [errors, setErrors] = useState<{ name?: string; value?: string }>({})
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    const e = validateSecret({ name, value })
    if (e.name || e.value) return setErrors(e)
    setBusy(true)
    try {
      await onAdd({ name: name.trim(), description: description.trim(), value })
      setName('')
      setDescription('')
      setValue('')
      setShown(false)
      setErrors({})
    } catch (e) {
      if (e instanceof ApiError && (e.field === 'name' || e.field === 'value')) setErrors({ [e.field]: e.message })
      else toast(errText(e), 'error')
    } finally {
      setBusy(false)
    }
  }
  const err = (m?: string) =>
    m && (
      <span role="alert" className="block text-xs text-red-600 dark:text-red-400">
        {m}
      </span>
    )
  return (
    <form
      noValidate
      aria-label="Add secret"
      className="grid gap-3 border-t border-line p-4 sm:grid-cols-2"
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
          placeholder="shared-api-token"
          aria-invalid={!!errors.name}
          onChange={(e) => (setName(e.target.value), setErrors((x) => ({ ...x, name: undefined })))}
          className={inputCls + (errors.name ? ' border-red-500! focus:border-red-500!' : '')}
        />
        {err(errors.name) || <span className="block text-xs text-muted">Lowercase letters, digits, dashes, dots and underscores. This is what a pipeline variable points at.</span>}
      </label>
      <label className="block space-y-1">
        <span className="block text-xs font-medium text-muted">Description</span>
        <input name="description" value={description} autoComplete="off" placeholder="What it opens, who owns it" onChange={(e) => setDescription(e.target.value)} className={inputCls} />
      </label>
      <label className="block space-y-1 sm:col-span-2">
        <span className="block text-xs font-medium text-muted">Value</span>
        <span className="flex flex-wrap gap-2">
          <input
            name="value"
            type={shown ? 'text' : 'password'}
            value={value}
            autoComplete="new-password"
            spellCheck={false}
            aria-invalid={!!errors.value}
            onChange={(e) => (setValue(e.target.value), setShown(false), setErrors((x) => ({ ...x, value: undefined })))}
            className={inputCls + ' min-w-0 flex-1 font-mono' + (errors.value ? ' border-red-500! focus:border-red-500!' : '')}
          />
          <Btn
            type="button"
            onClick={() => {
              setValue(randomSecret())
              setShown(true)
              setErrors((x) => ({ ...x, value: undefined }))
            }}
          >
            <Dices size={14} aria-hidden /> Generate random
          </Btn>
        </span>
        {err(errors.value) || <span className="block text-xs text-muted">Write-only: it is stored encrypted and never shown again. A generated value is visible here until you save, so copy it if something else needs it.</span>}
      </label>
      <div className="sm:col-span-2">
        <Btn type="submit" variant="primary" disabled={busy || !name.trim()}>
          <Plus size={14} aria-hidden /> Add secret
        </Btn>
      </div>
    </form>
  )
}

/** /admin/pipelines?secrets */
export function SecretsPage() {
  const q = useSecrets()
  const pa = usePipelineApi()
  const [del, setDel] = useState<Secret | null>(null)
  const secrets = q.data?.secrets
  return (
    <Page>
      <Link to={pipelinesBase} className="inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
        <ChevronLeft size={14} aria-hidden /> Pipelines
      </Link>
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="flex-1 text-lg font-semibold">Secrets</h2>
        <Btn variant="ghost" onClick={() => void q.refetch()} aria-label="Refresh">
          <RefreshCw size={14} />
        </Btn>
      </div>
      <p className="-mt-3 max-w-3xl text-[13px] text-muted">
        A secret is stored once, encrypted, and a pipeline variable can point at it instead of keeping its own copy. Update the value here and every pipeline that uses it gets the new one from its next run. Values are never shown.
      </p>
      <Card title="Stored secrets">
        {!secrets ? (
          <Loading q={q} />
        ) : (
          <>
            {secrets.length === 0 ? (
              <Empty icon={KeyRound} title="No stored secrets yet">
                Add one below, then choose it as &quot;Stored secret&quot; on a pipeline&apos;s Environment tab.
              </Empty>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm" data-testid="secrets-table">
                  <thead>
                    <tr>
                      <th className={thCls}>Name</th>
                      <th className={thCls}>Description</th>
                      <th className={thCls}>Used by</th>
                      <th className={thCls + ' hidden md:table-cell'}>Updated</th>
                      <th className={thCls + ' hidden sm:table-cell'}>Last used</th>
                      <th className={thCls + ' w-px'}>
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {secrets.map((s) => (
                      <SecretRow key={s.name} s={s} onSave={async (body) => void (await pa.updateSecret(s.name, body))} onDelete={() => setDel(s)} />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <AddSecret onAdd={async (v) => void (await pa.createSecret(v))} />
          </>
        )}
      </Card>
      <Confirm
        open={del !== null}
        title={`Delete ${del?.name ?? 'secret'}?`}
        body={
          del && del.used_by.length > 0 ? (
            <div className="space-y-2">
              <p>It is still used by these variables. Remove or change them first; Kera refuses to delete it meanwhile.</p>
              <UsedBy list={del.used_by} />
            </div>
          ) : (
            'The stored value is erased. This cannot be undone.'
          )
        }
        onConfirm={async () => void (del && (await pa.deleteSecret(del.name)))}
        onClose={() => setDel(null)}
      />
    </Page>
  )
}
