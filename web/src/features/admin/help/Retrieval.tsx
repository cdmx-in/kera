// B. The retrieval pipeline, with a worked example whose numbers change as you step through it.
import { useState } from 'react'
import { ArrowLeft, ArrowRight } from 'lucide-react'
import { cx } from '@/lib/format'
import { EXAMPLE, RETRIEVAL, fusedScore, fusedSum, passagesOf, rerankKeep, num, type Candidate } from './data'
import { BoxDetail, Section, mono } from './parts'

/** The steps in the strip; three of them are the parallel rankings of step 2. */
const strip: string[][] = [['q'], ['vec', 'kw', 'head'], ['fuse'], ['rerank'], ['nb'], ['num']]

const f4 = (n: number) => n.toFixed(4)

function Chip({ children, tone = 'grey' }: { children: React.ReactNode; tone?: 'grey' | 'accent' | 'ok' | 'warn' | 'bad' }) {
  const t = { grey: 'bg-surface-2 text-muted', accent: 'bg-accent-soft text-accent-ink', ok: 'bg-ok-soft text-ok-ink', warn: 'bg-warn-soft text-warn-ink', bad: 'bg-bad-soft text-bad-ink' }[tone]
  return <span className={cx('inline-flex items-center whitespace-nowrap rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums', t)}>{children}</span>
}

export function RetrievalSection() {
  const [step, setStep] = useState(0)
  const box = RETRIEVAL[step]
  return (
    <Section id="retrieval" title="B. The retrieval pipeline" lede="What happens when an assistant searches its knowledge, step by step. Step through one example question; the numbers are made up to show how the steps combine.">
      <ol className="flex flex-wrap items-stretch gap-x-2 gap-y-2" aria-label="Retrieval steps" data-testid="retrieval-strip">
        {strip.map((ids, gi) => (
          <li key={gi} className="flex items-stretch gap-1">
            <div className={cx('flex flex-wrap gap-1', ids.length > 1 && 'rounded-xl border border-dashed border-line p-1')}>
              {ids.map((id) => {
                const i = RETRIEVAL.findIndex((b) => b.id === id)
                const b = RETRIEVAL[i]
                const on = i === step
                return (
                  <button
                    key={id}
                    type="button"
                    aria-pressed={on}
                    onClick={() => setStep(i)}
                    className={cx('flex items-center gap-2 rounded-lg border px-2.5 py-1.5 text-left text-sm', on ? 'border-accent bg-accent text-white' : 'border-line bg-surface hover:bg-surface-2')}
                  >
                    <span className={cx('grid size-5 place-items-center rounded-full text-[11px] font-semibold tabular-nums', on ? 'bg-white/25' : 'bg-accent-soft text-accent-ink')}>{i + 1}</span>
                    {b.short}
                  </button>
                )
              })}
            </div>
          </li>
        ))}
      </ol>
      <p className="-mt-2 text-xs text-muted">Vector, keyword and heading search run side by side. Their rankings are fused in step 5.</p>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
        <Example step={step} />
        <div className="min-w-0 space-y-3 rounded-xl border border-line bg-surface p-4" aria-live="polite">
          <div className="flex items-center justify-between gap-2">
            <h4 className="text-[15px] font-semibold tracking-tight">
              Step {step + 1} of {RETRIEVAL.length}: {box.title}
            </h4>
            <div className="flex gap-1">
              <button type="button" aria-label="Previous step" disabled={step === 0} onClick={() => setStep(step - 1)} className="rounded-lg border border-line p-1.5 hover:bg-surface-2 disabled:opacity-40">
                <ArrowLeft size={15} aria-hidden />
              </button>
              <button type="button" aria-label="Next step" disabled={step === RETRIEVAL.length - 1} onClick={() => setStep(step + 1)} className="rounded-lg border border-line p-1.5 hover:bg-surface-2 disabled:opacity-40">
                <ArrowRight size={15} aria-hidden />
              </button>
            </div>
          </div>
          <p className="text-sm text-muted">{box.summary}</p>
          <BoxDetail box={box} narrow />
        </div>
      </div>
    </Section>
  )
}

/** Candidates in the order the step has just produced, with the numbers collected so far. */
function Example({ step }: { step: number }) {
  const id = RETRIEVAL[step].id
  const cs = EXAMPLE.candidates
  const rank = (n: number | null) => n ?? 99
  const fused = Object.fromEntries(cs.map((c) => [c.id, fusedScore(c)]))
  const { kept, byFloor } = rerankKeep(cs, EXAMPLE.threshold, num('rag.min_results'), num('rag.top_k_rerank'))
  const keptIds = kept.map((c) => c.id)
  const passages = passagesOf(kept, num('rag.neighbors'))
  let order: Candidate[] = cs
  if (id === 'vec') order = [...cs].sort((a, b) => rank(a.vec) - rank(b.vec))
  if (id === 'kw') order = [...cs].sort((a, b) => rank(a.kw) - rank(b.kw) || rank(a.vec) - rank(b.vec))
  if (id === 'head') order = [...cs].sort((a, b) => rank(a.head) - rank(b.head) || rank(a.vec) - rank(b.vec))
  if (id === 'fuse') order = [...cs].sort((a, b) => fused[b.id] - fused[a.id])
  if (id === 'rerank') order = [...cs].sort((a, b) => b.rerank - a.rerank)
  if (id === 'nb') order = kept
  const maxFused = Math.max(...Object.values(fused))
  const has = (...ids: string[]) => ids.includes(id)
  const idx = (x: string) => RETRIEVAL.findIndex((b) => b.id === x)
  const reached = (x: string) => step >= idx(x)

  return (
    <div className="min-w-0 rounded-xl border border-line bg-surface p-4" data-testid="example">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Chip tone="warn">Illustrative numbers</Chip>
        <span className="text-xs text-muted">Invented passages and ranks. Only the arithmetic is real.</span>
      </div>
      <p className="mb-3 text-sm">
        <span className="text-muted">Question: </span>
        <span className="font-medium">&ldquo;{EXAMPLE.question}&rdquo;</span>
      </p>

      {id === 'q' && <p className="mb-3 text-sm text-muted">The collection holds five passages that could matter. Nothing has been ranked yet.</p>}
      {id === 'fuse' && (
        <p className="mb-3 text-sm text-muted">
          Each score is weight / ({'60'} + rank), added over the lists. Weights: vector {EXAMPLE.weights.vec}, keyword {EXAMPLE.weights.kw}, heading {EXAMPLE.weights.head}.
        </p>
      )}
      {id === 'rerank' && (
        <p className="mb-3 text-sm text-muted">
          Example threshold {EXAMPLE.threshold} (the default is {num('rag.relevance_threshold')}, which keeps all). Only {cs.filter((c) => c.rerank >= EXAMPLE.threshold).length} passage clears it, so the floor of {num('rag.min_results')} results keeps the next best.
        </p>
      )}
      {id === 'nb' && <p className="mb-3 text-sm text-muted">Each kept chunk is widened by {num('rag.neighbors')} chunk before and after it, from the same file. Chunk numbers count from 0 within a file.</p>}

      {id !== 'num' ? (
        <ol key={step} className="space-y-2" data-testid="candidates">
          {order.map((c) => {
            const dropped = reached('rerank') && !keptIds.includes(c.id)
            const lo = Math.max(0, c.chunk - num('rag.neighbors'))
            const hi = c.chunk + num('rag.neighbors')
            return (
              <li key={c.id} className={cx('motion-safe:animate-fade-up rounded-lg border border-line px-3 py-2 text-sm', dropped && has('rerank', 'nb') && 'opacity-70')} data-testid="candidate">
                <div className="flex items-start gap-2.5">
                  <span className={cx('mt-0.5 grid size-5 shrink-0 place-items-center rounded bg-surface-2 text-[11px] font-semibold', mono)}>{c.id}</span>
                  <div className="min-w-0 flex-1">
                    <p className="font-medium leading-snug">{c.text}</p>
                    <p className={cx('mt-0.5 break-words text-xs text-muted', mono)}>
                      {c.file} &middot; {c.heading} &middot; chunk {c.chunk}
                    </p>
                    <div className="mt-1.5 flex flex-wrap gap-1">
                      {reached('vec') && <Chip tone={has('vec') ? 'accent' : 'grey'}>vector #{c.vec}</Chip>}
                      {reached('kw') && <Chip tone={has('kw') ? 'accent' : 'grey'}>{c.kw ? `keyword #${c.kw}` : 'keyword: no match'}</Chip>}
                      {reached('head') && <Chip tone={has('head') ? 'accent' : 'grey'}>{c.head ? `heading #${c.head}` : 'heading: no match'}</Chip>}
                      {reached('fuse') && <Chip tone={has('fuse') ? 'accent' : 'grey'}>fused {f4(fused[c.id])}</Chip>}
                      {reached('rerank') && <Chip tone={has('rerank') ? 'accent' : 'grey'}>rerank {c.rerank.toFixed(2)}</Chip>}
                      {reached('rerank') && (dropped ? <Chip tone="bad">dropped</Chip> : byFloor.includes(c.id) ? <Chip tone="warn">kept by the floor</Chip> : <Chip tone="ok">kept</Chip>)}
                      {has('nb') && (
                        <Chip tone="accent">
                          chunks {lo} to {hi}
                        </Chip>
                      )}
                    </div>
                    {id === 'fuse' && (
                      <p className={cx('mt-1.5 break-words text-xs text-muted', mono)}>
                        {fusedSum(c)} = {f4(fused[c.id])}
                      </p>
                    )}
                    {has('fuse') && <div aria-hidden className="mt-1.5 h-1 rounded-full bg-surface-2"><i className="block h-full rounded-full bg-accent" style={{ width: `${(100 * fused[c.id]) / maxFused}%` }} /></div>}
                    {has('rerank') && <div aria-hidden className="mt-1.5 h-1 rounded-full bg-surface-2"><i className="block h-full rounded-full bg-accent" style={{ width: `${100 * c.rerank}%` }} /></div>}
                  </div>
                </div>
              </li>
            )
          })}
        </ol>
      ) : (
        <div key={step} className="space-y-2" data-testid="passages">
          <p className="text-sm text-muted">
            Chunks that touch would be joined into one passage. Here the three kept chunks are far enough apart to stay separate. Files come in the order of their best hit, and the passages of a file in document order.
          </p>
          <ol className="space-y-2">
            {passages.map((p) => (
              <li key={p.n} className="motion-safe:animate-fade-up flex items-start gap-3 rounded-lg border border-accent/40 bg-accent-soft/40 px-3 py-2 text-sm" data-testid="passage">
                <span className={cx('mt-px rounded bg-accent px-1.5 py-0.5 text-xs font-semibold text-white', mono)}>[{p.n}]</span>
                <span className="min-w-0">
                  <span className={cx('block break-words', mono)}>
                    {p.file} &middot; chunks {p.from} to {p.to}
                  </span>
                  <span className="text-xs text-muted">Best hit: passage {p.best}. The model cites it as [{p.n}] and the answer&rsquo;s sources list shows it under the same number.</span>
                </span>
              </li>
            ))}
          </ol>
        </div>
      )}
    </div>
  )
}
