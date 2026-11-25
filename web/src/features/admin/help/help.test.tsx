import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import answers from '../../../../../docs/how-it-answers.md?raw'
import readme from '../../../../../README.md?raw'
import { EXAMPLE, FIXED, FLOW, LOOP, LOOP_BOXES, OVERFLOW_AT, RETRIEVAL, SETTINGS, TUNING, WINDOWS, fusedScore, num, passagesOf, rerankKeep, simulate, type Box } from './data'
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

const blocks = (md: string) => [...md.matchAll(/```mermaid\n([\s\S]*?)```/g)].map((m) => m[1])

describe('the Mermaid text versions', () => {
  const node = (md: string, b: Box) => new RegExp(`\\b${b.id}[\\[{(]+"${b.short.replace(/[()]/g, '\\$&')}"`).test(md)
  it('show every box of the page under the same id and label', () => {
    const [flow, retrieval, loop] = blocks(answers)
    for (const b of FLOW) expect(node(flow, b), `flow ${b.id}`).toBe(true)
    for (const b of RETRIEVAL) expect(node(retrieval, b), `retrieval ${b.id}`).toBe(true)
    for (const b of LOOP) expect(node(loop, b), `loop ${b.id}`).toBe(true)
  })
  it('use only classes that are defined, on nodes that exist', () => {
    for (const [i, b] of blocks(answers).entries()) {
      const defined = new Set([...b.matchAll(/^\s*classDef (\w+) /gm)].map((m) => m[1]))
      const nodes = new Set([...b.matchAll(/\b(\w+)(?:\[|\{|\(\[)"/g)].map((m) => m[1]))
      for (const m of b.matchAll(/^\s*class ([\w,]+) (\w+)$/gm)) {
        expect(defined.has(m[2]), `block ${i}: class ${m[2]}`).toBe(true)
        for (const id of m[1].split(',')) expect(nodes.has(id), `block ${i}: node ${id}`).toBe(true)
      }
      expect(b.startsWith('flowchart '), `block ${i}`).toBe(true)
    }
  })
  it('are in the doc only, under the diagrams; the README shows the SVGs', () => {
    expect(blocks(readme)).toEqual([])
    expect(answers.match(/<summary>Text version \(Mermaid\)<\/summary>/g)).toHaveLength(3)
  })
})

// ---- the SVG diagrams (docs/images/how-it-answers-*.svg) -------------------------------------------------------

const svgFiles = import.meta.glob<string>('../../../../../docs/images/how-it-answers-*.svg', { query: '?raw', import: 'default', eager: true })
type Diagram = 'flow' | 'retrieval' | 'loop'
const svg: Record<Diagram, { light: string; dark: string }> = { flow: { light: '', dark: '' }, retrieval: { light: '', dark: '' }, loop: { light: '', dark: '' } }
for (const [path, text] of Object.entries(svgFiles)) {
  const m = path.match(/how-it-answers-(flow|retrieval|loop)-(light|dark)\.svg$/)
  if (m) svg[m[1] as Diagram][m[2] as 'light' | 'dark'] = text
}
const unesc = (t: string) => t.replace(/&gt;/g, '>').replace(/&lt;/g, '<').replace(/&quot;/g, '"').replace(/&amp;/g, '&')
/** The blocks of a diagram: id, step number, label and the setting chips on it. */
function drawn(text: string) {
  return [...text.matchAll(/<g id="b-[^"]+" class="blk[^"]*" data-box="([^"]+)"(?: data-step="(\d+)")?>([\n\s\S]*?)\n<\/g>/g)].map((m) => ({
    id: m[1],
    step: m[2] ? Number(m[2]) : null,
    title: unesc(m[3].match(/<text class="t"[^>]*>([^<]*)<\/text>/)?.[1] ?? ''),
    keys: [...m[3].matchAll(/<g class="chip">[\s\S]*?<text[^>]*>([^<]*)<\/text><\/g>/g)].map((c) => c[1]),
    box: (m[3].match(/<rect class="card" x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" height="([\d.]+)"/) ?? []).slice(1).map(Number),
  }))
}
const withoutStyle = (t: string) => t.replace(/<style>[\s\S]*?<\/style>/, '')

describe('the SVG diagrams', () => {
  const lists: [Diagram, Box[]][] = [['flow', FLOW], ['retrieval', RETRIEVAL], ['loop', LOOP]]
  it('draw every box of the page once, with its label, its number and only its own settings', () => {
    for (const [name, boxes] of lists) {
      const got = drawn(svg[name].light)
      expect(got.map((g) => g.id).sort(), name).toEqual(boxes.map((b) => b.id).sort())
      for (const g of got) {
        const b = boxes.find((x) => x.id === g.id)!
        expect(g.title, `${name} ${g.id} label`).toBe(b.short)
        for (const k of g.keys) expect(b.keys as readonly string[], `${name} ${g.id} shows ${k}`).toContain(k)
        // the steps are numbered as on the page: by place in the flow and in the pipeline, 1 to 4 for a round of the loop
        const want = name === 'loop' ? ['modelCall', 'run', 'results', 'fit'].indexOf(g.id) + 1 || null : boxes.indexOf(b) + 1
        expect(g.step, `${name} ${g.id} number`).toBe(want)
      }
    }
  })
  it('draw the dashed loop frame around the same four boxes as the Help page', () => {
    for (const name of ['flow', 'loop'] as const) {
      const f = svg[name].light.match(/<rect class="frame md" x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" height="([\d.]+)"/)!.slice(1).map(Number)
      const inside = drawn(svg[name].light)
        .filter((g) => g.box[0] >= f[0] && g.box[1] >= f[1] && g.box[0] + g.box[2] <= f[0] + f[2] && g.box[1] + g.box[3] <= f[1] + f[3])
        .map((g) => g.id)
      // in the flow the frame holds guardIn, model, tools and window; in the loop diagram it holds the four steps of a round
      expect(inside.sort(), name).toEqual((name === 'flow' ? LOOP_BOXES : ['modelCall', 'run', 'results', 'fit']).slice().sort())
    }
  })
  it('are the same drawing in the light and the dark variant: only the style differs', () => {
    for (const name of ['flow', 'retrieval', 'loop'] as const) {
      expect(svg[name].light.length, name).toBeGreaterThan(5000)
      expect(withoutStyle(svg[name].dark), name).toBe(withoutStyle(svg[name].light))
      expect(svg[name].dark, name).not.toBe(svg[name].light)
    }
  })
  it('are self-contained, static-safe and small: no script, no foreign content, no link, a reduced-motion rule', () => {
    for (const name of ['flow', 'retrieval', 'loop'] as const) {
      for (const mode of ['light', 'dark'] as const) {
        const t = svg[name][mode]
        const id = `${name} ${mode}`
        expect(t.startsWith('<svg xmlns="http://www.w3.org/2000/svg" '), id).toBe(true)
        expect(t.replace('xmlns="http://www.w3.org/2000/svg"', ''), id).not.toMatch(/https?:|\/\/|javascript:/i)
        expect(t, id).not.toMatch(/<script|<foreignObject|<image|<iframe|<a[ >]|\son[a-z]+=|@import|url\((?!#)/i)
        expect(t, id).toMatch(/<title id="t">[^<]+<\/title>\s*<desc id="d">[^<]{80,}<\/desc>/)
        expect(t, id).toContain('role="img"')
        expect(t, id).toContain('@media (prefers-reduced-motion: reduce)')
        expect(t.length, id).toBeLessThan(60 * 1024)
        // every reference points at something in the file
        const ids = new Set([...t.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]))
        for (const m of t.matchAll(/(?:href="|url\()#([\w-]+)/g)) expect(ids.has(m[1]), `${id} #${m[1]}`).toBe(true)
      }
    }
  })
  it('are well-formed: every tag is closed in order', () => {
    for (const name of ['flow', 'retrieval', 'loop'] as const) {
      const stack: string[] = []
      for (const m of svg[name].light.replace(/<!--[\s\S]*?-->/g, '').matchAll(/<(\/?)([A-Za-z][\w:-]*)((?:"[^"]*"|[^>"])*?)(\/?)>/g)) {
        if (m[4]) continue
        if (m[1]) expect(stack.pop(), name).toBe(m[2])
        else stack.push(m[2])
      }
      expect(stack, name).toEqual([])
    }
  })
  it('are shown by the README and the doc, as light and dark pairs of files that exist', () => {
    const refs = (md: string) => [...md.matchAll(/(?:srcset|src)="(?:docs\/)?images\/how-it-answers-(flow|retrieval|loop)-(light|dark)\.svg"/g)].map((m) => `${m[1]}-${m[2]}`)
    expect(refs(readme).sort()).toEqual(['flow-dark', 'flow-light', 'retrieval-dark', 'retrieval-light'])
    expect(refs(answers).sort()).toEqual(['flow-dark', 'flow-light', 'loop-dark', 'loop-light', 'retrieval-dark', 'retrieval-light'])
    for (const md of [readme, answers]) {
      for (const r of refs(md)) expect(svg[r.split('-')[0] as Diagram][r.split('-')[1] as 'light' | 'dark'], r).not.toBe('')
      for (const m of md.matchAll(/<source media="\(prefers-color-scheme: dark\)" srcset="[^"]*-dark\.svg">\s*<img src="[^"]*-light\.svg" alt="([^"]{40,})" width="880">/g)) expect(m[1]).not.toBe('')
      expect(md.match(/<picture>/g)?.length ?? 0).toBeGreaterThan(0)
    }
    expect(answers.match(/<source media="\(prefers-color-scheme: dark\)" srcset="images\/how-it-answers-/g)).toHaveLength(3)
  })
})
