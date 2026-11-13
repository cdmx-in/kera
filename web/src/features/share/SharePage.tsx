// Lazy chunk: read-only view of a shared chat.
import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { useChat } from '@/stores/chat'
import { uiConfig } from '@/lib/ui-config'
import { LoadFailed, Spinner, isMissing } from '@/ui/misc'
import { MessageList } from '@/features/chat/messages/MessageList'
import { loadChat } from '@/features/chat/ChatPage'

export default function SharePage() {
  const { shareId = '' } = useParams()
  const [state, setState] = useState<'loading' | 'ready' | 'missing' | 'failed'>('loading')
  const title = useChat((s) => s.title)
  useEffect(() => {
    loadChat(shareId, `/api/v1/chats/share/${encodeURIComponent(shareId)}`)
      .then(() => setState('ready'))
      .catch((e) => setState(isMissing(e) ? 'missing' : 'failed'))
  }, [shareId])
  return (
    <div className="flex h-dvh flex-col">
      <header className="flex h-14 shrink-0 items-center gap-3 border-b border-line px-4">
        <Link to="/" className="font-semibold">
          {uiConfig().brand.name}
        </Link>
        <span className="truncate text-sm text-muted">{title}</span>
      </header>
      {state === 'loading' ? (
        <div className="grid flex-1 place-items-center text-muted">
          <Spinner size={20} />
        </div>
      ) : state === 'failed' ? (
        <LoadFailed what="shared chat" />
      ) : state === 'missing' ? (
        <p className="grid flex-1 place-items-center text-muted">This shared chat is not available.</p>
      ) : (
        <MessageList readOnly />
      )}
    </div>
  )
}
