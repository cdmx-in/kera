import { useEffect, useRef, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { cx } from '@/lib/format'

// Native <dialog showModal>: focus trap, Esc and top layer for free. Radix
// portals render outside the top layer, so dialogs avoid portal-based widgets.
export function Dialog(props: { open: boolean; onClose: () => void; label: string; className?: string; drawer?: boolean; testId?: string; persistent?: boolean; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null)
  // Backdrop closes only when the press also started on it (a menu item that
  // opens the dialog must not have its trailing click close it again).
  const downOnBackdrop = useRef(false)
  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (props.open && !d.open) d.showModal()
    else if (!props.open && d.open) d.close()
  }, [props.open])
  return (
    <dialog
      ref={ref}
      aria-label={props.label}
      data-testid={props.testId}
      className={cx('modal', props.drawer && 'drawer', props.className)}
      onClose={props.onClose}
      onCancel={(e) => props.persistent && e.preventDefault()}
      onPointerDown={(e) => (downOnBackdrop.current = e.target === ref.current)}
      onClick={(e) => {
        if (!props.persistent && downOnBackdrop.current && e.target === ref.current) props.onClose()
        downOnBackdrop.current = false
      }}
    >
      {props.open && props.children}
    </dialog>
  )
}

export function DialogHeader({ title, onClose, children }: { title: ReactNode; onClose: () => void; children?: ReactNode }) {
  return (
    <div className="flex items-center gap-2 border-b border-line px-4 py-3">
      <h2 className="min-w-0 flex-1 truncate text-base font-semibold">{title}</h2>
      {children}
      <IconButton label="Close" onClick={onClose}>
        <X size={18} />
      </IconButton>
    </div>
  )
}

export function IconButton({ label, className, children, ...rest }: React.ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button type="button" aria-label={label} title={label} className={cx('inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg disabled:opacity-40', className)} {...rest}>
      {children}
    </button>
  )
}
