// Pipeline > Script: the files a runner executes, as a small editor. Every save is a new version; the newest is live.
// The draft lives here until it is saved; leaving with unsaved changes asks first.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useBlocker } from 'react-router'
import { FilePlus, FileText, FlaskConical, History as HistoryIcon, Trash2 } from 'lucide-react'
import { ApiError } from '@/api/client'
import { cx } from '@/lib/format'
import { toast } from '@/stores/toast'
import { Badge, Btn, Card, Confirm, inputCls, Loading } from './ui'
import { CodeEditor } from './CodeEditor'
import { Notice } from './Notice'
import { useNotebookController } from './notebook'
import { CellBlock, NotebookToolbar, ProblemsPanel } from './PipelineNotebook'
import { NoRunnerHint } from './PipelineRunners'
import { relative, when } from './settings/Integrations'
import { bad, useFormErrors, VField } from './settings/dialogKit'
import {
  changedFiles,
  collapseDiff,
  diffLines,
  draftChanged,
  draftOf,
  fileNameError,
  startable,
  useRunners,
  usePipelineApi,
  useScript,
  useScriptVersion,
  validateScript,
  type DiffItem,
  type Draft,
  type Script,
  type ScriptVersion,
} from './pipelines'

const nameOf = (t: EventTarget) => (t as HTMLInputElement).name ?? ''

/** Asks before a navigation or a reload throws the draft away. Needs the app's data router, so it is kept out of the pieces that are rendered alone. */
function LeaveGuard({ when: dirty }: { when: boolean }) {
  const blocker = useBlocker(dirty)
  const proceeded = useRef(false)
  useEffect(() => {
    if (!dirty) return
    const h = (e: BeforeUnloadEvent) => e.preventDefault()
    addEventListener('beforeunload', h)
    return () => removeEventListener('beforeunload', h)
  }, [dirty])
  return (
    <Confirm
      open={blocker.state === 'blocked'}
      title="Leave without saving?"
      confirmLabel="Leave"
      body="The script has changes that are not saved as a version. Leave throws them away; Cancel keeps you here."
      onConfirm={() => {
        proceeded.current = true
        if (blocker.state === 'blocked') blocker.proceed()
      }}
      onClose={() => {
        if (!proceeded.current && blocker.state === 'blocked') blocker.reset()
        proceeded.current = false
      }}
    />
  )
}

/** A line diff: removed lines red with "-", added lines green with "+", long unchanged runs folded. The signs and screen reader words carry it, never colour alone. */
export function DiffView({ items }: { items: DiffItem[] }) {
  if (!items.some((i) => i.kind === 'add' || i.kind === 'del')) return <p className="px-4 py-3 text-sm text-muted">No changes.</p>
  return (
    <div role="group" aria-label="Changes" data-testid="diff" className="max-h-[24rem] overflow-auto py-1 font-mono text-xs leading-5">
      {items.map((l, i) =>
        l.kind === 'skip' ? (
          <div key={i} className="px-4 py-0.5 text-center text-muted">
            {l.count} unchanged lines
          </div>
        ) : (
          <div
            key={i}
            data-kind={l.kind}
            className={cx('flex px-2', l.kind === 'add' && 'bg-green-500/15 text-green-800 dark:text-green-300', l.kind === 'del' && 'bg-red-500/15 text-red-800 dark:text-red-300')}
          >
            <span aria-hidden className="w-4 shrink-0 select-none">
              {l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ' '}
            </span>
            {l.kind !== 'same' && <span className="sr-only">{l.kind === 'add' ? 'Added: ' : 'Removed: '}</span>}
            <span className="min-w-0 whitespace-pre-wrap break-all">{l.text || ' '}</span>
          </div>
        ),
      )}
    </div>
  )
}

/** The saved versions, newest first; picking one opens it read-only. */
export function VersionList({ versions, viewing, onView }: { versions: ScriptVersion[]; viewing: number | null; onView: (v: number | null) => void }) {
  if (!versions.length) return <p className="px-4 py-3 text-sm text-muted">Nothing saved yet.</p>
  return (
    <ul className="max-h-80 divide-y divide-line overflow-y-auto" data-testid="versions">
      {versions.map((v, i) => (
        <li key={v.version}>
          <button
            type="button"
            aria-pressed={viewing === v.version}
            onClick={() => onView(viewing === v.version ? null : v.version)}
            className={cx('block w-full space-y-0.5 px-4 py-2.5 text-left text-sm hover:bg-surface-2/60 aria-pressed:bg-accent-soft')}
          >
            <span className="flex flex-wrap items-center gap-1.5">
              <span className="font-medium">Version {v.version}</span>
              {i === 0 && <Badge tone="green">Live</Badge>}
            </span>
            <span className="block text-xs text-muted" title={when(v.created_at)}>
              {v.created_by || 'Unknown'} · {relative(v.created_at)}
            </span>
            {v.note && <span className="line-clamp-2 block break-words text-xs">{v.note}</span>}
          </button>
        </li>
      ))}
    </ul>
  )
}

function FileList({ names, active, entry, changed, readOnly, onPick, onDelete, onAdd }: { names: string[]; active: string; entry: string; changed: string[]; readOnly: boolean; onPick: (n: string) => void; onDelete: (n: string) => void; onAdd: (n: string) => string }) {
  const [fresh, setFresh] = useState('')
  const [error, setError] = useState('')
  return (
    <>
      {names.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted">No files yet.</p>
      ) : (
        <ul className="max-h-72 divide-y divide-line overflow-y-auto" aria-label="Script files">
          {names.map((n) => (
            <li key={n} className={cx('flex items-center', n === active && 'bg-accent-soft')}>
              <button type="button" aria-current={n === active ? 'true' : undefined} onClick={() => onPick(n)} className="flex min-w-0 flex-1 items-center gap-1.5 px-4 py-2 text-left hover:bg-surface-2/60">
                <FileText size={13} className="shrink-0 text-muted" aria-hidden />
                <span className="min-w-0 break-all font-mono text-xs">{n}</span>
                {n === entry && <Badge tone="accent">Entry</Badge>}
                {changed.includes(n) && (
                  <span role="img" className="text-amber-600 dark:text-amber-400" title="Not saved yet" aria-label="not saved yet">
                    ●
                  </span>
                )}
              </button>
              {!readOnly && (
                <Btn variant="ghost" className="mr-1 px-2 py-1 text-red-600 dark:text-red-400" aria-label={`Delete ${n}`} title="Delete from this draft" onClick={() => onDelete(n)}>
                  <Trash2 size={13} aria-hidden />
                </Btn>
              )}
            </li>
          ))}
        </ul>
      )}
      {!readOnly && (
        <form
          noValidate
          aria-label="Add file"
          className="space-y-1 border-t border-line p-3"
          onSubmit={(e) => {
            e.preventDefault()
            const msg = onAdd(fresh.trim())
            setError(msg)
            if (!msg) setFresh('')
          }}
        >
          <div className="flex gap-2">
            <input aria-label="New file name" aria-invalid={!!error} value={fresh} placeholder="scripts/a.py" autoComplete="off" spellCheck={false} onChange={(e) => (setFresh(e.target.value), setError(''))} className={inputCls + bad(error)} />
            <Btn type="submit" className="shrink-0" disabled={!fresh.trim()}>
              <FilePlus size={14} aria-hidden /> Add
            </Btn>
          </div>
          {error && (
            <p role="alert" className="text-xs text-red-600 dark:text-red-400">
              {error}
            </p>
          )}
        </form>
      )}
    </>
  )
}

/** /admin/pipelines/:name?tab=script */
export function ScriptTab({ name }: { name: string }) {
  const q = useScript(name)
  const runners = useRunners()
  const pa = usePipelineApi()
  const current: Script | null = q.data?.current ?? null
  const versions = q.data?.versions ?? []

  const [draft, setDraft] = useState<Draft>(() => draftOf(null))
  const [base, setBase] = useState<number>() // the version the draft started from
  const [viewing, setViewing] = useState<number | null>(null)
  const [active, setActive] = useState('')
  const [cmp, setCmp] = useState<number | null>(null)
  const [ask, setAsk] = useState(false)
  const [saving, setSaving] = useState(false)
  const dirty = draftChanged(current, draft)
  const form = useFormErrors(validateScript(draft))

  // a new live version (our own save, or another admin's) replaces a draft that has no changes of its own
  useEffect(() => {
    if (current && (base === undefined || !dirty)) {
      setDraft(draftOf(current))
      setBase(current.version)
    }
  }, [current?.version]) // eslint-disable-line react-hooks/exhaustive-deps

  const viewQ = useScriptVersion(name, viewing ?? undefined)
  const view = viewing != null ? viewQ.data : undefined
  const readOnly = viewing != null
  const shown: Pick<Draft, 'files' | 'entry' | 'args'> | undefined = readOnly ? view : draft
  const names = Object.keys(shown?.files ?? {}).sort()
  const file = names.includes(active) ? active : names.includes(shown?.entry ?? '') ? shown!.entry : (names[0] ?? '')
  const text = shown?.files[file] ?? ''

  // what the shown file is compared with: a chosen version, else the one before the version being viewed, else the live one
  const cmpV = cmp ?? (viewing != null ? viewing - 1 : current?.version)
  const cmpKnown = cmpV !== undefined && versions.some((v) => v.version === cmpV) && cmpV !== viewing
  const cmpQ = useScriptVersion(name, cmpKnown && cmpV !== current?.version ? cmpV : undefined)
  const against = cmpKnown ? (cmpV === current?.version ? current : cmpQ.data) : undefined
  const diff = useMemo(() => (against ? collapseDiff(diffLines(against.files[file] ?? '', text)) : null), [against, file, text])

  const open = (v: number | null) => (setViewing(v), setCmp(null))
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  // the entry as a notebook: only for a script that is being edited and that the runner can start
  const notebook = !readOnly && startable(draft.entry) && draft.entry in draft.files
  const nb = useNotebookController({
    name,
    files: draft.files,
    entry: draft.entry,
    file: readOnly ? '' : file,
    setEntryText: (t) => setDraft((d) => ({ ...d, files: { ...d.files, [d.entry]: t } })),
    setActive,
    online: runners.data ? runners.data.runners.some((r) => r.online) : undefined,
    enabled: notebook,
  })
  const addFile = (n: string): string => {
    const msg = fileNameError(n, Object.keys(draft.files))
    if (msg) return msg
    set({ files: { ...draft.files, [n]: '' }, entry: draft.entry || (startable(n) ? n : '') })
    setActive(n)
    form.touch('files')
    return ''
  }
  const deleteFile = (n: string) => {
    const files = { ...draft.files }
    delete files[n]
    set({ files, entry: draft.entry === n ? (Object.keys(files).find(startable) ?? '') : draft.entry })
    form.touch('files')
  }
  const store = async (body: Draft) => {
    const r = await pa.saveScript(name, { files: body.files, entry: body.entry, args: body.args.trim(), note: body.note.trim() })
    toast(`Saved as version ${r.version}. Runs that start now use it.`)
    return r
  }
  const save = async () => {
    if (!form.valid) return form.revealAll()
    setSaving(true)
    try {
      await store(draft)
    } catch (e) {
      if (e instanceof ApiError && e.field && ['files', 'entry', 'args'].includes(e.field)) form.setServer({ [e.field]: e.message })
      else toast((e as Error).message, 'error')
    } finally {
      setSaving(false)
    }
  }
  // Ctrl/Cmd+S saves the script, wherever the focus is on this tab
  const saveRef = useRef(save)
  saveRef.current = save
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 's') {
        e.preventDefault()
        if (!readOnly) void saveRef.current()
      }
    }
    addEventListener('keydown', h)
    return () => removeEventListener('keydown', h)
  }, [readOnly])
  const restore = async () => {
    if (!view) return
    const r = await store({ files: view.files, entry: view.entry, args: view.args, note: `Restored from version ${view.version}` })
    setDraft(draftOf(r)) // the editor now holds what is live; a draft that was open is dropped
    setBase(r.version)
    open(null)
  }
  const newer = base !== undefined && current && current.version > base && dirty

  if (!q.data) return <Loading q={q} />
  const entries = names.filter((n) => startable(n) || n === shown?.entry)
  return (
    <div className="space-y-4" data-testid="script-tab">
      <LeaveGuard when={dirty} />
      <NoRunnerHint runners={runners.data?.runners} />
      {newer && (
        <Notice tone="amber" role="status" testId="newer-version">
          Version {current.version} was saved by {current.created_by || 'someone else'} while you were editing. Saving now makes version {current.version + 1} from your draft and replaces it as the live one.
        </Notice>
      )}
      <div className="grid gap-4 lg:grid-cols-[16rem_minmax(0,1fr)] lg:items-start">
        <div className="space-y-4">
          <Card title="Files" hint="Add the files the script needs. The entry file is started.">
            <FileList names={names} active={file} entry={shown?.entry ?? ''} changed={readOnly ? [] : changedFiles(current, draft.files)} readOnly={readOnly} onPick={setActive} onDelete={deleteFile} onAdd={addFile} />
          </Card>
          <Card title="Versions" hint={current ? `Version ${current.version} is live. Pick one to read it.` : 'The first save is version 1.'}>
            <VersionList versions={versions} viewing={viewing} onView={open} />
          </Card>
        </div>

        <div className="min-w-0 space-y-4">
          {readOnly && (
            <Notice
              tone="accent"
              role="status"
              testId="viewing-banner"
              actions={
                <span className="flex flex-wrap gap-2">
                  <Btn onClick={() => setAsk(true)} disabled={!view} data-testid="restore">
                    <HistoryIcon size={14} aria-hidden /> Restore as new version
                  </Btn>
                  <Btn onClick={() => open(null)}>Back to editing</Btn>
                </span>
              }
            >
              Reading version {viewing}
              {view && ` (${view.created_by || 'unknown'}, ${relative(view.created_at)})`}; it cannot be edited here.
              {view?.note && <span className="mt-0.5 block break-words text-xs text-muted">{view.note}</span>}
            </Notice>
          )}
          {form.err('files') && (
            <p role="alert" className="rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
              {form.err('files')}
            </p>
          )}
          {notebook && (
            <NotebookToolbar
              session={nb.sess.session}
              noRunner={nb.sess.noRunner}
              starting={nb.sess.starting}
              error={nb.sess.error}
              real={nb.real}
              onReal={nb.setReal}
              runningAll={nb.runningAll}
              onStart={() => void nb.start()}
              onClose={() => void nb.sess.close()}
              onRunAll={nb.runAll}
              onStopAll={nb.stopAll}
              checking={nb.checks.checking}
              onCheck={() => void nb.checks.check()}
              raw={nb.raw}
              onRaw={nb.setRaw}
            />
          )}
          {notebook && nb.cellsVisible ? (
            <div className="space-y-3" data-testid="cells">
              {nb.nb.cells.map((c, i) => (
                <CellBlock
                  key={c.id}
                  cell={c}
                  index={i}
                  count={nb.nb.cells.length}
                  startLine={nb.starts[i].first}
                  step={nb.steps[i]}
                  problems={nb.byCell[i]}
                  run={nb.sess.runs[c.id]}
                  canMoveUp={nb.canMove(i, -1)}
                  canMoveDown={nb.canMove(i, 1)}
                  editorRef={(h) => (h ? nb.refs.cells.current.set(c.id, h) : nb.refs.cells.current.delete(c.id))}
                  titleRef={(el) => (el ? nb.refs.titles.current.set(c.id, el) : nb.refs.titles.current.delete(c.id))}
                  actions={{
                    onTitle: (v) => nb.edit(i, { title: v }),
                    onCode: (v) => nb.edit(i, { code: v, bare: false }),
                    onRun: () => nb.runCell(i),
                    onRunUpTo: () => nb.runUpTo(i),
                    onStop: () => void nb.sess.stop(c.id),
                    onDelete: () => nb.remove(i),
                    onAddBelow: () => nb.addBelow(i),
                    onMove: (by) => nb.move(i, by),
                    onClear: () => nb.sess.clearOutput(c.id),
                    onRunNext: () => nb.runNext(i),
                  }}
                />
              ))}
            </div>
          ) : (
            <Card title={file || 'Editor'} hint={file && file === shown?.entry ? 'The entry file: the runner starts this one.' : undefined}>
              {readOnly && !view ? (
                <Loading q={viewQ} />
              ) : file ? (
                <div className="p-3">
                  <CodeEditor
                    label={`Contents of ${file}`}
                    value={text}
                    readOnly={readOnly}
                    editorRef={readOnly ? undefined : nb.refs.plain}
                    marks={readOnly ? undefined : nb.marks}
                    onChange={(v) => set({ files: { ...draft.files, [file]: v } })}
                  />
                </div>
              ) : (
                <p className="px-5 py-8 text-center text-sm text-muted">Add a file to begin, for example <code>run.sh</code> or <code>main.py</code>.</p>
              )}
            </Card>
          )}

          <form
            noValidate
            aria-label="Script settings"
            onSubmit={(e) => {
              e.preventDefault()
              void save()
            }}
            onChange={(e) => form.touch(nameOf(e.target))}
            className="space-y-3 rounded-2xl border border-line bg-surface p-4"
          >
            <div className="grid gap-3 sm:grid-cols-2">
              <VField label="Entry file" hint="The file the runner starts: .sh or .bash with bash, .py with python3." error={form.err('entry')}>
                <select name="entry" value={shown?.entry ?? ''} disabled={readOnly} onChange={(e) => set({ entry: e.target.value })} className={inputCls + bad(form.err('entry'))}>
                  <option value="">{entries.length ? 'Choose the entry file' : 'Add a .sh or .py file first'}</option>
                  {entries.map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </select>
              </VField>
              <VField label="Arguments" hint="Added after the entry file, split on spaces. Optional." error={form.err('args')}>
                <input name="args" value={shown?.args ?? ''} readOnly={readOnly} autoComplete="off" spellCheck={false} placeholder="--full" onChange={(e) => set({ args: e.target.value })} className={inputCls + bad(form.err('args'))} />
              </VField>
            </div>
            {!readOnly && (
              <div className="flex flex-wrap items-end gap-3">
                <VField label="Note" hint="What changed. Shown in the version list. Optional." className="min-w-[14rem] flex-1">
                  <input name="note" value={draft.note} maxLength={300} autoComplete="off" onChange={(e) => set({ note: e.target.value })} className={inputCls} />
                </VField>
                {dirty && (
                  <Btn
                    onClick={() => {
                      setDraft(draftOf(current))
                      setBase(current?.version)
                    }}
                  >
                    Discard changes
                  </Btn>
                )}
                {notebook && (
                  <Btn onClick={nb.runAll} disabled={nb.raw || nb.runningAll} title="Run every cell in a test session. Nothing real changes." data-testid="test-run-all">
                    <FlaskConical size={14} aria-hidden /> Test (Run all)
                  </Btn>
                )}
                <Btn type="submit" variant="primary" disabled={saving || !dirty} data-testid="save-script" title="Save as a new version (Ctrl+S)">
                  Save as new version
                </Btn>
              </div>
            )}
            {!readOnly && notebook && (
              <p className="text-xs text-muted" data-testid="test-note">
                Test runs every cell in a session on a runner. Nothing real changes: uploads to collections are only planned, and workspace files stay untouched unless you chose the real workspace.
              </p>
            )}
            {!readOnly && dirty && !form.valid && form.hidden && <p className="text-xs text-muted">Fix the fields above before saving.</p>}
          </form>

          {notebook && <ProblemsPanel problems={nb.checks.problems} tools={nb.checks.tools} checked={nb.checks.checked} checking={nb.checks.checking} hint={nb.checks.hint} onPick={nb.pick} />}

          <Card
            title="Changes"
            hint={against ? `${file || 'The file'} compared with version ${against.version}.` : undefined}
            actions={
              cmpKnown || versions.length > 1 ? (
                <label className="flex items-center gap-2 whitespace-nowrap text-xs text-muted">
                  Compare with
                  <select value={cmpV ?? ''} onChange={(e) => setCmp(Number(e.target.value))} className={inputCls + ' w-auto'}>
                    {versions.filter((v) => v.version !== viewing).map((v) => (
                      <option key={v.version} value={v.version}>
                        Version {v.version}
                      </option>
                    ))}
                  </select>
                </label>
              ) : undefined
            }
          >
            {!file ? (
              <p className="px-4 py-3 text-sm text-muted">Pick a file to see what changed.</p>
            ) : !cmpKnown ? (
              <p className="px-4 py-3 text-sm text-muted">{versions.length ? 'There is no earlier version to compare with.' : 'Nothing to compare with until the first version is saved.'}</p>
            ) : diff ? (
              <DiffView items={diff} />
            ) : (
              <Loading q={cmpQ} />
            )}
          </Card>
        </div>
      </div>
      <Confirm open={ask} title={`Restore version ${viewing}?`} confirmLabel="Restore" body={dirty ? 'It is saved as a new version and becomes the live one. The unsaved changes in the editor are thrown away.' : 'It is saved as a new version and becomes the live one: runs that start after this use it.'} onConfirm={restore} onClose={() => setAsk(false)} />
    </div>
  )
}


export { CodeEditor }
