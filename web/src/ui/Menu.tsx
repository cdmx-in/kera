import { DropdownMenu } from 'radix-ui'
import type { ReactNode } from 'react'
import { Check, ChevronRight } from 'lucide-react'

// Thin styling layer over Radix DropdownMenu.
export const Menu = DropdownMenu.Root
export const MenuTrigger = DropdownMenu.Trigger

const content = 'z-50 min-w-48 rounded-xl border border-line bg-surface p-1 text-sm text-fg shadow-pop'
const item = 'flex cursor-default select-none items-center gap-2 rounded-lg px-2.5 py-1.5 outline-none data-[highlighted]:bg-surface-2 data-[disabled]:opacity-50'

export function MenuContent({ children, align = 'start', side, testId }: { children: ReactNode; align?: 'start' | 'end' | 'center'; side?: 'top' | 'bottom' | 'left' | 'right'; testId?: string }) {
  return (
    <DropdownMenu.Portal>
      <DropdownMenu.Content align={align} side={side} sideOffset={6} collisionPadding={8} className={content} data-testid={testId}>
        {children}
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  )
}

export function MenuItem({ children, onSelect, danger, disabled, testId }: { children: ReactNode; onSelect?: (e: Event) => void; danger?: boolean; disabled?: boolean; testId?: string }) {
  return (
    <DropdownMenu.Item onSelect={onSelect} disabled={disabled} className={item + (danger ? ' text-red-700 dark:text-red-400' : '')} data-testid={testId}>
      {children}
    </DropdownMenu.Item>
  )
}

export function MenuCheckItem({ children, checked, onChange, testId }: { children: ReactNode; checked: boolean; onChange: (v: boolean) => void; testId?: string }) {
  return (
    <DropdownMenu.CheckboxItem checked={checked} onCheckedChange={onChange} onSelect={(e) => e.preventDefault()} className={item} data-testid={testId}>
      <span className="flex size-4 items-center justify-center">{checked && <Check size={14} />}</span>
      {children}
    </DropdownMenu.CheckboxItem>
  )
}

export function MenuSub({ label, icon, children }: { label: ReactNode; icon?: ReactNode; children: ReactNode }) {
  return (
    <DropdownMenu.Sub>
      <DropdownMenu.SubTrigger className={item}>
        {icon}
        <span className="flex-1">{label}</span>
        <ChevronRight size={14} />
      </DropdownMenu.SubTrigger>
      <DropdownMenu.Portal>
        <DropdownMenu.SubContent sideOffset={4} collisionPadding={8} className={content}>
          {children}
        </DropdownMenu.SubContent>
      </DropdownMenu.Portal>
    </DropdownMenu.Sub>
  )
}

export const MenuSeparator = () => <DropdownMenu.Separator className="my-1 h-px bg-line" />
export const MenuLabel = ({ children }: { children: ReactNode }) => <DropdownMenu.Label className="px-2.5 py-1 text-xs text-muted">{children}</DropdownMenu.Label>
