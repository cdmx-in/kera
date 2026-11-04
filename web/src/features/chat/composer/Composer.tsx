import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowUp, FileUp, Loader2, Plus, Square, Wrench, X } from 'lucide-react'
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
import { stopStream } from '../stream'
import { useUploads } from './uploads'

const plusIntegration = (i: Integration) => i.kind === 'oauthToolServer' && i.showInPlusMenu !== false

/** `variant="task"` is the Cowork goal box: its own draft, wording and test ids. `initialText` (?q=) wins over a saved draft. */
export function Composer({ onSend, disabled, variant, initialText }: { onSend: (text: string, files: FileRef[]) => Promise<boolean>; disabled?: boolean; variant?: 'task'; initialText?: string }) {
  const chatId = useChat((s) => s.chatId)
  const streaming = useChat((s) => s.streamingId !== null)
  const task = variant === 'task'
  const draftKey = task ? 'draft:task' : `draft:${chatId ?? 'new'}`
  const [text, setText] = useState(() => initialText || (storageGet('session', draftKey) ?? ''))
  const ta = useRef<HTMLTextAreaElement>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const uploads = useUploads()
  const [dragging, setDragging] = useState(false)

  // Switching chats loads that chat's draft; typing saves it (sessionStorage draft:<chatId|new>).
  useEffect(() => {
    setText(initialText || (storageGet('session', draftKey) ?? ''))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draftKey])
  const edit = (v: string) => {
    setText(v)
    storageSet('session', draftKey, v)
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

  const canSend = !disabled && !streaming && !uploads.busy && (text.trim() !== '' || uploads.ready.length > 0)

  const send = async () => {
    if (!canSend) return
    const body = text.trim()
    const files = uploads.ready
    setText('')
    storageSet('session', draftKey, null)
    uploads.clear()
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
      {uploads.items.length > 0 && (
        <ul className="flex flex-wrap gap-2 px-1 pb-2" aria-label="Attachments">
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
      <textarea
        ref={ta}
        id="chat-input"
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
        <PlusMenu onFiles={() => fileInput.current?.click()} />
        <input ref={fileInput} type="file" multiple hidden onChange={(e) => (uploads.add([...(e.target.files ?? [])]), (e.target.value = ''))} />
        <FeaturePills />
        <div className="flex-1" />
        {streaming ? (
          <button type="button" data-testid="stop" aria-label="Stop" onClick={() => void stopStream()} className="grid size-9 place-items-center rounded-full bg-fg text-bg">
            <Square size={14} fill="currentColor" />
          </button>
        ) : (
          <button type="button" data-testid={task ? 'start-task' : 'send'} aria-label={task ? 'Start task' : 'Send'} disabled={!canSend} onClick={() => void send()} className="grid size-9 place-items-center rounded-full bg-accent text-white transition-[opacity,background-color] enabled:hover:bg-accent-bright disabled:opacity-35">
            <ArrowUp size={18} />
          </button>
        )}
      </div>
    </div>
  )
}

function PlusMenu({ onFiles }: { onFiles: () => void }) {
  const { data: models } = useModels()
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
