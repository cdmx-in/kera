import { Select as RSelect } from 'radix-ui'
import { useCallback, useState, type ReactNode } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import { cx } from '@/lib/format'

// A styled listbox on Radix Select: native <select> option lists are drawn by the OS and cannot be themed.

export interface SelectOption {
  value: string
  label: string
  description?: ReactNode
  icon?: ReactNode
}

/** Radix forbids an item with value "": an empty option travels under this sentinel instead. */
export const EMPTY = '__kera_empty__'
/** The value handed to Radix: "" stays "" (shows the placeholder) unless an option really has value "". */
export const toRadix = (value: string, options: SelectOption[]) => (value === '' && options.some((o) => o.value === '') ? EMPTY : value)
export const fromRadix = (value: string) => (value === EMPTY ? '' : value)

const tones = {
  accent: 'border-accent/40 bg-accent-soft text-accent dark:text-accent-bright',
  grey: 'border-line bg-surface-2 text-fg',
}

export function Select({
  value,
  onChange,
  options,
  placeholder,
  disabled,
  required,
  size = 'md',
  variant = 'field',
  tone = 'grey',
  className,
  id,
  'aria-label': ariaLabel,
  ...data
}: {
  value: string
  onChange: (v: string) => void
  options: SelectOption[]
  placeholder?: string
  disabled?: boolean
  required?: boolean
  size?: 'sm' | 'md'
  variant?: 'field' | 'pill'
  /** Pill colour. */
  tone?: keyof typeof tones
  className?: string
  /** For a <label htmlFor>. */
  id?: string
  'aria-label'?: string
  [data: `data-${string}`]: string | undefined
}) {
  // Inside a modal <dialog> the list must portal into it: the dialog's top layer hides anything portalled to <body>.
  const [container, setContainer] = useState<HTMLElement | null>(null)
  const findDialog = useCallback((el: HTMLElement | null) => setContainer(el?.closest('dialog') ?? null), [])
  return (
    <RSelect.Root value={toRadix(value, options)} onValueChange={(v) => onChange(fromRadix(v))} disabled={disabled} required={required}>
      <RSelect.Trigger
        ref={findDialog}
        id={id}
        aria-label={ariaLabel}
        {...data}
        className={cx(
          'group inline-flex min-w-0 cursor-pointer items-center justify-between gap-1.5 text-left outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-60 focus-visible:ring-3 focus-visible:ring-accent-soft data-[placeholder]:text-muted',
          // A caller's width class (w-auto, w-40...) replaces the default full width; both together let w-full win and squash flex siblings.
          variant === 'field' && !/(^|\s)w-/.test(className ?? '') && 'w-full',
          variant === 'field' && 'rounded-lg border border-line bg-surface px-3 hover:border-accent/45 focus-visible:border-accent data-[state=open]:border-accent',
          variant === 'field' && (size === 'sm' ? 'py-1 text-sm' : 'py-1.5 text-sm'),
          variant === 'pill' && cx('rounded-full border pl-3 pr-2 font-medium hover:brightness-95 dark:hover:brightness-110', tones[tone], size === 'sm' ? 'py-0.5 text-xs' : 'py-1 text-sm'),
          className,
        )}
      >
        <span className="truncate">
          <RSelect.Value placeholder={placeholder} />
        </span>
        <RSelect.Icon className="shrink-0 opacity-60 transition-transform group-data-[state=open]:rotate-180">
          <ChevronDown size={size === 'sm' ? 13 : 15} />
        </RSelect.Icon>
      </RSelect.Trigger>
      <RSelect.Portal container={container ?? undefined}>
        <RSelect.Content
          position="popper"
          sideOffset={6}
          collisionPadding={8}
          className="z-50 min-w-[max(var(--radix-select-trigger-width),9rem)] max-w-[min(30rem,calc(100vw-1rem))] overflow-hidden rounded-xl border border-line bg-surface text-sm text-fg shadow-pop data-[state=open]:animate-pop-in"
        >
          <RSelect.Viewport className="max-h-[min(20rem,var(--radix-select-content-available-height))] overflow-y-auto p-1">
            {options.map((o) => (
              <RSelect.Item
                key={o.value}
                value={o.value === '' ? EMPTY : o.value}
                className="flex cursor-default select-none items-center gap-2 rounded-lg py-1.5 pl-2.5 pr-2 outline-none data-[disabled]:opacity-50 data-[highlighted]:bg-surface-2"
              >
                {o.icon && <span className="flex shrink-0 text-muted">{o.icon}</span>}
                <span className="min-w-0 flex-1">
                  <RSelect.ItemText>{o.label}</RSelect.ItemText>
                  {o.description && <span className="block text-xs text-muted">{o.description}</span>}
                </span>
                <span className="flex size-4 shrink-0 items-center justify-center">
                  <RSelect.ItemIndicator>
                    <Check size={14} className="text-accent dark:text-accent-bright" />
                  </RSelect.ItemIndicator>
                </span>
              </RSelect.Item>
            ))}
          </RSelect.Viewport>
        </RSelect.Content>
      </RSelect.Portal>
    </RSelect.Root>
  )
}
