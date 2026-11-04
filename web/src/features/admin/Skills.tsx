// Admin > Skills: imported SKILL.md packs. Chats see only names and
// descriptions; the model loads a skill when it needs it.
import { useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Trash2, Upload } from 'lucide-react'
import { api, apiFetch } from '@/api/client'
import { Dialog, DialogHeader, IconButton } from '@/ui/Dialog'
import { Switch } from '@/ui/misc'
import { toast } from '@/stores/toast'
import { AccessEditor } from './Access'
import { enc } from './api'
import { attempt, Badge, Btn, Card, Check, Confirm, Loading, ManagedBadge, Page, tdCls, thCls } from './ui'

export interface SkillRow {
  id: string
  name: string
  description: string
  enabled: boolean
  lazy: boolean
  managed: boolean
  files: number
}

interface SkillFull extends Omit<SkillRow, 'files'> {
  content: string
  files: string[]
}

interface ImportResult {
  results: { name: string; outcome: 'created' | 'updated' | 'unchanged' }[]
  warnings: string[]
}

/** "3 created, 1 updated" from an import response. */
export function importSummary(r: ImportResult): string {
  const n = { created: 0, updated: 0, unchanged: 0 }
  for (const x of r.results) n[x.outcome]++
  const parts = (Object.keys(n) as (keyof typeof n)[]).filter((k) => n[k]).map((k) => `${n[k]} ${k}`)
  return (parts.join(', ') || 'Nothing imported') + (r.warnings.length ? ` (${r.warnings.length} files skipped)` : '')
}

const KEY = ['admin', 'skills']

export function SkillsPage() {
  const qc = useQueryClient()
  const skills = useQuery({ queryKey: KEY, queryFn: () => api<SkillRow[]>('/api/v1/skills/') })
  const [open, setOpen] = useState<SkillRow | null>(null)
  const [deleting, setDeleting] = useState<SkillRow | null>(null)
  const [busy, setBusy] = useState(false)
  const file = useRef<HTMLInputElement>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: KEY })
  const upload = async (f: File) => {
    setBusy(true)
    const fd = new FormData()
    fd.append('file', f, f.name)
    await attempt(async () => {
      const r = (await (await apiFetch('/api/v1/skills/import', { method: 'POST', body: fd })).json()) as ImportResult
      toast(importSummary(r))
      await refresh()
    })
    setBusy(false)
  }
  const patch = (s: SkillRow, body: Partial<Pick<SkillRow, 'enabled' | 'lazy'>>) =>
    attempt(async () => {
      await api(`/api/v1/skills/id/${enc(s.id)}/update`, { method: 'POST', json: body })
      await qc.invalidateQueries({ queryKey: ['admin', 'skill'] })
      await refresh()
    })
  return (
    <Page
      title="Skills"
      actions={
        <>
          <input
            ref={file}
            type="file"
            accept=".zip,application/zip"
            hidden
            onChange={(e) => {
              const f = e.target.files?.[0]
              e.target.value = ''
              if (f) void upload(f)
            }}
          />
          <Btn variant="primary" disabled={busy} onClick={() => file.current?.click()} data-testid="import-skills">
            <Upload size={15} /> Import zip
          </Btn>
        </>
      }
    >
      <p className="text-sm text-muted">
        A skill is a folder with a SKILL.md and optional files. Chats list the skills a user can use; the model loads one only when a task needs it. Without grants only admins
        can use a skill. Turn skills off for one model with its built-in tool “skills”.
      </p>
      <Loading q={skills} />
      {skills.data && !skills.data.length && <p className="text-sm text-muted">No skills yet. Import a .zip of skill folders.</p>}
      {!!skills.data?.length && (
        <Card className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-line">
                <th className={thCls}>Skill</th>
                <th className={thCls}>Files</th>
                <th className={thCls}>Enabled</th>
                <th className={thCls} />
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {skills.data.map((s) => (
                <tr key={s.id} data-testid="skill-row">
                  <td className={tdCls}>
                    <button type="button" className="block max-w-xl text-left" onClick={() => setOpen(s)}>
                      <span className="flex flex-wrap items-center gap-1.5 font-medium">
                        {s.name}
                        {s.managed && <ManagedBadge />}
                        {!s.lazy && <Badge tone="amber">always loaded</Badge>}
                      </span>
                      <span className="line-clamp-2 text-xs text-muted">{s.description || 'No description'}</span>
                    </button>
                  </td>
                  <td className={tdCls}>{s.files}</td>
                  <td className={tdCls}>
                    <Switch checked={s.enabled} label={`Enable ${s.name}`} onChange={(v) => void patch(s, { enabled: v })} />
                  </td>
                  <td className={tdCls + ' text-right'}>
                    <IconButton label={`Delete ${s.name}`} onClick={() => setDeleting(s)} className="hover:text-red-600">
                      <Trash2 size={15} />
                    </IconButton>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
      {open && <SkillDialog id={open.id} onClose={() => setOpen(null)} onPatch={(b) => patch(open, b)} />}
      <Confirm
        open={!!deleting}
        title="Delete skill"
        body={
          <p>
            Delete <b>{deleting?.name}</b>?{deleting?.managed && ' It comes back on the next kera apply unless the config file drops it.'}
          </p>
        }
        onClose={() => setDeleting(null)}
        onConfirm={async () => {
          await api(`/api/v1/skills/id/${enc(deleting!.id)}/delete`, { method: 'DELETE' })
          await refresh()
        }}
      />
    </Page>
  )
}

function SkillDialog({ id, onClose, onPatch }: { id: string; onClose: () => void; onPatch: (b: { lazy: boolean }) => Promise<boolean> }) {
  const q = useQuery({ queryKey: ['admin', 'skill', id], queryFn: () => api<SkillFull>(`/api/v1/skills/id/${enc(id)}`) })
  const [path, setPath] = useState<string | null>(null)
  const f = useQuery({
    queryKey: ['admin', 'skill', id, 'file', path],
    queryFn: () => api<{ content: string }>(`/api/v1/skills/id/${enc(id)}/file/${path!.split('/').map(enc).join('/')}`),
    enabled: !!path,
  })
  const s = q.data
  return (
    <Dialog open onClose={onClose} label="Skill" className="w-[min(56rem,calc(100vw-2rem))]" testId="skill-dialog">
      <DialogHeader title={s?.name ?? 'Skill'} onClose={onClose} />
      <div className="max-h-[80dvh] space-y-4 overflow-y-auto p-4">
        <Loading q={q} />
        {s && (
          <>
            <p className="text-sm text-muted">{s.description}</p>
            <Check checked={!s.lazy} onChange={(v) => void onPatch({ lazy: !v })}>
              Always loaded (the whole SKILL.md goes into every chat of users who can use it)
            </Check>
            <div className="flex flex-wrap gap-1.5">
              <Btn variant={path ? 'ghost' : 'secondary'} onClick={() => setPath(null)}>
                SKILL.md
              </Btn>
              {s.files.map((p) => (
                <Btn key={p} variant={path === p ? 'secondary' : 'ghost'} onClick={() => setPath(p)}>
                  {p}
                </Btn>
              ))}
            </div>
            {path && <Loading q={f} />}
            <pre className="max-h-[45dvh] overflow-auto whitespace-pre-wrap rounded-lg border border-line bg-surface-2 p-3 text-xs" data-testid="skill-content">
              {path ? f.data?.content : s.content}
            </pre>
            <div className="space-y-2 border-t border-line pt-3">
              <h3 className="text-sm font-semibold">Access</h3>
              <AccessEditor type="skill" id={s.id} hint={s.managed ? 'Saving replaces the grants from the config file too; the next kera apply adds them back.' : 'Admins can always use every skill.'} />
            </div>
          </>
        )}
      </div>
    </Dialog>
  )
}
