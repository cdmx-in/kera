import { useState } from 'react'
import { Plus } from 'lucide-react'
import type { AdminUser } from './api'
import { useDebounced, UserAvatar, useUserSearch } from './Users'
import { inputCls } from './ui'

/** Search box that lists matching users; picking one calls onPick. Plain list (no portal) so it works inside dialogs. */
export function UserPicker({ onPick, exclude = [], placeholder = 'Add a user by name or email' }: { onPick: (u: AdminUser) => void; exclude?: string[]; placeholder?: string }) {
  const [q, setQ] = useState('')
  const query = useDebounced(q.trim())
  const res = useUserSearch(query)
  const hits = query ? (res.data?.users ?? []).filter((u) => !exclude.includes(u.id)).slice(0, 8) : []
  return (
    <div className="relative">
      <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={placeholder} aria-label={placeholder} className={inputCls} data-testid="user-picker" />
      {hits.length > 0 && (
        <ul className="absolute inset-x-0 top-full z-10 mt-1 max-h-64 overflow-y-auto rounded-lg border border-line bg-surface p-1 shadow-pop">
          {hits.map((u) => (
            <li key={u.id}>
              <button
                type="button"
                onClick={() => (onPick(u), setQ(''))}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-surface-2"
              >
                <UserAvatar id={u.id} size={22} />
                <span className="min-w-0 flex-1 truncate">
                  {u.name} <span className="text-muted">{u.email}</span>
                </span>
                <Plus size={14} className="text-muted" />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
