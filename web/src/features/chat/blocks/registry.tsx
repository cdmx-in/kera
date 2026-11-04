import { lazy, Suspense, type ComponentType } from 'react'
import type { Block } from '@/api/types'

// block.type -> lazily loaded renderer. New block types register here.
const renderers: Record<string, ComponentType<{ block: Block }>> = {
  cards: lazy(() => import('./Cards')),
  product_cards: lazy(() => import('./Cards')),
}

export function BlockView({ block }: { block: Block }) {
  const R = renderers[block.type]
  if (!R) return null
  return (
    <Suspense fallback={<div className="h-24 animate-pulse rounded-xl bg-surface-2" />}>
      <R block={block} />
    </Suspense>
  )
}
