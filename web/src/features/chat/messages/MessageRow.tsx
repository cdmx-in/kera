import { memo, useEffect, useState } from 'react'
import { AlertTriangle, Check, ChevronLeft, ChevronRight, Copy, Pencil, RefreshCw } from 'lucide-react'
import type { Message } from '@/api/types'
import { useChat, useMessage, siblingsOf } from '@/stores/chat'
import { useUI } from '@/stores/ui'
import { copyText, cx } from '@/lib/format'
import { Spinner } from '@/ui/misc'
import { IconButton } from '@/ui/Dialog'
import { Markdown } from '../markdown/Markdown'
import { BlockView } from '../blocks/registry'
import { messageText } from '../reduce'
import { editMessage, regenerate, sendMessage, switchBranch } from '../actions'
import { Reasoning } from './Reasoning'
import { Steps } from './Steps'
import { Sources } from './Sources'
import { FileChip } from './FileChip'
import { Feedback } from './Feedback'

export const MessageRow = memo(function MessageRow({ id, isLast, readOnly, modelName }: { id: string; isLast: boolean; readOnly?: boolean; modelName?: (id?: string) => string }) {
  const m = useMessage(id)
  if (!m) return null
  return (
    <div data-testid="message" data-role={m.role} data-status={m.status} data-message-id={m.id} className="msg-row group/msg py-3">
      {m.role === 'user' ? <UserMessage m={m} readOnly={readOnly} /> : <AssistantMessage m={m} isLast={isLast} readOnly={readOnly} modelName={modelName} />}
    </div>
  )
})

function BranchArrows({ m }: { m: Message }) {
  const sibs = useChat((s) => siblingsOf(s.messages, m.id).join(','))
    .split(',')
    .filter(Boolean)
  if (sibs.length < 2) return null
  const i = sibs.indexOf(m.id)
  return (
    <span className="inline-flex items-center text-xs text-muted">
      <IconButton label="Previous version" data-testid="branch-prev" disabled={i <= 0} onClick={() => switchBranch(m.id, -1)} className="size-7">
        <ChevronLeft size={14} />
      </IconButton>
      {i + 1}/{sibs.length}
      <IconButton label="Next version" data-testid="branch-next" disabled={i >= sibs.length - 1} onClick={() => switchBranch(m.id, 1)} className="size-7">
        <ChevronRight size={14} />
      </IconButton>
    </span>
  )
}

function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <IconButton
      label="Copy"
      data-testid="copy-message"
      className="size-7"
      onClick={async () => {
        if (!(await copyText(text))) return
        setDone(true)
        setTimeout(() => setDone(false), 1500)
      }}
    >
      {done ? <Check size={14} /> : <Copy size={14} />}
    </IconButton>
  )
}

function UserMessage({ m, readOnly }: { m: Message; readOnly?: boolean }) {
  const [editing, setEditing] = useState(false)
  const text = messageText(m)
  const [draft, setDraft] = useState(text)
  const busy = useChat((s) => s.streamingId !== null)
  return (
    <div className="flex flex-col items-end gap-1.5">
      {m.files.length > 0 && (
        <div className="flex flex-wrap justify-end gap-2">
          {m.files.map((f) => (
            <FileChip key={f.id} file={f} />
          ))}
        </div>
      )}
      {editing ? (
        <form
          className="w-full max-w-[85%] rounded-2xl border border-line bg-surface p-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (!draft.trim()) return
            setEditing(false)
            editMessage(m.id, draft.trim())
          }}
        >
          <textarea value={draft} onChange={(e) => setDraft(e.target.value)} aria-label="Edit message" rows={3} autoFocus className="w-full resize-y bg-transparent p-1 text-sm outline-none" />
          <div className="flex justify-end gap-2">
            <button type="button" onClick={() => setEditing(false)} className="rounded-lg px-3 py-1 text-sm hover:bg-surface-2">
              Cancel
            </button>
            <button type="submit" className="rounded-lg bg-accent px-3 py-1 text-sm font-medium text-white">
              Send
            </button>
          </div>
        </form>
      ) : (
        <div data-testid="message-content" className="max-w-[85%] rounded-2xl bg-surface-2 px-4 py-2.5 text-[15px] leading-relaxed [overflow-wrap:anywhere]">
          <Markdown text={text} breaks className="md md-user" />
        </div>
      )}
      {!readOnly && !editing && (
        <div className="flex items-center opacity-100 transition-opacity md:opacity-0 md:group-hover/msg:opacity-100 md:focus-within:opacity-100">
          <BranchArrows m={m} />
          <CopyButton text={text} />
          <IconButton label="Edit" data-testid="edit-message" className="size-7" disabled={busy} onClick={() => (setDraft(text), setEditing(true))}>
            <Pencil size={14} />
          </IconButton>
        </div>
      )}
    </div>
  )
}

function AssistantMessage({ m, isLast, readOnly, modelName }: { m: Message; isLast: boolean; readOnly?: boolean; modelName?: (id?: string) => string }) {
  const showCompaction = useUI((s) => s.showCompaction)
  const busy = useChat((s) => s.streamingId !== null)
  const streaming = m.status === 'streaming'
  const text = messageText(m)
  return (
    <div className="min-w-0">
      {modelName && <div className="mb-1 text-xs font-medium text-muted">{modelName(m.model)}</div>}
      {m.parts.map((p, i) => {
        switch (p.type) {
          case 'text':
            return (
              <div key={i} data-testid="message-content">
                <Markdown text={p.text} sources={m.sources} live={streaming && i === m.parts.length - 1} />
              </div>
            )
          case 'reasoning':
            return <Reasoning key={i} part={p} />
          case 'steps':
            return <Steps key={i} steps={p.steps} />
          case 'block':
            return <BlockView key={i} block={p.block} />
          case 'compaction':
            if (p.state === 'running') return <StatusRow key={i} text="Summarising earlier messages…" />
            return showCompaction ? (
              <p key={i} className="my-2 inline-block rounded-full bg-surface-2 px-2.5 py-0.5 text-xs text-muted" title={p.summary}>
                Earlier messages summarised
              </p>
            ) : null
          default:
            return null
        }
      })}
      {m.statusLine && <StatusRow text={m.statusLine.description} />}
      {streaming && !m.parts.length && !m.statusLine && <StatusRow text="" />}
      {m.files.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-2">
          {m.files.map((f) => (
            <FileChip key={f.id} file={f} />
          ))}
        </div>
      )}
      <Sources sources={m.sources} />
      {m.status === 'error' && (
        <p role="alert" className="mt-2 flex items-start gap-2 rounded-xl border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-400">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          {m.error?.message ?? 'Something went wrong'}
        </p>
      )}
      {m.status === 'stopped' && <p className="mt-1 text-xs text-muted">Stopped</p>}
      {!readOnly && !streaming && (
        <div className={cx('mt-1 flex items-center transition-opacity', !isLast && 'md:opacity-0 md:group-hover/msg:opacity-100 md:focus-within:opacity-100')}>
          <BranchArrows m={m} />
          {text && <CopyButton text={text} />}
          <IconButton label="Regenerate" data-testid="regenerate" className="size-7" disabled={busy} onClick={() => regenerate(m.id)}>
            <RefreshCw size={14} />
          </IconButton>
          {m.status === 'done' && text && <Feedback m={m} />}
          {m.usage && (m.usage.prompt_tokens || m.usage.completion_tokens) ? (
            <span data-testid="usage" title="Tokens read / written for this answer" className="ml-2 text-xs text-muted tabular-nums">
              {(m.usage.prompt_tokens ?? 0).toLocaleString()} in · {(m.usage.completion_tokens ?? 0).toLocaleString()} out
            </span>
          ) : null}
        </div>
      )}
      {!readOnly && isLast && m.status === 'done' && m.followUps && m.followUps.length > 0 && (
        <div className="mt-3 flex flex-col items-start gap-1.5" aria-label="Suggested follow-ups">
          {m.followUps.map((f) => (
            <button key={f} type="button" data-testid="follow-up" disabled={busy} onClick={() => void sendMessage(f)} className="rounded-xl border border-line px-3 py-1.5 text-left text-sm hover:bg-surface-2">
              {f}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function StatusRow({ text }: { text: string }) {
  return (
    <p className="my-2 flex items-center gap-2 text-sm text-muted">
      {text ? (
        <>
          <Spinner size={13} className="text-accent" />
          <span className="truncate">{text}</span>
        </>
      ) : (
        <Whimsy />
      )}
    </p>
  )
}

const whimsy = ['🤔 Thinking', '🏊 Swimming through docs', '🧠 Pondering', '🔍 Digging around', '🧩 Connecting dots', '☕ Brewing an answer', '🪄 Conjuring', '🐙 Wrangling tentacles', '🚀 Warming up thrusters', '🧪 Mixing ideas', '🦆 Paddling furiously', '📚 Skimming the shelves']

// Shown before the first token: a random phrase that changes every couple of seconds.
function Whimsy() {
  const [i, setI] = useState(() => Math.floor(Math.random() * whimsy.length))
  useEffect(() => {
    const t = setInterval(() => setI((c) => (c + 1 + Math.floor(Math.random() * (whimsy.length - 1))) % whimsy.length), 2200)
    return () => clearInterval(t)
  }, [])
  return (
    <span key={i} className="animate-fade-up truncate" data-testid="whimsy">
      {whimsy[i]}
      <span className="whimsy-dots" />
    </span>
  )
}
