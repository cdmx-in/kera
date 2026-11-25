// C. The agent loop: one run's rounds, its exits, and a meter that shows which protection starts at which prompt size.
import { useState } from 'react'
import { ArrowDown, ArrowRight, CornerDownLeft, LogOut, RotateCcw } from 'lucide-react'
import { cx } from '@/lib/format'
import { LOOP, OVERFLOW_AT, WINDOWS, num, simulate, type Box, type Zone } from './data'
import { BoxDetail, KeyChip, Section, nodeTone } from './parts'

const box = (id: string) => LOOP.find((b) => b.id === id)!
const cycle = ['modelCall', 'run', 'results', 'fit']
const exits = ['final', 'cap', 'retry', 'fail']

export function LoopSection() {
  const [sel, setSel] = useState('modelCall')
  const cur = box(sel)
  return (
    <Section id="loop" title="C. The agent loop" lede="A run is a number of rounds. Each round asks the model, runs the tools it asked for, and adds the results. The loop ends with an answer, at the round cap, or on an error.">
      <div className="space-y-3 rounded-2xl border border-line bg-surface p-4" data-testid="loop">
        <p className="text-xs text-muted">Start: the prompt is built (system prompt, earlier turns, any retrieved passages, tool definitions).</p>
        <ol className="grid items-stretch gap-y-1.5 lg:grid-cols-[1fr_auto_1fr_auto_1fr_auto_1fr]" aria-label="One round">
          {cycle.map((id, i) => (
            <li key={id} className="contents">
              <Node b={box(id)} n={i + 1} on={sel === id} onSelect={() => setSel(id)} />
              {i < cycle.length - 1 && (
                <span aria-hidden className="grid place-items-center text-muted lg:px-1">
                  <ArrowDown size={16} className="lg:hidden" />
                  <ArrowRight size={16} className="hidden lg:block" />
                </span>
              )}
            </li>
          ))}
        </ol>
        <div className="flex items-center gap-2 rounded-lg border border-dashed border-accent/40 bg-accent-soft/30 px-3 py-2 text-sm text-accent-ink">
          <CornerDownLeft size={16} aria-hidden className="shrink-0" />
          <span>Next round: back to the model call with the results added. The loop goes round until a round has no tool calls.</span>
        </div>
        <h4 className="pt-2 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted">Exits and retries</h4>
        <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4" aria-label="Exits and retries">
          {exits.map((id) => {
            const b = box(id)
            return (
              <li key={id}>
                <button
                  type="button"
                  aria-pressed={sel === id}
                  onClick={() => setSel(id)}
                  className={cx('flex h-full w-full flex-col gap-1 rounded-xl border px-3 py-2 text-left', sel === id ? 'border-accent bg-accent-soft/40' : 'border-line hover:bg-surface-2/50', id === 'retry' && 'border-dashed')}
                  data-testid="exit"
                  data-box={id}
                >
                  <span className="flex items-center gap-1.5 text-sm font-semibold">
                    {id === 'retry' ? <RotateCcw size={14} aria-hidden className="shrink-0" /> : <LogOut size={14} aria-hidden className={cx('shrink-0', id === 'fail' && 'text-bad')} />}
                    {b.title}
                  </span>
                  <span className="text-xs text-muted">{b.when}</span>
                </button>
              </li>
            )
          })}
        </ul>
      </div>

      <div className="min-w-0 rounded-xl border border-line bg-surface p-4" aria-live="polite" data-testid="loop-detail">
        <h4 className="mb-1 text-[15px] font-semibold tracking-tight">{cur.title}</h4>
        <p className="mb-4 text-sm text-muted">{cur.summary}</p>
        <BoxDetail box={cur} />
      </div>

      <Meter />
    </Section>
  )
}

function Node({ b, n, on, onSelect }: { b: Box; n: number; on: boolean; onSelect: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onSelect}
      data-testid="loop-node"
      data-box={b.id}
      className={cx('flex h-full flex-col gap-1 rounded-xl border px-3 py-2.5 text-left', on ? 'border-accent bg-accent-soft/40 shadow-sm' : 'border-line hover:bg-surface-2/50')}
    >
      <span className="flex items-center gap-2 text-sm font-semibold">
        <span aria-hidden className={cx('grid size-5 shrink-0 place-items-center rounded-full border text-[11px] tabular-nums', nodeTone[b.kind ?? 'step'])}>
          {n}
        </span>
        {b.title}
      </span>
      <span className="text-xs text-muted">{b.summary}</span>
    </button>
  )
}

// ---- The window meter ----------------------------------------------------------------------------------------

const zoneLook: Record<Zone, { label: string; bar: string; chip: string }> = {
  quiet: { label: 'Nothing happens', bar: 'bg-ok', chip: 'bg-ok-soft text-ok-ink' },
  clear: { label: 'Clear, then summarise if needed', bar: 'bg-warn', chip: 'bg-warn-soft text-warn-ink' },
  overflow: { label: 'Overflow suspected', bar: 'bg-bad', chip: 'bg-bad-soft text-bad-ink' },
}

const n0 = (n: number) => n.toLocaleString('en-US')

function Meter() {
  const [win, setWin] = useState(WINDOWS[0].id)
  const [pct, setPct] = useState(40)
  const w = WINDOWS.find((x) => x.id === win)!
  const sim = simulate(w.tokens, Math.round((pct / 100) * w.tokens))
  const look = zoneLook[sim.zone]
  const at = (t: number) => `${(100 * t) / w.tokens}%`
  return (
    <div className="space-y-4 rounded-2xl border border-line bg-surface p-4" data-testid="meter">
      <div>
        <h4 className="text-[15px] font-semibold tracking-tight">The window meter</h4>
        <p className="mt-0.5 max-w-[62ch] text-sm text-muted">
          How full is the model&rsquo;s context window, and what does Kera do about it? Drag the slider to change the prompt size. The marks use the default settings.
        </p>
      </div>

      <fieldset className="flex flex-wrap gap-2">
        <legend className="sr-only">Window</legend>
        {WINDOWS.map((x) => (
          <label key={x.id} className={cx('cursor-pointer rounded-lg border px-3 py-1.5 text-sm has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-accent', win === x.id ? 'border-accent bg-accent-soft/50' : 'border-line hover:bg-surface-2')}>
            <input type="radio" name="window" className="sr-only" checked={win === x.id} onChange={() => setWin(x.id)} />
            {x.label}: <span className="font-semibold tabular-nums">{n0(x.tokens)}</span> tokens
          </label>
        ))}
      </fieldset>

      <div>
        <div className="relative h-16 pt-1">
          <div className="absolute inset-x-0 top-1 h-3 overflow-hidden rounded-full bg-surface-2" aria-hidden>
            <i className={cx('block h-full rounded-full transition-[width]', look.bar)} style={{ width: `${pct}%` }} />
          </div>
          {[
            { t: sim.clearTarget, label: 'clear to', edge: false },
            { t: sim.clearMark, label: 'clear at', edge: false },
            { t: sim.overflowMark, label: 'overflow', edge: true },
          ].map((m) => (
            <span key={m.label} aria-hidden className="absolute top-0 w-0" style={{ left: at(m.t) }}>
              <i className="absolute -translate-x-1/2 block h-5 w-px bg-fg/60" />
              <span className={cx('absolute top-6 whitespace-nowrap text-[11px] leading-tight text-muted', m.edge ? 'right-0 text-right' : 'left-0 -translate-x-1/2 text-center')}>
                {m.label}
                <br />
                <span className="tabular-nums">{n0(m.t)}</span>
              </span>
            </span>
          ))}
        </div>
        <label className="mt-1 block">
          <span className="sr-only">Prompt size as a share of the window</span>
          <input type="range" min={0} max={100} step={1} value={pct} onChange={(e) => setPct(Number(e.target.value))} className="w-full accent-[var(--accent)]" aria-valuetext={`${n0(sim.tokens)} tokens, ${pct}% of the window`} />
        </label>
        <p className="text-sm">
          Prompt: <span className="font-semibold tabular-nums">{n0(sim.tokens)}</span> tokens, <span className="tabular-nums">{pct}%</span> of the window
        </p>
      </div>

      <div role="status" className="space-y-1.5 rounded-xl border border-line bg-bg-soft p-3 text-sm leading-relaxed" data-testid="meter-readout" data-zone={sim.zone}>
        <span className={cx('inline-block rounded-full px-2.5 py-0.5 text-xs font-semibold', look.chip)}>{look.label}</span>
        {sim.zone === 'quiet' && <p>The prompt is under {Math.round(num('chat.context.clear_at') * 100)}% of the window. Kera leaves it as it is. If a single tool result is longer than {n0(sim.spillChars)} characters, it is still stored as a file and the model sees its start and end.</p>}
        {sim.zone === 'clear' && (
          <p>
            Before the next call, results from rounds older than the newest {num('chat.context.keep_rounds')} are replaced by a one-line note, oldest first, until the estimate is down to {n0(sim.clearTarget)} tokens. If the prompt is still above {n0(sim.clearMark)}, the task model summarises the run so far and the summary takes its place, keeping the newest 2 rounds.
          </p>
        )}
        {sim.zone === 'overflow' && (
          <p>
            A call that reports {Math.round(OVERFLOW_AT * 100)}% or more of the window is recorded as overflow_suspected, because a server such as Ollama silently drops the oldest messages of a prompt that is too long. Clearing starts at {Math.round(num('chat.context.clear_at') * 100)}%, so this appears when context management is off, when there are too few old rounds to clear, or when the window you set is larger than the model&rsquo;s real one.
          </p>
        )}
      </div>

      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1.5 text-xs text-muted">
        <span>Set by:</span>
        {(['chat.context.clear_at', 'chat.context.clear_to', 'chat.context.keep_rounds', 'chat.context.compact', 'tools.spill_chars'] as const).map((k) => (
          <KeyChip key={k} k={k} />
        ))}
        <KeyChip k={w.key} />
        <span className="basis-full">The window comes from the model parameter context_window, then num_ctx, then the setting above for this kind of model. Overflow at 97% is fixed in the code.</span>
      </div>
    </div>
  )
}
