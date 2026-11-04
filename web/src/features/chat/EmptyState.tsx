import { ShieldCheck } from 'lucide-react'
import { useMe, useModels } from '@/api/queries'
import { useComposer } from '@/stores/composer'
import { uiConfig } from '@/lib/ui-config'
import { ModelIcon } from '@/features/header/ModelPicker'
import { pickSuggestions } from './model-select'

function greeting(): string {
  const h = new Date().getHours()
  return h < 5 ? 'Good evening' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
}

/** The new-chat screen: the selected model (icon, name, description) and its prompt suggestions. */
export function EmptyState({ onPick }: { onPick: (text: string) => void }) {
  const { data: me } = useMe()
  const { data: models } = useModels()
  const modelId = useComposer((s) => s.model)
  const model = models?.find((m) => m.id === modelId)
  const suggestions = pickSuggestions(model, uiConfig().promptSuggestions)
  const first = me?.name?.split(/\s+/)[0]
  const description = model?.info?.meta?.description?.trim()
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col items-center px-4 pb-6 pt-[10vh] text-center">
      {/* keyed by model: switching models replays the fade-up */}
      <div key={model?.id ?? ''} className="flex w-full animate-fade-up flex-col items-center" data-testid="empty-state" data-model-id={model?.id ?? ''}>
        {model && <ModelIcon m={model} size="lg" />}
        <p className="mt-4 text-sm text-muted">
          {greeting()}
          {first ? `, ${first}` : ''}
        </p>
        <h1 className="mt-1 max-w-full break-words text-2xl font-semibold tracking-tight sm:text-3xl">{model ? model.name || model.id : 'What would you like to work on?'}</h1>
        {model && <p className="mt-2 line-clamp-3 max-w-xl text-pretty text-muted">{description || 'What would you like to work on?'}</p>}
        {model?.local && (
          <span title="Runs on this server: data stays here" className="mt-3 inline-flex items-center gap-1 rounded-full bg-accent-soft px-2.5 py-0.5 text-xs font-medium text-accent dark:text-accent-bright">
            <ShieldCheck size={12} aria-hidden /> On server · private
          </span>
        )}
        {suggestions.length > 0 && (
          <div className="mt-8 grid w-full gap-2 text-left sm:grid-cols-2 lg:grid-cols-3">
            {suggestions.map((s, i) => {
              const title = Array.isArray(s.title) ? s.title : s.title ? [s.title] : []
              return (
                <button key={i} type="button" data-testid="suggestion" onClick={() => onPick(s.content)} className="flex flex-col rounded-2xl border border-line bg-surface p-3 text-left max-sm:[&:nth-child(n+5)]:hidden text-sm transition-colors hover:bg-surface-2">
                  <span className="line-clamp-1 font-medium">{title[0] ?? s.content}</span>
                  <span className="line-clamp-2 text-muted">{title[1] ?? (title[0] ? s.content : '')}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
