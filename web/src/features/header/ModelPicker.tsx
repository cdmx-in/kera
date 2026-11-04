import { useState } from 'react'
import { Popover } from 'radix-ui'
import { Command } from 'cmdk'
import { Check, ChevronDown, Lock } from 'lucide-react'
import { useModels } from '@/api/queries'
import { useComposer } from '@/stores/composer'
import { useChat } from '@/stores/chat'
import type { Model } from '@/api/types'
import { cx } from '@/lib/format'

/** True when the open chat already has a reply from a local model (cloud models are then locked out). */
export function useLocalLock(models: Model[] | undefined): boolean {
  return useChat((s) => {
    if (!models) return false
    const local = new Set(models.filter((m) => m.local).map((m) => m.id))
    return Object.values(s.messages).some((m) => m.role === 'assistant' && m.model && local.has(m.model))
  })
}

export function ModelPicker() {
  const { data: models } = useModels()
  const model = useComposer((s) => s.model)
  const setModel = useComposer((s) => s.setModel)
  const locked = useLocalLock(models)
  const [open, setOpen] = useState(false)
  const current = models?.find((m) => m.id === model)
  // hidden models are listed only when explicitly selected (?models=)
  const visible = (models ?? []).filter((m) => !m.hidden || m.id === model)
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        data-testid="model-picker"
        data-model-id={current ? current.id : ''}
        aria-label="Select a model"
        className="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-[15px] font-medium hover:bg-surface-2"
      >
        <span className="truncate">{current?.name ?? (models ? 'Select a model' : 'Loading…')}</span>
        <ChevronDown size={16} className="shrink-0 text-muted" />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={6} collisionPadding={8} className="z-50 w-[min(22rem,calc(100vw-1rem))] rounded-xl border border-line bg-surface text-fg shadow-pop">
          <Command label="Models" loop>
            <Command.Input placeholder="Search models" className="w-full border-b border-line bg-transparent px-3 py-2.5 text-sm outline-none focus-visible:outline-none" />
            <Command.List className="max-h-80 overflow-y-auto p-1">
              <Command.Empty className="px-3 py-6 text-center text-sm text-muted">No models found</Command.Empty>
              {visible.map((m) => {
                const blocked = locked && !m.local
                return (
                  <Command.Item
                    key={m.id}
                    value={`${m.name} ${m.id}`}
                    disabled={blocked}
                    onSelect={() => {
                      setModel(m.id)
                      setOpen(false)
                    }}
                    data-testid="model-option"
                    data-model-id={m.id}
                    title={blocked ? 'This chat used a local model, so it stays on local models' : undefined}
                    className={cx('flex cursor-default items-center gap-2 rounded-lg px-2.5 py-2 text-sm data-[selected=true]:bg-surface-2', blocked && 'opacity-45')}
                  >
                    {m.profileImageUrl ? <img src={m.profileImageUrl} alt="" className="size-5 rounded-full" /> : <span className="grid size-5 place-items-center rounded-full bg-accent-soft text-[10px] font-semibold">{(m.name || m.id)[0]?.toUpperCase()}</span>}
                    <span className="min-w-0 flex-1">
                      <span className="block truncate">{m.name || m.id}</span>
                      {m.info?.meta?.description && <span className="block truncate text-xs text-muted">{m.info.meta.description}</span>}
                    </span>
                    {m.local && <span className="rounded bg-surface-2 px-1.5 text-[10px] uppercase text-muted">local</span>}
                    {blocked && <Lock size={13} className="text-muted" />}
                    {m.id === model && <Check size={15} className="text-accent" />}
                  </Command.Item>
                )
              })}
            </Command.List>
          </Command>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  )
}
