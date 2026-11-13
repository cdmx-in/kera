import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ThumbsDown, ThumbsUp } from 'lucide-react'
import { api } from '@/api/client'
import type { Message } from '@/api/types'
import { useChat } from '@/stores/chat'
import { toast } from '@/stores/toast'
import { IconButton } from '@/ui/Dialog'

type Votes = Record<string, number>
const votesKey = (chatId: string) => ['votes', chatId]

/** Thumbs up/down on an answer in a saved chat; a down vote asks what went wrong (optional). */
export function Feedback({ m }: { m: Message }) {
  const chatId = useChat((s) => (s.temporary || s.chatId?.startsWith('temp:') ? null : s.chatId))
  const qc = useQueryClient()
  const { data: votes } = useQuery({
    queryKey: votesKey(chatId ?? ''),
    enabled: !!chatId,
    queryFn: () => api<Votes>(`/api/v1/evaluations/feedback/chat/${encodeURIComponent(chatId!)}`),
  })
  const [asking, setAsking] = useState(false)
  const [comment, setComment] = useState('')
  if (!chatId) return null
  const rating = votes?.[m.id]

  const send = async (r: 1 | -1, text = '') => {
    try {
      await api('/api/v1/evaluations/feedback', {
        method: 'POST',
        json: { type: 'rating', data: { rating: r, model_id: m.model ?? '', comment: text }, meta: { chat_id: chatId, message_id: m.id } },
      })
      qc.setQueryData<Votes>(votesKey(chatId), (v) => ({ ...v, [m.id]: r }))
      return true
    } catch {
      toast('Could not save your feedback', 'error')
      return false
    }
  }

  return (
    <>
      <IconButton label="Good answer" data-testid="vote-up" aria-pressed={rating === 1} className="size-7" onClick={() => void send(1).then(() => setAsking(false))}>
        <ThumbsUp size={14} fill={rating === 1 ? 'currentColor' : 'none'} />
      </IconButton>
      <IconButton label="Bad answer" data-testid="vote-down" aria-pressed={rating === -1} className="size-7" onClick={() => void send(-1).then((ok) => setAsking(ok))}>
        <ThumbsDown size={14} fill={rating === -1 ? 'currentColor' : 'none'} />
      </IconButton>
      {asking && (
        <form
          className="ml-1 flex items-center gap-1"
          onSubmit={(e) => {
            e.preventDefault()
            void send(-1, comment.trim()).then((ok) => ok && (setAsking(false), setComment(''), toast('Thanks for the feedback')))
          }}
        >
          <input
            autoFocus
            data-testid="vote-comment"
            aria-label="What went wrong? (optional)"
            placeholder="What went wrong? (optional)"
            maxLength={2000}
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            onKeyDown={(e) => e.key === 'Escape' && setAsking(false)}
            className="w-56 rounded-lg border border-line bg-surface px-2 py-1 text-xs outline-none focus:border-accent"
          />
          <button type="submit" className="rounded-lg px-2 py-1 text-xs text-accent hover:bg-surface-2">
            Send
          </button>
        </form>
      )}
    </>
  )
}
