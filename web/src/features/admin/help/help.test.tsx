import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import answers from '../../../../../docs/how-it-answers.md?raw'
import readme from '../../../../../README.md?raw'
import { EXAMPLE, FIXED, FLOW, LOOP, OVERFLOW_AT, RETRIEVAL, SETTINGS, TUNING, WINDOWS, fusedScore, num, passagesOf, rerankKeep, simulate, type Box } from './data'
import { HelpPage } from './Help'

const settingsSrc = Object.values(import.meta.glob<string>('/src/features/admin/settings/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true })).join('\n')
const html = renderToStaticMarkup(<HelpPage />)
const decode = (s: string) => s.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/&gt;/g, '>')

describe('the Help page', () => {
  it('renders every box of the three diagrams', () => {
    for (const b of FLOW) expect(html, b.id).toContain(`data-testid="station" data-box="${b.id}"`)
    for (const b of RETRIEVAL) expect(decode(html), b.id).toContain(b.short)
    for (const b of LOOP) expect(html, b.id).toMatch(new RegExp(`data-box="${b.id}"`))
  })
  it('shows a heading and a link for every section, and every tuning row and doc link', () => {
    for (const l of ['A. How a question is answered', 'B. The retrieval pipeline', 'C. The agent loop', 'D. What to tune and what to measure', 'E. Read more']) expect(decode(html)).toContain(l)
    expect(html.match(/href="#(answered|retrieval|loop|tune|docs)"/g)).toHaveLength(5)
    for (const t of TUNING) expect(decode(html)).toContain(t.symptom)
    expect(html.match(/\/blob\/main\//g)?.length).toBeGreaterThanOrEqual(6)
  })
  it('opens the first box with its settings, their defaults and where they are', () => {
    expect(html).toContain('data-testid="box-detail" data-box="ask"')
    expect(html).toContain('aria-expanded="true"')
  })
  it('keeps every box complete: a summary, what it does and where to look', () => {
    for (const b of [...FLOW, ...RETRIEVAL, ...LOOP]) {
      expect(b.summary, b.id).not.toBe('')
      expect(b.what.length, b.id).toBeGreaterThan(0)
      expect(b.look.length, b.id).toBeGreaterThan(0)
    }
    for (const list of [FLOW, RETRIEVAL, LOOP]) expect(new Set(list.map((b) => b.id)).size).toBe(list.length)
  })
})

describe('the settings the page names', () => {
  it('are listed once, and the ones in a settings tab are in the web app', () => {
    expect(new Set(SETTINGS.map((s) => s.key)).size).toBe(SETTINGS.length)
    for (const s of SETTINGS) if (s.where.startsWith('Settings >')) expect(settingsSrc, s.key).toContain(`'${s.key}'`)
  })
  it('include every key the boxes and the tuning table use', () => {
    const known = new Set<string>(SETTINGS.map((s) => s.key))
    for (const k of [...FLOW, ...RETRIEVAL, ...LOOP].flatMap((b) => b.keys).concat(TUNING.flatMap((t) => t.keys))) expect(known.has(k), k).toBe(true)
  })
  it('have the fixed numbers the page quotes', () => {
    expect(FIXED.length).toBeGreaterThan(5)
    expect(OVERFLOW_AT).toBe(0.97)
  })
})

describe('the window meter', () => {
  it('uses the default windows and the default marks', () => {
    expect(WINDOWS.map((w) => w.tokens)).toEqual([16000, 120000])
    const s = simulate(16000, 0)
    expect([s.clearMark, s.clearTarget, s.overflowMark, s.spillChars]).toEqual([11200, 8000, 15520, 8000])
    expect(simulate(120000, 0).spillChars).toBe(60000)
  })
  it('changes zone at the thresholds', () => {
    const z = (t: number) => simulate(16000, t).zone
    expect([z(0), z(11200), z(11201), z(15519), z(15520), z(16000)]).toEqual(['quiet', 'quiet', 'clear', 'clear', 'overflow', 'overflow'])
    expect(num('chat.context.clear_at')).toBe(0.7)
  })
})

describe('the worked example', () => {
  const [a, b, c, d, e] = EXAMPLE.candidates
  it('fuses the way the search does', () => {
    expect(fusedScore(a)).toBeCloseTo(0.5 / 62 + 0.5 / 63 + 0.25 / 61, 10)
    const order = [...EXAMPLE.candidates].sort((x, y) => fusedScore(y) - fusedScore(x)).map((x) => x.id)
    expect(order).toEqual([c.id, a.id, b.id, e.id, d.id])
  })
  it('reranks, keeps the floor, widens the chunks and numbers the passages', () => {
    const { kept, byFloor } = rerankKeep(EXAMPLE.candidates, EXAMPLE.threshold, 3, 8)
    expect(kept.map((x) => x.id)).toEqual(['A', 'C', 'E'])
    expect(byFloor).toEqual(['C', 'E'])
    expect(passagesOf(kept, 1).map((p) => `[${p.n}] ${p.file} ${p.from}-${p.to}`)).toEqual(['[1] returns.md 3-5', '[2] returns.md 7-9', '[3] terms.md 1-3'])
    expect(passagesOf([a, { ...c, chunk: 5 }], 1)).toHaveLength(1) // touching chunks join
  })
})

describe('the Mermaid diagrams', () => {
  const node = (md: string, b: Box) => new RegExp(`\\b${b.id}[\\[{(]+"${b.short.replace(/[()]/g, '\\$&')}"`).test(md)
  const blocks = (md: string) => [...md.matchAll(/```mermaid\n([\s\S]*?)```/g)].map((m) => m[1])
  it('show every box of the page under the same id and label', () => {
    const [flow, retrieval, loop] = blocks(answers)
    for (const b of FLOW) expect(node(flow, b), `flow ${b.id}`).toBe(true)
    for (const b of RETRIEVAL) expect(node(retrieval, b), `retrieval ${b.id}`).toBe(true)
    for (const b of LOOP) expect(node(loop, b), `loop ${b.id}`).toBe(true)
  })
  it('are the same two diagrams in the README', () => {
    expect(blocks(readme)).toEqual(blocks(answers).slice(0, 2))
  })
})
