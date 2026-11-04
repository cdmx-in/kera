// One pager for every admin table: URL-backed (?page=&size=) on pages, local state in dialogs.
import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from 'lucide-react'
import { cx } from '@/lib/format'
import { Select } from '@/ui/Select'
import { pageCount, pageForSize, pageRange } from './lib'

export const PAGE_SIZES = [25, 50, 100]
const sizeOptions = PAGE_SIZES.map((n) => ({ value: String(n), label: String(n) }))

export interface Pager {
  page: number
  size: number
  /** replace: rewrite the history entry instead of adding one (clamping, a new search). */
  setPage: (page: number, replace?: boolean) => void
  setSize: (size: number) => void
}

/** Page and size in the query string, so reload and back/forward keep the place. prefix tells two tables on one page apart. */
export function useUrlPager(prefix = ''): Pager {
  const [params, setParams] = useSearchParams()
  const pk = prefix ? `${prefix}_page` : 'page'
  const sk = prefix ? `${prefix}_size` : 'size'
  const page = Math.max(1, Math.floor(Number(params.get(pk))) || 1)
  const size = PAGE_SIZES.includes(Number(params.get(sk))) ? Number(params.get(sk)) : PAGE_SIZES[0]
  const set = (p: number, s: number, replace = false) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (p > 1) next.set(pk, String(p))
        else next.delete(pk)
        if (s !== PAGE_SIZES[0]) next.set(sk, String(s))
        else next.delete(sk)
        return next
      },
      { replace },
    )
  return { page, size, setPage: (p, replace) => set(p, size, replace), setSize: (s) => set(pageForSize(page, size, s), s) }
}

/** Pager in component state (dialogs, which have no URL of their own). */
export function useLocalPager(): Pager {
  const [[page, size], set] = useState([1, PAGE_SIZES[0]])
  return { page, size, setPage: (p) => set([p, size]), setSize: (s) => set([pageForSize(page, size, s), s]) }
}

const btn =
  'inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-xs tabular-nums text-muted outline-none hover:bg-surface-2 hover:text-fg focus-visible:ring-2 focus-visible:ring-accent disabled:pointer-events-none disabled:opacity-35'

/** "Showing 26–50 of 59", first/prev/numbers/next/last and a page-size picker. Hides what a single page does not need. */
export function Pagination({ pager, total, label = 'Pagination', className }: { pager: Pager; total: number; label?: string; className?: string }) {
  const pages = pageCount(total, pager.size)
  const page = Math.min(pager.page, pages)
  // The total shrank under the current page (a delete, a new date range): move to the last page.
  useEffect(() => {
    if (pager.page > pages) pager.setPage(pages, true)
  }, [pager, pages])
  const from = total ? (page - 1) * pager.size + 1 : 0
  const to = Math.min(page * pager.size, total)
  const go = (p: number) => pager.setPage(Math.min(Math.max(1, p), pages))
  return (
    <nav aria-label={label} className={cx('flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-line px-4 py-2 text-xs text-muted', className)} data-testid="pagination">
      <span className="mr-auto tabular-nums" aria-live="polite">
        {total ? (
          <>
            <span className="hidden sm:inline">Showing </span>
            {from}–{to} of {total}
          </>
        ) : (
          'No rows'
        )}
      </span>
      {total > PAGE_SIZES[0] && (
        <span className="flex items-center gap-1.5">
          <span className="hidden sm:inline" aria-hidden>
            Rows per page
          </span>
          <Select
            aria-label="Rows per page"
            value={String(pager.size)}
            onChange={(v) => pager.setSize(Number(v))}
            options={sizeOptions}
            variant="pill"
            size="sm"
            data-testid="page-size"
          />
        </span>
      )}
      {pages > 1 && (
        <span className="flex items-center">
          <button type="button" className={btn} aria-label="First page" title="First page" disabled={page <= 1} onClick={() => go(1)}>
            <ChevronsLeft size={16} />
          </button>
          <button type="button" className={btn} aria-label="Previous page" title="Previous page" disabled={page <= 1} onClick={() => go(page - 1)}>
            <ChevronLeft size={16} />
          </button>
          <span className="hidden items-center md:flex">
            {pageRange(page, pages).map((p, i) =>
              p === null ? (
                <span key={`gap${i}`} className="w-6 text-center" aria-hidden>
                  …
                </span>
              ) : (
                <button
                  key={p}
                  type="button"
                  className={cx(btn, p === page && 'bg-accent-soft font-semibold text-accent dark:text-accent-bright')}
                  aria-label={`Page ${p}`}
                  aria-current={p === page ? 'page' : undefined}
                  onClick={() => go(p)}
                >
                  {p}
                </button>
              ),
            )}
          </span>
          <span className="px-1.5 tabular-nums md:hidden">
            {page} / {pages}
          </span>
          <button type="button" className={btn} aria-label="Next page" title="Next page" disabled={page >= pages} onClick={() => go(page + 1)}>
            <ChevronRight size={16} />
          </button>
          <button type="button" className={btn} aria-label="Last page" title="Last page" disabled={page >= pages} onClick={() => go(pages)}>
            <ChevronsRight size={16} />
          </button>
        </span>
      )}
    </nav>
  )
}
