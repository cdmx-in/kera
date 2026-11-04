import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowDown } from 'lucide-react'
import { useBranch, useChat } from '@/stores/chat'
import { MessageRow } from './MessageRow'

/** The active branch. Sticks to the bottom while streaming unless the user scrolls up. */
export function MessageList({ readOnly, modelName, footer }: { readOnly?: boolean; modelName?: (id?: string) => string; footer?: React.ReactNode }) {
  const ids = useBranch()
  const ref = useRef<HTMLDivElement>(null)
  const stick = useRef(true)
  const [away, setAway] = useState(false)
  const tick = useChat((s) => (s.currentId ? s.messages[s.currentId] : undefined))

  useLayoutEffect(() => {
    const el = ref.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [tick, ids.length])

  useEffect(() => {
    stick.current = true
    const el = ref.current
    if (el) el.scrollTop = el.scrollHeight
  }, [ids[0]])

  return (
    <div className="relative min-h-0 flex-1">
      <div
        ref={ref}
        className="h-full overflow-y-auto"
        onScroll={(e) => {
          const el = e.currentTarget
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
          stick.current = atBottom
          setAway(!atBottom)
        }}
      >
        <div className="mx-auto w-full max-w-3xl px-4 pb-6 pt-4">
          {ids.map((id, i) => (
            <MessageRow key={id} id={id} isLast={i === ids.length - 1} readOnly={readOnly} modelName={modelName} />
          ))}
          {footer}
        </div>
      </div>
      {away && (
        <button
          type="button"
          aria-label="Scroll to bottom"
          onClick={() => {
            const el = ref.current
            if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
          }}
          className="absolute bottom-3 left-1/2 grid size-8 -translate-x-1/2 place-items-center rounded-full border border-line bg-surface shadow-pop"
        >
          <ArrowDown size={16} />
        </button>
      )}
    </div>
  )
}
