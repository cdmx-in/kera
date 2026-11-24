import { useState, type ReactNode, type Ref } from 'react'
import { Eye, EyeOff } from 'lucide-react'
import { cx } from '@/lib/format'

/**
 * A password box with a show/hide button. The button is a sibling of the input,
 * not inside the label, so the input's accessible name stays its label.
 * `show` and `onShowChange` let a parent reveal it (a generated password); without them it keeps its own state.
 */
export function PasswordInput({
  id,
  value,
  onChange,
  autoComplete,
  className,
  invalid,
  describedBy,
  inputRef,
  show,
  onShowChange,
  testId,
  ...rest
}: {
  id: string
  value: string
  onChange: (v: string) => void
  autoComplete: 'current-password' | 'new-password'
  className: string
  invalid?: boolean
  describedBy?: string
  inputRef?: Ref<HTMLInputElement>
  show?: boolean
  onShowChange?: (v: boolean) => void
  testId?: string
} & Omit<React.InputHTMLAttributes<HTMLInputElement>, 'id' | 'value' | 'onChange' | 'type' | 'className' | 'autoComplete'>) {
  const [own, setOwn] = useState(false)
  const shown = show ?? own
  const toggle = () => (onShowChange ?? setOwn)(!shown)
  return (
    <div className="relative">
      <input
        {...rest}
        id={id}
        ref={inputRef}
        type={shown ? 'text' : 'password'}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoComplete={autoComplete}
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        data-testid={testId}
        className={cx(className, 'pr-10')}
      />
      <button
        type="button"
        onClick={toggle}
        aria-label={shown ? 'Hide password' : 'Show password'}
        aria-pressed={shown}
        title={shown ? 'Hide password' : 'Show password'}
        className="absolute inset-y-0 right-0 grid w-10 place-items-center rounded-r-[inherit] text-muted hover:text-fg focus-visible:text-fg focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
      >
        {shown ? <EyeOff size={16} aria-hidden /> : <Eye size={16} aria-hidden />}
      </button>
    </div>
  )
}

/** The id of a LField's hint or error line, for the control's aria-describedby. */
export const noteId = (id: string) => `${id}-note`

/** Label, control, then an error (replacing the hint). The label points at the control by id. */
export function LField({ id, label, hint, error, action, children }: { id: string; label: ReactNode; hint?: ReactNode; error?: string; action?: ReactNode; children: ReactNode }) {
  const note = noteId(id)
  return (
    <div className="space-y-1">
      <div className="flex items-end justify-between gap-2">
        <label htmlFor={id} className="block text-xs font-medium text-muted">
          {label}
        </label>
        {action}
      </div>
      {children}
      {error ? (
        <p id={note} role="alert" className="text-xs text-red-600 dark:text-red-400" data-testid="field-error">
          {error}
        </p>
      ) : (
        hint && (
          <p id={note} className="text-xs text-muted">
            {hint}
          </p>
        )
      )}
    </div>
  )
}
