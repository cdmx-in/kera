import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowUp, FileUp, Library, Loader2, Plus, Square, Wrench, X } from 'lucide-react'
import { useModels, useTools } from '@/api/queries'
import type { FileRef } from '@/api/types'
import { useComposer } from '@/stores/composer'
import { useChat } from '@/stores/chat'
import { iconFor } from '@/lib/icons'
import { cx, storageGet, storageSet } from '@/lib/format'
import { Menu, MenuCheckItem, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuSub, MenuTrigger } from '@/ui/Menu'
import { IntegrationSlots, hasSlot } from '@/features/integrations/registry'
import { toolKey } from '@/features/tools/ids'
import { uiConfig, type Integration } from '@/lib/ui-config'
import { ExtBadge } from '../messages/FileChip'
import { useCollections, usePrompts, type Prompt } from '@/features/workspace/api'
import { stopStream } from '../stream'
import { MicButton } from './MicButton'
import { filterPrompts, slashQuery } from './slash'
import { useUploads } from './uploads'

const plusIntegration = (i: Integration) => i.kind === 'oauthToolServer' && i.showInPlusMenu !== false

/** `variant="task"` is the Cowork goal box: its own draft, wording and test ids. `initialText` (?q=) wins over a saved draft. */
export function Composer({ onSend, disabled, variant, initialText, sendLabel }: { onSend: (text: string, files: FileRef[]) => Promise<boolean>; disabled?: boolean; variant?: 'task'; initialText?: string; sendLabel?: string }) {
  const chatId = useChat((s) => s.chatId)
  const streaming = useChat((s) => s.streamingId !== null)
  const task = variant === 'task'
  const draftKey = task ? 'draft:task' : `draft:${chatId ?? 'new'}`
  const [text, setText] = useState(() => initialText || (storageGet('session', draftKey) ?? ''))
  const ta = useRef<HTMLTextAreaElement>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const uploads = useUploads()
  const [collections, setCollections] = useState<FileRef[]>([]) // knowledge collections picked for the next message
  const [dragging, setDragging] = useState(false)
  const [slashClosed, setSlashClosed] = useState(false) // Escape hides the prompt list until the next edit
  const [active, setActive] = useState(0)

  // Switching chats loads that chat's draft; typing saves it (sessionStorage draft:<chatId|new>).
  useEffect(() => {
    setText(initialText || (storageGet('session', draftKey) ?? ''))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draftKey])
  const textRef = useRef(text) // the latest draft, for dictation results that arrive between renders
  textRef.current = text
  const edit = (v: string) => {
    textRef.current = v
    setText(v)
    storageSet('session', draftKey, v)
    setSlashClosed(false)
  }

  // "/command" alone in the box lists saved prompts; picking one replaces the draft.
  const slash = slashQuery(text)
  const { data: prompts } = usePrompts(slash !== null)
  const matches = slash !== null && !slashClosed ? filterPrompts(prompts ?? [], slash) : []
  const open = matches.length > 0
  const sel = Math.min(active, matches.length - 1)
  const pick = (p: Prompt) => {
    edit(p.content)
    setActive(0)
    ta.current?.focus()
  }

  useLayoutEffect(() => {
    const el = ta.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, window.innerHeight * 0.4)}px`
  }, [text])

  useEffect(() => {
    if (!disabled && window.matchMedia?.('(pointer: fine)').matches) ta.current?.focus()
  }, [chatId, disabled])

  const canSend = !disabled && !streaming && !uploads.busy && (text.trim() !== '' || uploads.ready.length > 0 || collections.length > 0)

  const send = async () => {
    if (!canSend) return
    const body = text.trim()
    const files = [...uploads.ready, ...collections]
    setText('')
    storageSet('session', draftKey, null)
    uploads.clear()
    setCollections([])
    const ok = await onSend(body, files)
    if (!ok) edit(body)
  }

  return (
    <div
      className={cx('rounded-3xl border bg-surface p-2 shadow-pop transition-colors focus-within:border-accent/60', dragging ? 'border-accent' : 'border-line')}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes('Files')) {
          e.preventDefault()
          setDragging(true)
        }
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => {
        setDragging(false)
        if (e.dataTransfer.files.length) {
          e.preventDefault()
          uploads.add([...e.dataTransfer.files])
        }
      }}
    >
      {uploads.items.length + collections.length > 0 && (
        <ul className="flex flex-wrap gap-2 px-1 pb-2" aria-label="Attachments">
          {collections.map((c) => (
            <li key={c.id} data-testid="collection-chip" data-name={c.name} className="flex max-w-56 items-center gap-2 rounded-xl border border-line p-1.5 pr-1">
              <span className="flex h-8 w-9 shrink-0 items-center justify-center rounded-md bg-accent text-white">
                <Library size={16} />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm">{c.name}</span>
                <span className="block text-xs text-muted">Knowledge</span>
              </span>
              <button type="button" aria-label={`Remove ${c.name}`} onClick={() => setCollections((xs) => xs.filter((x) => x.id !== c.id))} className="grid size-6 place-items-center rounded-md text-muted hover:bg-surface-2">
                <X size={13} />
              </button>
            </li>
          ))}
          {uploads.items.map((a) => (
            <li key={a.key} data-testid="attachment" data-status={a.status} data-name={a.name} title={a.error} className={cx('flex max-w-56 items-center gap-2 rounded-xl border p-1.5 pr-1', a.status === 'failed' ? 'border-red-500/50' : 'border-line')}>
              {a.preview ? <img src={a.preview} alt="" className="size-8 rounded-md object-cover" /> : <ExtBadge name={a.name} />}
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm">{a.name}</span>
                <span className={cx('block text-xs', a.status === 'failed' ? 'text-red-600' : 'text-muted')}>
                  {a.status === 'uploading' ? 'Uploading…' : a.status === 'processing' ? 'Processing…' : a.status === 'failed' ? (a.error ?? 'Failed') : 'Ready'}
                </span>
              </span>
              {(a.status === 'uploading' || a.status === 'processing') && <Loader2 size={14} className="animate-spin text-muted" />}
              <button type="button" aria-label={`Remove ${a.name}`} onClick={() => uploads.remove(a.key)} className="grid size-6 place-items-center rounded-md text-muted hover:bg-surface-2">
                <X size={13} />
              </button>
            </li>
          ))}
        </ul>
      )}
      {open && (
        <ul id="slash-list" role="listbox" aria-label="Saved prompts" data-testid="slash-menu" className="mb-1 max-h-64 overflow-auto rounded-2xl border border-line bg-surface p-1">
          {matches.map((p, i) => (
            <li
              key={p.id}
              id={`slash-${p.id}`}
              role="option"
              aria-selected={i === sel}
              data-testid="slash-item"
              onMouseDown={(e) => (e.preventDefault(), pick(p))}
              onMouseEnter={() => setActive(i)}
              className={cx('flex cursor-pointer items-baseline gap-2 rounded-xl px-3 py-1.5 text-sm', i === sel && 'bg-surface-2')}
            >
              <span className="font-mono text-accent">/{p.command}</span>
              <span className="truncate text-muted">{p.title}</span>
            </li>
          ))}
        </ul>
      )}
      <textarea
        ref={ta}
        id="chat-input"
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={open ? 'slash-list' : undefined}
        aria-activedescendant={open ? `slash-${matches[sel].id}` : undefined}
        data-testid={task ? 'task-goal' : 'composer-input'}
        aria-label={task ? 'Goal' : 'Message'}
        rows={1}
        value={text}
        disabled={disabled}
        placeholder={task ? "Describe the goal. The assistant plans and works on it, and asks when it needs you." : 'How can I help?'}
        onChange={(e) => edit(e.target.value)}
        onPaste={(e) => {
          const files = [...e.clipboardData.files]
          if (files.length) {
            e.preventDefault()
            uploads.add(files)
          }
        }}
        onKeyDown={(e) => {
          if (open && !e.nativeEvent.isComposing) {
            const n = matches.length
            const keys: Record<string, () => void> = {
              ArrowDown: () => setActive((sel + 1) % n),
              ArrowUp: () => setActive((sel - 1 + n) % n),
              Enter: () => pick(matches[sel]),
              Tab: () => pick(matches[sel]),
              Escape: () => setSlashClosed(true),
            }
            if (keys[e.key] && !e.shiftKey) {
              e.preventDefault()
              keys[e.key]()
              return
            }
          }
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault()
            void send()
          } else if (e.key === 'Escape' && streaming) {
            e.preventDefault()
            void stopStream()
          }
        }}
        className="block max-h-[40vh] w-full resize-none bg-transparent px-3 py-2 focus-visible:outline-none text-[15px] leading-relaxed outline-none placeholder:text-muted"
      />
      <div className="flex items-center gap-1.5 px-1">
        <PlusMenu onFiles={() => fileInput.current?.click()} picked={collections} onPick={(c, on) => setCollections((xs) => (on ? [...xs.filter((x) => x.id !== c.id), c] : xs.filter((x) => x.id !== c.id)))} />
        <input ref={fileInput} type="file" multiple hidden onChange={(e) => (uploads.add([...(e.target.files ?? [])]), (e.target.value = ''))} />
        <FeaturePills />
        <div className="flex-1" />
        <MicButton onText={(t) => edit(textRef.current ? `${textRef.current.trimEnd()} ${t}` : t)} />
        {streaming ? (
          <button type="button" data-testid="stop" aria-label="Stop" onClick={() => void stopStream()} className="grid size-9 place-items-center rounded-full bg-fg text-bg">
            <Square size={14} fill="currentColor" />
          </button>
        ) : (
          <button type="button" data-testid={task ? 'start-task' : 'send'} aria-label={sendLabel ?? (task ? 'Start task' : 'Send')} title={sendLabel} disabled={!canSend} onClick={() => void send()} className="grid size-9 place-items-center rounded-full bg-accent text-white transition-[opacity,background-color] enabled:hover:bg-accent-bright disabled:opacity-35">
            <ArrowUp size={18} />
          </button>
        )}
      </div>
    </div>
  )
}

function PlusMenu({ onFiles, picked, onPick }: { onFiles: () => void; picked: FileRef[]; onPick: (c: FileRef, on: boolean) => void }) {
  const { data: models } = useModels()
  const { data: collections } = useCollections()
  // only collections that finished indexing something can answer
  const searchable = (collections ?? []).filter((c) => c.status.completed > 0)
  const { data: servers } = useTools()
  const model = useComposer((s) => s.model)
  const toolIds = useComposer((s) => s.toolIds)
  const toggle = useComposer((s) => s.toggleTool)
  const integrationIds = new Set(
    // oauth integrations are listed in their own slot
    uiConfig().integrations.flatMap((i) => (i.kind === 'oauthToolServer' ? [i.toolServerId] : [])),
  )
  const modelTools = (models?.find((m) => m.id === model)?.toolIds ?? []).map(toolKey)
  // servers the user can use now: builtin sets ride on the model, OAuth servers appear once connected
  const usable = (servers ?? []).filter((s) => s.kind !== 'builtin' && s.authenticated !== false).map((s) => s.id)
  const tools = [...new Set([...modelTools, ...usable])].filter((id) => !integrationIds.has(id))
  const label = (id: string) => servers?.find((s) => s.id === id)?.name ?? id.replace(/^(server|builtin):/, '')
  return (
    <Menu>
      <MenuTrigger data-testid="plus-menu" aria-label="Add files and tools" className="grid size-9 place-items-center rounded-full text-muted hover:bg-surface-2 hover:text-fg">
        <Plus size={20} />
      </MenuTrigger>
      <MenuContent side="top">
        <MenuItem onSelect={onFiles} testId="upload-files">
          <FileUp size={15} /> Upload files
        </MenuItem>
        {searchable.length > 0 && (
          <MenuSub label="Knowledge" icon={<Library size={15} />}>
            {searchable.map((c) => (
              <MenuCheckItem key={c.id} checked={picked.some((p) => p.id === c.id)} onChange={(v) => onPick({ id: c.id, name: c.name, type: 'collection' }, v)} testId={`knowledge-${c.id}`}>
                {c.name}
              </MenuCheckItem>
            ))}
          </MenuSub>
        )}
        {hasSlot('PlusMenuItem', plusIntegration) && (
          <>
            <MenuSeparator />
            <MenuLabel>Integrations</MenuLabel>
            <IntegrationSlots slot="PlusMenuItem" filter={plusIntegration} />
          </>
        )}
        {tools.length > 0 && (
          <>
            <MenuSeparator />
            <MenuSub label="Tools" icon={<Wrench size={15} />}>
              {tools.map((id) => (
                <MenuCheckItem key={id} checked={toolIds.includes(id)} onChange={(v) => toggle(id, v)} testId={`tool-${id}`}>
                  {label(id)}
                </MenuCheckItem>
              ))}
            </MenuSub>
          </>
        )}
      </MenuContent>
    </Menu>
  )
}

function FeaturePills() {
  const { data: models } = useModels()
  const model = useComposer((s) => s.model)
  const features = useComposer((s) => s.features)
  const setFeature = useComposer((s) => s.setFeature)
  const list = models?.find((m) => m.id === model)?.features ?? []
  return (
    <div className="flex min-w-0 gap-1.5 overflow-x-auto">
      {list.map((f) => {
        const on = !!features[f.id]
        const Icon = iconFor(f.icon)
        return (
          <button
            key={f.id}
            type="button"
            data-testid={`pill-${f.id}`}
            aria-pressed={on}
            onClick={() => setFeature(f.id, !on)}
            className={cx('inline-flex shrink-0 items-center gap-1.5 rounded-full border px-3 py-1 text-sm transition-colors', on ? 'border-accent bg-accent-soft text-fg' : 'border-line text-muted hover:bg-surface-2')}
          >
            <Icon size={14} />
            {f.label}
          </button>
        )
      })}
    </div>
  )
}
