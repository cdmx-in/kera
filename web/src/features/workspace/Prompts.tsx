import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { useMe } from '@/api/queries'
import { cx } from '@/lib/format'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Spinner } from '@/ui/misc'
import { toast } from '@/stores/toast'
import { usePrompts, type Prompt } from './api'
import { AccessEditor, Badge, Field, btnPrimary, errorText, input } from './ui'

export function PromptsPage({ canCreate }: { canCreate: boolean }) {
  const { data: prompts, isLoading } = usePrompts()
  const { data: me } = useMe()
  const [editing, setEditing] = useState<Prompt | 'new' | null>(null)
  const [q, setQ] = useState('')
  const shown = (prompts ?? []).filter((p) => !q || `${p.command} ${p.title} ${p.content}`.toLowerCase().includes(q.toLowerCase()))
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <input className={cx(input, 'max-w-xs')} placeholder="Search prompts" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search prompts" />
        <p className="flex-1 text-sm text-muted">Type / and the command in the message box to insert a prompt.</p>
        {canCreate && (
          <button type="button" className={btnPrimary} onClick={() => setEditing('new')} data-testid="new-prompt">
            <Plus size={15} /> New prompt
          </button>
        )}
      </div>
      {isLoading && <Spinner />}
      {prompts && prompts.length === 0 && <p className="text-sm text-muted">No prompts yet.</p>}
      <ul className="divide-y divide-line rounded-xl border border-line">
        {shown.map((p) => (
          <li key={p.id}>
            <button type="button" className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-surface-2" onClick={() => setEditing(p)} data-testid="prompt-row">
              <code className="rounded bg-surface-2 px-1.5 py-0.5 text-xs">/{p.command}</code>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">{p.title}</span>
                <span className="block truncate text-xs text-muted">{p.content}</span>
              </span>
              {p.user_id !== me?.id && <Badge>shared</Badge>}
            </button>
          </li>
        ))}
      </ul>
      {editing && <PromptDialog prompt={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function PromptDialog({ prompt, onClose }: { prompt: Prompt | null; onClose: () => void }) {
  const qc = useQueryClient()
  const [command, setCommand] = useState(prompt?.command ?? '')
  const [title, setTitle] = useState(prompt?.title ?? '')
  const [content, setContent] = useState(prompt?.content ?? '')
  const [saving, setSaving] = useState(false)
  const canEdit = !prompt || prompt.write_access
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
    <Dialog open onClose={onClose} label={prompt ? 'Edit prompt' : 'New prompt'} className="w-[40rem]">
      <DialogHeader title={prompt ? `/${prompt.command}` : 'New prompt'} onClose={onClose}>
        {prompt && canEdit && (
          <IconButton label="Delete prompt" onClick={remove}>
            <Trash2 size={16} />
          </IconButton>
        )}
      </DialogHeader>
      <form
        className="max-h-[75dvh] space-y-3 overflow-y-auto p-4"
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Command" hint="Letters, digits, - and _.">
            <input className={input} value={command} disabled={!canEdit} onChange={(e) => setCommand(e.target.value.replace(/^\//, ''))} required data-testid="prompt-command" />
          </Field>
          <Field label="Title">
            <input className={input} value={title} disabled={!canEdit} onChange={(e) => setTitle(e.target.value)} />
          </Field>
        </div>
        <Field label="Prompt" hint="{{CLIPBOARD}} and the system prompt variables are filled in when the prompt is used.">
          <textarea className={cx(input, 'min-h-48')} value={content} disabled={!canEdit} onChange={(e) => setContent(e.target.value)} required data-testid="prompt-content" />
        </Field>
        {canEdit && (
          <div className="flex justify-end">
            <button type="submit" className={btnPrimary} disabled={saving || !command.trim() || !content.trim()} data-testid="save-prompt">
              Save
            </button>
          </div>
        )}
        {prompt && (
          <div className="space-y-2 border-t border-line pt-3">
            <h3 className="text-sm font-semibold">Access</h3>
            <AccessEditor url={`/api/v1/prompts/id/${prompt.id}/grants`} canEdit={canEdit} />
          </div>
        )}
      </form>
    </Dialog>
  )
}
