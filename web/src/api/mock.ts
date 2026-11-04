// Dev-only fake backend (VITE_MOCK=1 or ?mock=1 under `pnpm dev`). It wraps
// window.fetch for /api/* and streams a realistic event sequence, so the UI can
// be built and checked without the Go server. Never part of a production build.
import type { StreamEvent } from './types'

type Json = Record<string, unknown>
type RawMsg = Json & { id: string; parentId: string | null; childrenIds: string[] }

const me = { id: 'u1', name: 'Ada Lovelace', email: 'ada@example.com', role: 'admin', profile_image_url: '', token: 'mock-token', permissions: { features: { api_keys: true }, workspace: { models: true } } }

const models = [
  {
    id: 'assistant',
    name: 'Assistant',
    info: { meta: { description: 'Cloud model with tools' } },
    features: [
      { id: 'web_search', label: 'Web search', icon: 'globe', default: true },
      { id: 'code_interpreter', label: 'Code', icon: 'terminal' },
    ],
    toolIds: ['server:docs'],
    suggestionPrompts: [
      { title: ['Compare laptops', 'under 1,500 with good battery life'], content: 'Compare three laptops under 1,500 with good battery life' },
      { title: ['Summarise a file', 'upload a CSV and get the highlights'], content: 'Summarise the attached file' },
      { title: ['Draft an email', 'to reschedule a meeting politely'], content: 'Draft a polite email to reschedule a meeting to next week' },
      { title: ['Explain a concept', 'like vector search in plain words'], content: 'Explain vector search in plain words' },
      { title: ['Plan a trip', 'three days in Lisbon'], content: 'Plan a three day trip to Lisbon' },
      { title: ['Copy rows', 'to a spreadsheet (asks to confirm)'], content: 'Copy the totals to my spreadsheet' },
    ],
  },
  { id: 'local-private', name: 'Local Private', local: true, info: { meta: { description: 'Runs on our own GPU' } }, features: [] },
  { id: 'reasoner', name: 'Reasoner', info: { meta: { description: 'Slower, thinks longer' } } },
  { id: 'base-hidden', name: 'Base model (hidden)', hidden: true },
]

const tools = [
  { id: 'docs', name: 'Docs search', kind: 'openapi', auth_type: 'bearer', authenticated: true, functions: ['search_docs'] },
  { id: 'mcp:sheets', name: 'Spreadsheets', kind: 'mcp', auth_type: 'user_oauth', provider: 'mcp:sheets', auto_enable: true, authenticated: false, functions: [] as string[] },
  { id: 'builtin:web', name: 'builtin:web', kind: 'builtin', auth_type: 'none', authenticated: true, functions: ['search_web', 'fetch_url'] },
]

const ui = {
  brand: { name: 'Kera', tagline: 'Team chat' },
  defaultModels: ['assistant'],
  modelOrder: ['assistant', 'reasoner', 'local-private'],
  toolDisplay: {
    search_web: { label: 'web search', icon: 'search', kind: 'search' },
    fetch_url: { label: 'page read', icon: 'globe', kind: 'read' },
    execute_code: { label: 'code run', icon: 'terminal', kind: 'code' },
  },
  integrations: [
    { id: 'sheets', kind: 'oauthToolServer', toolServerId: 'mcp:sheets', label: 'Spreadsheets', icon: 'sheet', autoEnable: true, showInPlusMenu: true },
    { id: 'browser-ext', kind: 'extensionInstall', label: 'Browser helper', version: '1.3.5', downloadUrl: '/cache/ext/browser-helper-1.3.5.zip', markerAttr: 'data-browser-helper', platforms: ['chromium-desktop'] },
  ],
}

const chats = new Map<string, { id: string; title: string; updated_at: number; created_at: number; pinned: boolean; folder_id: string | null; messages: Record<string, RawMsg>; currentId: string | null }>()
const folders: { id: string; name: string }[] = [{ id: 'fold1', name: 'Research' }]
const files = new Map<string, { id: string; name: string; size: number; type: string; polls: number }>()
const keys: Json[] = [{ id: 'k1', name: 'Laptop script', prefix: 'sk-4f2a', created_at: 1_760_000_000, last_used_at: 1_761_000_000 }]
let settings: Json = { ui: { memory: true } }

const now = () => Math.floor(Date.now() / 1000)
const uid = () => crypto.randomUUID()

function seed() {
  const id = 'seed-1'
  const u = 'm-u1'
  const a = 'm-a1'
  chats.set(id, {
    id,
    title: '🗂️ Quarterly numbers',
    updated_at: now() - 3600,
    created_at: now() - 3600,
    pinned: false,
    folder_id: null,
    currentId: a,
    messages: {
      [u]: { id: u, parentId: null, childrenIds: [a], role: 'user', content: 'What were the totals last quarter?', timestamp: now() - 3600 },
      [a]: { id: a, parentId: u, childrenIds: [], role: 'assistant', model: 'assistant', content: 'Totals were **4,210** orders, up 12% on the previous quarter.', timestamp: now() - 3590, status: 'done' },
    },
  })
  chats.set('seed-2', { id: 'seed-2', title: '✈️ Lisbon trip ideas', updated_at: now() - 3 * 86400, created_at: now() - 3 * 86400, pinned: true, folder_id: null, currentId: null, messages: {} })
  chats.set('seed-3', { id: 'seed-3', title: '📚 Vector search notes', updated_at: now() - 10 * 86400, created_at: now() - 10 * 86400, pinned: false, folder_id: 'fold1', currentId: null, messages: {} })
}

// ---- runs: an event log per message, replayable from any seq ----------------

interface Run {
  log: StreamEvent[]
  listeners: Set<() => void>
  finished: boolean
  cancelled: boolean
  answers: Map<string, (v: unknown) => void>
}
const runs = new Map<string, Run>()

function sse(run: Run, after: number): Response {
  const enc = new TextEncoder()
  let sent = after
  let notify: () => void = () => {}
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      const pump = () => {
        for (const ev of run.log) {
          if ((ev.seq ?? 0) <= sent) continue
          sent = ev.seq ?? sent
          c.enqueue(enc.encode(`event: ${ev.type}\nid: ${ev.seq}\ndata: ${JSON.stringify(ev)}\n\n`))
        }
        if (run.finished) {
          run.listeners.delete(notify)
          c.close()
        }
      }
      notify = pump
      c.enqueue(enc.encode(': connected\n\n'))
      run.listeners.add(pump)
      pump()
    },
    cancel() {
      run.listeners.delete(notify)
    },
  })
  return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

async function play(chatId: string | null, messageId: string, userText: string, model: string) {
  const run: Run = { log: [], listeners: new Set(), finished: false, cancelled: false, answers: new Map() }
  runs.set(messageId, run)
  let seq = 0
  const emit = (type: string, data: Json = {}) => {
    if (run.finished) return
    run.log.push({ type, seq: ++seq, messageId, ...(chatId?.startsWith('temp:') && seq === 1 ? { chatId } : {}), ...data })
    if (type === 'done' || type === 'error') run.finished = true
    run.listeners.forEach((l) => l())
  }
  const step = async (ms: number) => {
    await sleep(ms)
    if (run.cancelled) throw new Error('cancelled')
  }
  const words = async (text: string, type = 'delta') => {
    for (const w of text.match(/\S+\s*/g) ?? []) {
      emit(type, { text: w })
      await step(18)
    }
  }
  try {
    if (/error/i.test(userText)) {
      await step(300)
      emit('error', { code: 'provider_error', message: 'The model provider returned 502.' })
      return
    }
    await step(250)
    await words('The user wants a concise comparison. I should search first, then read the most relevant page and cite it. ', 'reasoning')
    emit('reasoning', { done: true, durationMs: 2300 })
    emit('tool_call', { callId: 'c1', name: 'search_web', server: '', args: { query: userText.slice(0, 60) } })
    emit('status', { callId: 'c1', id: 's1', action: 'web_search', description: `Searching the web for “${userText.slice(0, 40)}”`, done: false })
    await step(900)
    emit('status', { callId: 'c1', id: 's1', action: 'web_search', description: 'Found 5 results', done: true, urls: ['https://example.org/review', 'https://example.com/guide'] })
    emit('tool_result', { callId: 'c1', status: 'ok', preview: JSON.stringify([{ title: 'Review', url: 'https://example.org/review' }, { title: 'Guide', url: 'https://example.com/guide' }]) })
    emit('tool_call', { callId: 'c2', name: 'fetch_url', args: { url: 'https://example.org/review' } })
    emit('status', { callId: 'c2', id: 's2', action: 'fetch_url', description: 'Reading example.org', done: false })
    await step(800)
    emit('tool_result', { callId: 'c2', status: 'ok', preview: 'Battery life tests: model A 14 h, model B 11 h, model C 9 h.' })
    emit('sources', {
      sources: [
        { title: 'Independent laptop review', url: 'https://example.org/review', snippet: 'Battery life tests across three models.' },
        { title: 'Buying guide', url: 'https://example.com/guide', snippet: 'What to look for in a work laptop.' },
        { title: 'Spec sheet', url: 'https://docs.example.net/specs', snippet: 'Official specifications.' },
      ],
    })
    await words(
      "Here's a quick comparison based on the review [1] and the buying guide [2]:\n\n| Model | Battery | Price |\n|---|---|---|\n| A | 14 h | 1,399 |\n| B | 11 h | 1,199 |\n| C | 9 h | 999 |\n\nModel **A** lasts longest. To compute value per hour:\n\n```python\nprices = {'A': 1399, 'B': 1199, 'C': 999}\nhours = {'A': 14, 'B': 11, 'C': 9}\nfor m in prices:\n    print(m, round(prices[m] / hours[m], 1))\n```\n",
    )
    emit('block', {
      block: {
        type: 'cards',
        items: [
          { title: 'Model A 14" laptop, 16 GB, 512 GB', url: 'https://example.org/a', image: '/__mock/card/1.svg', price: '1,399', meta: '14 h battery · 1.3 kg', ctaLabel: 'View' },
          { title: 'Model B 15" laptop, 16 GB, 1 TB', url: 'https://example.org/b', image: '/__mock/card/2.svg', price: '1,199', meta: '11 h battery · 1.6 kg', ctaLabel: 'View' },
          { title: 'Model C 13" laptop, 8 GB, 256 GB', url: 'https://example.org/c', image: '/__mock/card/3.svg', price: '999', meta: '9 h battery · 1.1 kg', ctaLabel: 'View' },
        ],
      },
    })
    emit('files', { files: [{ id: 'f-summary', name: 'comparison.csv', size: 8806, contentType: 'text/csv' }] })
    files.set('f-summary', { id: 'f-summary', name: 'comparison.csv', size: 8806, type: 'text/csv', polls: 9 })
    await step(300)
    const wait = new Promise<unknown>((resolve) => run.answers.set('call-1', resolve))
    if (/copy|sheet/i.test(userText)) {
      emit('confirm', { callId: 'call-1', title: 'Copy rows to your spreadsheet?', message: 'This will copy **3 rows** into *Totals!A2:C4*:\n\n| Model | Battery | Price |\n|---|---|---|\n| A | 14 h | 1,399 |\n| B | 11 h | 1,199 |\n| C | 9 h | 999 |', confirmLabel: 'Copy rows' })
    } else {
      emit('ask_user', {
        callId: 'call-1',
        questions: [
          {
            header: 'Format',
            question: 'How would you like the full report?',
            options: [
              { label: 'Spreadsheet', description: 'One row per model with all specs', recommended: true },
              { label: 'PDF', description: 'A printable two-page summary' },
              { label: 'Short summary', description: 'Three bullet points' },
            ],
          },
        ],
      })
    }
    const answer = await wait
    emit('snapshot', { parts: undefined, pendingCalls: [] })
    await step(200)
    await words(`\n\nGot it (${JSON.stringify(answer)}). The CSV above has every figure; I can turn it into a chart next.`)
    emit('follow_ups', { follow_ups: ['Make a chart from the CSV', 'Which one is best for travel?', 'Show prices in EUR'] })
    if (chatId && !chatId.startsWith('temp:')) {
      const c = chats.get(chatId)
      if (c && c.title === 'New Chat') {
        c.title = '💻 Laptop battery comparison'
        emit('title', { title: c.title })
      }
    }
    emit('usage', { usage: { prompt_tokens: 812, completion_tokens: 233 } })
    emit('done')
  } catch {
    emit('done', { status: 'stopped' })
  } finally {
    persist(chatId, messageId, run, model)
  }
}

async function persist(chatId: string | null, messageId: string, run: Run, model: string) {
  const c = chatId ? chats.get(chatId) : undefined
  if (!c) return
  const { applyEvent } = await import('@/features/chat/reduce')
  const m = c.messages[messageId]
  if (!m) return
  let msg = { id: messageId, parentId: m.parentId, childrenIds: [], role: 'assistant' as const, createdAt: 0, status: 'streaming' as const, parts: [], files: [], sources: [], pendingCalls: [], model }
  for (const ev of run.log) msg = applyEvent(msg, ev) as typeof msg
  c.messages[messageId] = { ...m, parts: msg.parts, files: msg.files, sources: msg.sources, followUps: (msg as Json).followUps, status: msg.status, seq: run.log.length }
}

// ---- routes -------------------------------------------------------------------

function json(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

async function body(init?: RequestInit): Promise<Json> {
  try {
    return typeof init?.body === 'string' ? JSON.parse(init.body) : {}
  } catch {
    return {}
  }
}

const summary = (c: ReturnType<typeof chats.get> & object) => ({ id: c.id, title: c.title, updated_at: c.updated_at, created_at: c.created_at, pinned: c.pinned, folder_id: c.folder_id })

async function route(method: string, url: URL, init?: RequestInit): Promise<Response | null> {
  const p = url.pathname
  let m: RegExpMatchArray | null
  if (p === '/api/v1/auths/') return json(me)
  if (p === '/api/v1/auths/signout') return json({ ok: true })
  if (p === '/api/models') return json({ data: models })
  if (p === '/api/v1/tools/') return json(tools)
  if (p === '/api/v1/folders/') {
    if (method === 'POST') folders.push({ id: uid(), name: String((await body(init)).name ?? 'Folder') })
    return json(folders)
  }
  if (p === '/api/v1/users/user/settings') return json(settings)
  if (p === '/api/v1/users/user/settings/update') return json((settings = await body(init)))
  if (p === '/api/v1/auths/api_keys') {
    if (method === 'POST') {
      const k = { id: uid(), name: (await body(init)).name, prefix: 'sk-9c1e', created_at: now(), last_used_at: null }
      keys.push(k)
      return json({ ...k, key: 'sk-mock-not-a-real-key' })
    }
    return json(keys)
  }
  if ((m = p.match(/^\/api\/v1\/auths\/api_keys\/([^/]+)$/)) && method === 'DELETE') {
    keys.splice(keys.findIndex((k) => k.id === m![1]), 1)
    return json({ ok: true })
  }
  if ((m = p.match(/^\/api\/v1\/oauth\/clients\/([^/]+)\/session$/))) {
    const t = tools.find((x) => x.id === decodeURIComponent(m![1]))
    if (t) t.authenticated = false
    return json({ ok: true })
  }
  if (p === '/api/v1/chats/' || p === '/api/v1/chats/pinned' || p === '/api/v1/chats/search') {
    const q = url.searchParams.get('text')?.toLowerCase() ?? ''
    const list = [...chats.values()].sort((a, b) => b.updated_at - a.updated_at)
    if (p.endsWith('pinned')) return json(list.filter((c) => c.pinned).map(summary))
    return json(list.filter((c) => !q || c.title.toLowerCase().includes(q)).map(summary))
  }
  if (p === '/api/v1/chats/new') {
    const id = uid()
    chats.set(id, { id, title: 'New Chat', updated_at: now(), created_at: now(), pinned: false, folder_id: null, messages: {}, currentId: null })
    return json({ id })
  }
  if ((m = p.match(/^\/api\/v1\/chats\/folder\/([^/]+)$/))) {
    const id = decodeURIComponent(m[1])
    return json([...chats.values()].filter((c) => c.folder_id === id).map(summary))
  }
  if ((m = p.match(/^\/api\/v1\/chats\/share\/([^/]+)$/))) {
    const c = [...chats.values()][0]
    return c ? json({ id: c.id, title: c.title, chat: { history: { currentId: c.currentId, messages: c.messages } } }) : json({ detail: 'not found' }, 404)
  }
  if ((m = p.match(/^\/api\/v1\/chats\/([^/]+)\/messages\/([^/]+)\/(stream|cancel|calls\/(.+))$/))) {
    const run = runs.get(m[2])
    if (!run) return json({ detail: 'no run' }, 404)
    if (m[3] === 'stream') return sse(run, Number(url.searchParams.get('after') ?? 0))
    if (m[3] === 'cancel') {
      run.cancelled = true
      run.answers.forEach((r) => r(null))
      return json({ ok: true })
    }
    const answer = run.answers.get(decodeURIComponent(m[4]))
    if (!answer) return json({ detail: 'no such call' }, 404)
    answer((await body(init)).value)
    run.answers.delete(decodeURIComponent(m[4]))
    return json({ ok: true })
  }
  if ((m = p.match(/^\/api\/v1\/chats\/([^/]+)(\/(pin|archive|share|folder))?$/))) {
    const c = chats.get(m[1])
    if (!c) return json({ detail: 'Chat not found' }, 404)
    if (method === 'DELETE') return json(chats.delete(c.id))
    if (m[3] === 'pin') c.pinned = !c.pinned
    if (m[3] === 'archive') chats.delete(c.id)
    if (m[3] === 'share') return json({ share_id: 'share-' + c.id })
    if (m[3] === 'folder') c.folder_id = ((await body(init)).folder_id as string) ?? null
    if (!m[3] && method === 'POST') c.title = String(((await body(init)).chat as Json)?.title ?? c.title)
    return json({ id: c.id, title: c.title, pinned: c.pinned, chat: { title: c.title, history: { currentId: c.currentId, messages: c.messages } } })
  }
  if (p === '/api/v1/files/' && method === 'POST') {
    const f = (init?.body as FormData).get('file') as File
    const id = uid()
    files.set(id, { id, name: f.name, size: f.size, type: f.type, polls: 0 })
    await sleep(500)
    return json({ id, filename: f.name, meta: { name: f.name, size: f.size, content_type: f.type }, data: { status: 'pending' } })
  }
  if ((m = p.match(/^\/api\/v1\/files\/([^/]+)\/preview$/))) {
    const rows = [['Model', 'Battery (h)', 'Price', 'Weight (kg)', 'Released']]
    for (let i = 0; i < 40; i++) rows.push([`Model ${String.fromCharCode(65 + (i % 26))}${i >= 26 ? 2 : ''}`, String(8 + (i % 7)), (999 + i * 25).toLocaleString('en'), (1 + (i % 6) / 10).toFixed(1), `2026-${String((i % 12) + 1).padStart(2, '0')}-01`])
    return json({ kind: 'xlsx', sheets: ['Summary', 'Raw'], rows: url.searchParams.get('sheet') === '1' ? rows.slice(0, 5) : rows, truncated: false })
  }
  if ((m = p.match(/^\/api\/v1\/files\/([^/]+)$/))) {
    const f = files.get(m[1])
    if (!f) return json({ detail: 'not found' }, 404)
    f.polls++
    return json({ id: f.id, filename: f.name, meta: { name: f.name, size: f.size, content_type: f.type }, data: { status: f.polls < 3 ? 'processing' : 'completed' } })
  }
  if (p === '/api/chat/completions' && method === 'POST') {
    const b = await body(init)
    const messageId = String(b.id ?? uid())
    const chatId = (b.chat_id as string) ?? (b.temporary ? `temp:${uid()}` : null)
    const c = chatId ? chats.get(chatId) : undefined
    const um = b.user_message as Json | undefined
    if (c && um) {
      const uidm = String(um.id)
      c.messages[uidm] = { id: uidm, parentId: (um.parentId as string) ?? null, childrenIds: [], role: 'user', content: um.content, files: um.files, timestamp: now() }
      c.messages[messageId] = { id: messageId, parentId: uidm, childrenIds: [], role: 'assistant', model: b.model, content: '', timestamp: now(), status: 'streaming' }
      for (const x of Object.values(c.messages)) x.childrenIds = Object.values(c.messages).filter((y) => y.parentId === x.id).map((y) => y.id)
      c.currentId = messageId
      c.updated_at = now()
    }
    const last = (b.messages as Json[] | undefined)?.at(-1)?.content
    void play(chatId, messageId, String(last ?? ''), String(b.model))
    await sleep(50)
    return sse(runs.get(messageId)!, 0)
  }
  return null
}

export function installMock() {
  seed()
  window.__UI__ = { ...ui, ...(window.__UI__ ?? {}) } as typeof window.__UI__
  try {
    if (!localStorage.getItem('token')) localStorage.setItem('token', 'mock-token')
  } catch {
    // fine: the mock ignores auth
  }
  const real = window.fetch.bind(window)
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.origin)
    if (url.origin === location.origin && url.pathname.startsWith('/api/')) {
      const method = (init?.method ?? 'GET').toUpperCase()
      await sleep(80)
      const res = await route(method, url, init)
      if (res) return res
      console.warn('[mock] unhandled', method, url.pathname)
      return json({ detail: 'not mocked' }, 404)
    }
    return real(input, init)
  }
  console.info('[mock] Kera mock API active')
}
