// Admin > Help: how Kera answers a question. The content is in data.ts.
import { useEffect, useState } from 'react'
import { ChevronDown, CornerDownLeft } from 'lucide-react'
import { cx } from '@/lib/format'
import { Page } from '../ui'
import { DOCS, FLOW, LOOP_BOXES, MODE_BRANCHES, READING, SECTIONS, TUNING, docUrl, type Box } from './data'
import { LoopSection } from './Loop'
import { RetrievalSection } from './Retrieval'
import { BoxDetail, DocLink, KeyChip, Label, Section, mono, nodeTone } from './parts'

export function HelpPage() {
  return (
    <Page title="How Kera answers">
      <div className="lg:grid lg:grid-cols-[13rem_minmax(0,1fr)] lg:gap-10">
        <Contents />
        <div className="min-w-0 max-w-[60rem] space-y-14">
          <p className="max-w-[62ch] text-[15px] leading-relaxed">
            Kera answers in two stages. First it builds the prompt and, if the assistant has knowledge, searches it. Then it calls the model, runs the tools the model asks for, and calls it again until the model answers. This page shows both stages, with the settings that steer each step and where to look when a step misbehaves.
            <span className="mt-2 block text-sm text-muted">Select a box to open it. Every number shown is a default; your install may differ.</span>
          </p>
          <FlowSection />
          <RetrievalSection />
          <LoopSection />
          <TuneSection />
          <ReadSection />
        </div>
      </div>
    </Page>
  )
}

/** The in-page list of contents: sticky on wide screens, a wrapping row on narrow ones. */
function Contents() {
  const [active, setActive] = useState(SECTIONS[0].id)
  useEffect(() => {
    // the section whose heading was last scrolled past a line near the top of the scroll area
    const area = document.querySelector('[data-testid="admin-content"]') ?? window
    let frame = 0
    const update = () => {
      frame = 0
      let cur = SECTIONS[0].id
      for (const s of SECTIONS) if ((document.getElementById(s.id)?.getBoundingClientRect().top ?? Infinity) <= 140) cur = s.id
      setActive(cur)
    }
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(update)
    }
    area.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      area.removeEventListener('scroll', onScroll)
      if (frame) cancelAnimationFrame(frame)
    }
  }, [])
  const go = (id: string) => (e: React.MouseEvent) => {
    const el = document.getElementById(id)
    if (!el) return
    e.preventDefault()
    el.scrollIntoView({ behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'start' })
    setActive(id)
  }
  return (
    <nav aria-label="On this page" className="mb-6 lg:sticky lg:top-6 lg:mb-0 lg:self-start">
      <p className="mb-2 hidden text-[11px] font-semibold uppercase tracking-[0.08em] text-muted lg:block">On this page</p>
      <ul className="flex flex-wrap gap-1 lg:flex-col lg:gap-0.5">
        {SECTIONS.map((s, i) => (
          <li key={s.id}>
            <a
              href={`#${s.id}`}
              onClick={go(s.id)}
              aria-current={active === s.id ? 'location' : undefined}
              className={cx(
                'flex items-baseline gap-2 rounded-md px-2.5 py-1 text-sm lg:border-l-2 lg:rounded-l-none lg:py-1.5',
                active === s.id ? 'bg-accent-soft text-accent-ink lg:border-accent' : 'text-muted hover:bg-surface-2 hover:text-fg lg:border-line',
              )}
            >
              <span className={cx('text-[11px] tabular-nums opacity-70', mono)}>{String.fromCharCode(65 + i)}</span>
              {s.label}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  )
}

// ---- A. The flow ---------------------------------------------------------------------------------------------

function FlowSection() {
  const [open, setOpen] = useState<string | null>(FLOW[0].id)
  // consecutive boxes of the loop form one group
  const groups: { loop: boolean; boxes: { box: Box; n: number }[] }[] = []
  FLOW.forEach((box, i) => {
    const loop = LOOP_BOXES.includes(box.id)
    const last = groups[groups.length - 1]
    if (last && last.loop === loop) last.boxes.push({ box, n: i + 1 })
    else groups.push({ loop, boxes: [{ box, n: i + 1 }] })
  })
  return (
    <Section id="answered" title="A. How a question is answered" lede="One question, from your message to the saved reply. The four boxes inside the dashed frame repeat in every round until the model answers without asking for a tool.">
      <ol className="space-y-0" data-testid="flow">
        {groups.map((g, gi) =>
          g.loop ? (
            <li key={gi} className="relative my-2 rounded-2xl border border-dashed border-accent/50 bg-accent-soft/30 p-3 pt-9 sm:p-4 sm:pt-10">
              <span className="absolute left-3 top-2.5 inline-flex items-center gap-1.5 rounded-full bg-surface px-2.5 py-0.5 text-xs font-medium text-accent ring-1 ring-accent/30 dark:text-accent-bright sm:left-4">
                <CornerDownLeft size={12} aria-hidden /> The loop: these steps repeat each round
              </span>
              <ol>
                {g.boxes.map(({ box, n }) => (
                  <Station key={box.id} box={box} n={n} open={open === box.id} onToggle={() => setOpen(open === box.id ? null : box.id)} last={n === FLOW.length} />
                ))}
              </ol>
            </li>
          ) : (
            <li key={gi}>
              <ol>
                {g.boxes.map(({ box, n }) => (
                  <Station key={box.id} box={box} n={n} open={open === box.id} onToggle={() => setOpen(open === box.id ? null : box.id)} last={n === FLOW.length} />
                ))}
              </ol>
            </li>
          ),
        )}
      </ol>
    </Section>
  )
}

function Station({ box, n, open, onToggle, last }: { box: Box; n: number; open: boolean; onToggle: () => void; last: boolean }) {
  const panel = `st-${box.id}`
  const kind = box.kind ?? 'step'
  return (
    <li className="relative pb-3 pl-12" data-testid="station" data-box={box.id}>
      {!last && <span aria-hidden className="absolute bottom-0 left-[1.05rem] top-10 w-px bg-line" />}
      <span
        aria-hidden
        className={cx('absolute left-0 top-2 grid size-[2.2rem] place-items-center border-2 text-sm font-semibold tabular-nums shadow-sm', nodeTone[kind], kind === 'decision' ? 'rotate-45 rounded-lg' : 'rounded-full')}
      >
        <span className={kind === 'decision' ? '-rotate-45' : ''}>{n}</span>
      </span>
      <div className={cx('overflow-hidden rounded-xl border bg-surface transition-shadow', open ? 'border-accent/50 shadow-[0_2px_12px_rgb(0_0_0/0.06)]' : 'border-line')}>
        <button type="button" aria-expanded={open} aria-controls={panel} onClick={onToggle} className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-surface-2/50">
          <span className="min-w-0 flex-1">
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="text-[15px] font-semibold tracking-tight">{box.title}</span>
              {box.kind === 'guard' && <span className="rounded-full bg-warn-soft px-2 py-px text-[11px] font-medium text-warn-ink">guardrails</span>}
              {box.id === 'guardIn' && <span className="rounded-full bg-surface-2 px-2 py-px text-[11px] text-muted">also before every later round</span>}
            </span>
            <span className="mt-0.5 block text-sm text-muted">{box.summary}</span>
          </span>
          <ChevronDown size={18} aria-hidden className={cx('mt-0.5 shrink-0 text-muted transition-transform', open && 'rotate-180')} />
        </button>
        {box.id === 'mode' && (
          <ul className="grid gap-px border-t border-line bg-line text-sm sm:grid-cols-3" aria-label="Knowledge modes">
            {MODE_BRANCHES.map((b) => (
              <li key={b.mode} className="bg-surface-2/40 px-4 py-2">
                <span className={cx('font-semibold', mono)}>{b.mode}</span>
                <span className="block text-xs text-muted">{b.goes}</span>
              </li>
            ))}
          </ul>
        )}
        {open && (
          <div id={panel} role="region" aria-label={box.title} className="border-t border-line bg-bg-soft px-4 py-4">
            <BoxDetail box={box} />
          </div>
        )}
      </div>
    </li>
  )
}

// ---- D. Tuning -----------------------------------------------------------------------------------------------

function TuneSection() {
  return (
    <Section id="tune" title="D. What to tune and what to measure" lede="Start from the symptom. Look at the trace first, change one setting, and compare on the same questions. Each setting can be switched back on its own.">
      <ul className="grid gap-3 xl:grid-cols-2" data-testid="tuning">
        {TUNING.map((t) => (
          <li key={t.symptom} className="rounded-xl border border-line bg-surface p-4 text-sm leading-relaxed">
            <h4 className="mb-2.5 text-[15px] font-semibold tracking-tight">{t.symptom}</h4>
            <dl className="space-y-3">
              <div>
                <dt>
                  <Label>Look at</Label>
                </dt>
                <dd>{t.look}</dd>
              </div>
              <div>
                <dt>
                  <Label>Settings to try</Label>
                </dt>
                <dd className="flex flex-wrap gap-1.5">
                  {t.keys.map((k) => (
                    <KeyChip key={k} k={k} />
                  ))}
                </dd>
              </div>
              <div>
                <dt>
                  <Label>Measure</Label>
                </dt>
                <dd className="text-muted">{t.measure}</dd>
              </div>
            </dl>
          </li>
        ))}
      </ul>
      <p className="max-w-[62ch] text-xs text-muted">
        The repository has no built-in evaluation screen. The way to measure a retrieval change on your own questions is described in docs/retrieval.md, and the questions to ask of the trace in docs/context-management.md.
      </p>
    </Section>
  )
}

// ---- E. Links ------------------------------------------------------------------------------------------------

function ReadSection() {
  return (
    <Section id="docs" title="E. Read more">
      <ul className="grid gap-3 sm:grid-cols-2" data-testid="doc-links">
        {READING.map((id) => (
          <li key={id} className="rounded-xl border border-line bg-surface p-4">
            <DocLink doc={id} />
            <p className="mt-1 text-sm text-muted">{DOCS[id].blurb}</p>
            <p className={cx('mt-2 truncate text-[11px] text-muted', mono)} title={docUrl(id)}>
              {DOCS[id].path}
            </p>
          </li>
        ))}
      </ul>
    </Section>
  )
}
