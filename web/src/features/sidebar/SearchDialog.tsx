import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { Command } from 'cmdk'
import { MessageSquare } from 'lucide-react'
import { api } from '@/api/client'
import type { ChatSummary } from '@/api/types'
import { useUI } from '@/stores/ui'
import { Dialog } from '@/ui/Dialog'
import { Spinner } from '@/ui/misc'

export function SearchDialog() {
  const open = useUI((s) => s.searchOpen)
  const setOpen = useUI((s) => s.setSearch)
  const [text, setText] = useState('')
  const [q, setQ] = useState('')
  const navigate = useNavigate()

  useEffect(() => {
    const t = setTimeout(() => setQ(text.trim()), 200)
    return () => clearTimeout(t)
  }, [text])

  // Ctrl/Cmd+K opens search anywhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [setOpen])

  const res = useQuery({
    queryKey: ['chats', 'search', q],
    queryFn: () => (q ? api<ChatSummary[]>(`/api/v1/chats/search?text=${encodeURIComponent(q)}`) : api<ChatSummary[]>('/api/v1/chats/?page=1')),
    enabled: open,
  })

  const close = () => {
    setOpen(false)
    setText('')
  }
  return (
    <Dialog open={open} onClose={close} label="Search chats" className="mt-[12vh] w-[min(36rem,calc(100vw-2rem))]">
      <Command shouldFilter={false} label="Search chats">
        <div className="flex items-center border-b border-line pr-3">
          <Command.Input value={text} onValueChange={setText} autoFocus placeholder="Search chats" className="w-full bg-transparent px-4 py-3 text-[15px] outline-none focus-visible:outline-none" />
          {res.isFetching && <Spinner className="text-muted" />}
        </div>
        <Command.List className="max-h-[50dvh] overflow-y-auto p-1.5">
          {res.isSuccess && !res.data.length && <Command.Empty className="px-3 py-6 text-center text-sm text-muted">No chats found</Command.Empty>}
          {(res.data ?? []).slice(0, 50).map((c) => (
            <Command.Item
              key={c.id}
              value={c.id}
              onSelect={() => {
                close()
                navigate(`/c/${c.id}`)
              }}
              className="flex cursor-default items-center gap-2.5 rounded-lg px-3 py-2 text-sm data-[selected=true]:bg-surface-2"
            >
              <MessageSquare size={15} className="shrink-0 text-muted" />
              <span className="truncate">{c.title || 'New Chat'}</span>
            </Command.Item>
          ))}
        </Command.List>
      </Command>
    </Dialog>
  )
}
