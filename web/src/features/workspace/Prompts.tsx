import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { FileText, MessageSquareText, Plus, SearchX, Trash2, UserCog, Users } from 'lucide-react'
import { api } from '@/api/client'
import { useMe } from '@/api/queries'
import { cx } from '@/lib/format'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { toast } from '@/stores/toast'
import { usePrompts, type Prompt } from './api'
import { filterPrompts } from './lib'
import { serverFieldError, validatePrompt, type Errors } from './validate'
import { AccessEditor, Chip, Empty, Field, GridSkeleton, SearchBox, TabBar, Toolbar, btnPrimary, cardLink, errorText, grid, input } from './ui'

export function PromptsPage({ canCreate }: { canCreate: boolean }) {
  const { data: prompts, isLoading, error } = usePrompts()
  const { data: me } = useMe()
  const [editing, setEditing] = useState<Prompt | 'new' | null>(null)
  const [q, setQ] = useState('')
  const shown = filterPrompts(prompts ?? [], q)
  const newButton = canCreate && (
    <button type="button" className={btnPrimary} onClick={() => setEditing('new')} data-testid="new-prompt">
      <Plus size={15} /> New prompt
    </button>
  )
  return (
    <div className="space-y-6">
      <Toolbar action={newButton}>
        <SearchBox value={q} onChange={setQ} label="Search prompts" />
        <p className="text-sm text-muted">
          Type <kbd className="rounded border border-line bg-surface-2 px-1 font-mono text-xs">/</kbd> and the command in the message box to insert a prompt.
        </p>
      </Toolbar>
      {isLoading && <GridSkeleton className={grid} />}
      {error && <p className="rounded-xl border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300">{errorText(error)}</p>}
      {prompts && prompts.length === 0 && (
        <Empty icon={MessageSquareText} title="No prompts yet" action={newButton}>
          Save prompts you use often and insert them with a slash command.
        </Empty>
      )}
      {prompts && prompts.length > 0 && shown.length === 0 && (
        <Empty icon={SearchX} title="No prompts match">
          Try another search.
        </Empty>
      )}
      {shown.length > 0 && (
        <ul className={grid}>
          {shown.map((p) => (
            <li key={p.id}>
              <button type="button" className={cardLink} onClick={() => setEditing(p)} data-testid="prompt-row">
                <span className="flex w-full items-center gap-2">
                  <code className="min-w-0 truncate rounded-lg bg-accent-soft px-2 py-0.5 font-mono text-sm font-medium text-accent dark:text-accent-bright">/{p.command}</code>
                  <span className="flex-1" />
                  {p.user_id !== me?.id && (
                    <Chip icon={Users} title="Shared with you">
                      shared
                    </Chip>
                  )}
                </span>
                <span className="mt-3 block w-full truncate font-semibold tracking-tight group-hover:text-accent dark:group-hover:text-accent-bright">{p.title || p.command}</span>
                <span className="mt-1 line-clamp-2 w-full whitespace-pre-line text-sm leading-relaxed text-muted">{p.content}</span>
                {p.updated_at > 0 && <span className="mt-auto block pt-3 text-xs text-muted/80">Updated {new Date(p.updated_at * 1000).toLocaleDateString()}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
      {editing && <PromptDialog prompt={editing === 'new' ? null : editing} mine={(prompts ?? []).filter((p) => p.user_id === me?.id)} onClose={() => setEditing(null)} />}
    </div>
  )
}

function PromptDialog({ prompt, mine, onClose }: { prompt: Prompt | null; mine: Prompt[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [command, setCommand] = useState(prompt?.command ?? '')
  const [title, setTitle] = useState(prompt?.title ?? '')
  const [content, setContent] = useState(prompt?.content ?? '')
  const [saving, setSaving] = useState(false)
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set(prompt ? ['command', 'content'] : []))
  const [serverErr, setServerErr] = useState<Errors>({})
  const [tab, setTab] = useState<'prompt' | 'access'>('prompt')
  const canEdit = !prompt || prompt.write_access
  const all: Errors = { ...validatePrompt({ id: prompt?.id, command, content }, mine), ...serverErr }
  const errors = Object.fromEntries(Object.entries(all).filter(([k]) => touched.has(k) || k in serverErr))
  const touch = (k: string) => {
    setTouched((t) => (t.has(k) ? t : new Set(t).add(k)))
    setServerErr(({ [k]: _, ...rest }) => rest)
  }
  const done = async () => {
    await qc.invalidateQueries({ queryKey: ['ws', 'prompts'] })
    onClose()
  }
  const save = async () => {
    setSaving(true)
    try {
      const body = { command, title, content }
      if (prompt) await api(`/api/v1/prompts/id/${prompt.id}/update`, { method: 'POST', json: body })
      else await api('/api/v1/prompts/create', { method: 'POST', json: body })
      await done()
    } catch (e) {
      const fe = serverFieldError(e)
      if (fe) setServerErr({ [fe[0]]: fe[1] })
      toast(errorText(e), 'error')
    } finally {
      setSaving(false)
    }
  }
  const remove = async () => {
    if (!prompt || !confirm(`Delete /${prompt.command}?`)) return
    try {
      await api(`/api/v1/prompts/id/${prompt.id}/delete`, { method: 'DELETE' })
      await done()
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }
  return (
    <Dialog open onClose={onClose} label={prompt ? 'Edit prompt' : 'New prompt'} className="w-[min(48rem,calc(100vw-2rem))]">
      <DialogHeader title={prompt ? `/${prompt.command}` : 'New prompt'} onClose={onClose}>
        {prompt && canEdit && (
          <IconButton label="Delete prompt" onClick={remove}>
            <Trash2 size={16} />
          </IconButton>
        )}
      </DialogHeader>
      {prompt && (
        <div className="border-b border-line px-4">
          <TabBar
            tabs={[
              { key: 'prompt', label: 'Prompt', icon: FileText, error: Object.keys(all).length > 0 },
              { key: 'access', label: 'Access', icon: UserCog },
            ]}
            value={tab}
            onChange={setTab}
            label="Prompt"
          />
        </div>
      )}
      {tab === 'prompt' ? (
        <form
          className="max-h-[75dvh] space-y-4 overflow-y-auto p-4"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
            <Field label="Command" hint="Letters, digits, - and _." error={errors.command}>
              <div className="relative">
                <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 font-mono text-sm text-muted">/</span>
                <input
                  className={cx(input, 'pl-6 font-mono')}
                  value={command}
                  disabled={!canEdit}
                  aria-invalid={!!errors.command}
                  onChange={(e) => (setCommand(e.target.value.replace(/^\//, '')), touch('command'))}
                  onBlur={() => touch('command')}
                  required
                  data-testid="prompt-command"
                />
              </div>
            </Field>
            <Field label="Title">
              <input className={input} value={title} disabled={!canEdit} onChange={(e) => setTitle(e.target.value)} />
            </Field>
          </div>
          <Field label="Prompt" hint="{{CLIPBOARD}} and the system prompt variables are filled in when the prompt is used." error={errors.content}>
            <textarea
              className={cx(input, 'min-h-64 resize-y font-mono text-[13px] leading-relaxed')}
              value={content}
              disabled={!canEdit}
              aria-invalid={!!errors.content}
              onChange={(e) => (setContent(e.target.value), touch('content'))}
              onBlur={() => touch('content')}
              required
              data-testid="prompt-content"
            />
          </Field>
          {canEdit && (
            <div className="flex justify-end">
              <button type="submit" className={btnPrimary} disabled={saving || Object.keys(all).length > 0} title={Object.values(all)[0]} data-testid="save-prompt">
                Save
              </button>
            </div>
          )}
        </form>
      ) : (
        prompt && (
          <div className="max-h-[75dvh] overflow-y-auto p-4">
            <AccessEditor url={`/api/v1/prompts/id/${prompt.id}/grants`} canEdit={canEdit} />
          </div>
        )
      )}
    </Dialog>
  )
}
