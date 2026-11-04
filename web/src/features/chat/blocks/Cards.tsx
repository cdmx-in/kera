import type { Block } from '@/api/types'
import { domainOf } from '@/lib/format'

interface Item {
  title?: string
  url?: string
  image?: string
  price?: string
  meta?: string
  ctaLabel?: string
}

const safeUrl = (u?: string) => (u && /^(https?:)?\/\//i.test(u) ? u : u?.startsWith('/') ? u : undefined)

/** Generic item-card grid: {type:'cards', items:[{title,url,image,price,meta,ctaLabel}]}. */
export default function Cards({ block }: { block: Block }) {
  const items = (Array.isArray(block.items) ? block.items : []) as Item[]
  if (!items.length) return null
  return (
    <div data-testid="cards" className="my-3 grid grid-cols-[repeat(auto-fill,minmax(11rem,1fr))] gap-3">
      {items.map((it, i) => {
        const url = safeUrl(it.url)
        const img = safeUrl(it.image)
        return (
          <article key={i} className="flex flex-col overflow-hidden rounded-xl border border-line bg-surface">
            {img && <img src={img} alt="" loading="lazy" className="h-36 w-full bg-white object-contain p-2" />}
            <div className="flex flex-1 flex-col gap-1 p-3">
              <h4 className="line-clamp-3 text-sm font-medium">{it.title ?? domainOf(url)}</h4>
              {it.meta && <p className="line-clamp-2 text-xs text-muted">{it.meta}</p>}
              <div className="mt-auto flex items-center justify-between gap-2 pt-2">
                {it.price ? <span className="text-sm font-semibold">{it.price}</span> : <span />}
                {url && (
                  <a href={url} target="_blank" rel="noreferrer" className="rounded-lg bg-accent px-2.5 py-1 text-xs font-medium text-white hover:opacity-90">
                    {it.ctaLabel ?? 'View'}
                  </a>
                )}
              </div>
            </div>
          </article>
        )
      })}
    </div>
  )
}
