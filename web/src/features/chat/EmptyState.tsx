import { useMe, useModels } from '@/api/queries'
import { useComposer } from '@/stores/composer'
import { uiConfig } from '@/lib/ui-config'

function greeting(): string {
  const h = new Date().getHours()
  return h < 5 ? 'Good evening' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
}

export function EmptyState({ onPick }: { onPick: (text: string) => void }) {
  const { data: me } = useMe()
  const { data: models } = useModels()
  const model = useComposer((s) => s.model)
  const fromModel = models?.find((m) => m.id === model)?.suggestionPrompts
  const suggestions = (fromModel?.length ? fromModel : uiConfig().promptSuggestions).slice(0, 6)
  const first = me?.name?.split(/\s+/)[0]
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col items-center px-4 pb-6 pt-[12vh] text-center">
      <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">
        {greeting()}
        {first ? `, ${first}` : ''}
      </h1>
      <p className="mt-2 text-muted">What would you like to work on?</p>
      {suggestions.length > 0 && (
        <div className="mt-8 grid w-full gap-2 text-left sm:grid-cols-2 lg:grid-cols-3">
          {suggestions.map((s, i) => {
            const title = Array.isArray(s.title) ? s.title : s.title ? [s.title] : []
            return (
              <button key={i} type="button" data-testid="suggestion" onClick={() => onPick(s.content)} className="rounded-2xl border border-line bg-surface p-3 text-left max-sm:[&:nth-child(n+5)]:hidden text-sm transition-colors hover:bg-surface-2">
                <span className="line-clamp-1 font-medium">{title[0] ?? s.content}</span>
                <span className="line-clamp-2 text-muted">{title[1] ?? (title[0] ? s.content : '')}</span>
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
