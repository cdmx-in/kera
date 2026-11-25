// One section of the review: what was chosen, and a way back to change it.
import type { ReactNode } from 'react'
import { Pencil } from 'lucide-react'

export function ReviewCard({ title, onEdit, children, testId }: { title: string; onEdit?: () => void; children: ReactNode; testId: string }) {
  return (
    <section data-testid={testId} aria-label={title} className="rounded-2xl border border-line bg-surface p-4">
      <div className="flex items-center gap-2">
        <h3 className="flex-1 text-xs font-semibold uppercase tracking-wide text-muted">{title}</h3>
        {onEdit && (
          <button type="button" onClick={onEdit} aria-label={`Edit ${title.toLowerCase()}`} data-testid={`edit-${testId}`} className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-link hover:bg-surface-2">
            <Pencil size={12} aria-hidden /> Edit
          </button>
        )}
      </div>
      <div className="mt-2 min-w-0 text-sm">{children}</div>
    </section>
  )
}
