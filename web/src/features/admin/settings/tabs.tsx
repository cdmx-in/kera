// Settings tabs that are plain forms over settings keys.
import { useState } from 'react'
import { ArrowDown, ArrowUp, GripVertical, Plus, X } from 'lucide-react'
import { useAuthOptions, useModels } from '@/api/queries'
import { IconButton } from '@/ui/Dialog'
import { cx } from '@/lib/format'
import { move } from '../lib'
import { Btn, Card, Check, inputCls } from '../ui'
import { ExtractorStatus } from './ExtractorStatus'
import { SettingRow, SettingsForm, type Draft, type FieldSpec } from './form'
import { WEB_SEARCH_FIELDS } from './webSearchFields'
import { Select } from '@/ui/Select'
import { Switch } from '@/ui/misc'

const f = (key: string, kind: FieldSpec['kind'], hint?: string, label?: string): FieldSpec => ({ key, kind, hint, label })

export function TasksTab() {
  return (
    <SettingsForm
      sections={[
        {
          title: 'Task models',
          hint: 'Background tasks (titles, tags, follow-ups, search queries) run on these models. Chats on local models use the local task model.',
          fields: [f('tasks.model_external', 'model', 'For chats on cloud models.', 'Cloud task model'), f('tasks.model_local', 'model', 'For chats on local models.', 'Local task model')],
        },
        {
          title: 'Tasks',
          fields: [
            f('tasks.title', 'bool', 'Name new chats from the first exchange.', 'Generate titles'),
            f('tasks.tags', 'bool', undefined, 'Generate tags'),
            f('tasks.follow_ups', 'bool', 'Suggest follow-up questions after a reply.', 'Follow-up suggestions'),
            f('tasks.query', 'bool', 'Rewrite the question into search queries for web and knowledge search.', 'Search query generation'),
          ],
        },
      ]}
    />
  )
}

export function ChatTab() {
  return (
    <SettingsForm
      sections={[
        {
          title: 'Context compaction',
          hint: 'Summarise older messages when a conversation grows past the threshold.',
          fields: [
            f('chat.compaction.enable', 'bool', undefined, 'Enable compaction'),
            f('chat.compaction.threshold', 'int', 'Estimated tokens before compacting.', 'Threshold (tokens)'),
            f('chat.compaction.retention', 'int', 'Recent messages kept verbatim.', 'Messages kept'),
          ],
        },
        {
          title: 'Tools and models',
          fields: [
            f('chat.max_tool_iterations', 'int', 'Tool-calling rounds per reply before the model must answer.'),
            f('chat.local_lock', 'bool', 'A chat that used a local model cannot switch to a cloud model.', 'Local lock'),
            f('compat.throwaway_chats', 'bool', 'Accept the create-chat, complete, delete flow older API clients use.', 'Throwaway API chats'),
          ],
        },
        {
          title: 'Run trace',
          hint: 'Sizes, token counts and timings of each model round and tool call, shown under Admin > Traces. No prompt, answer or tool text is stored.',
          fields: [
            f('trace.enable', 'bool', undefined, 'Record traces'),
            f('trace.retention_days', 'int', 'Days a trace is kept before it is deleted (at least 1).', 'Keep for (days)'),
          ],
        },
        {
          title: 'Memory',
          fields: [
            f('chat.memory.enable', 'bool', 'Let the assistant remember details across chats (users opt in).', 'Enable memory'),
            f('chat.memory.review_every', 'int', 'Note lasting facts about the user every N user messages of a chat (0 turns this off). Only for users who switched memory on.', 'Review every'),
            f('chat.memory.local_only', 'bool', 'Extract memories with the local task model only.', 'Local models only'),
          ],
        },
      ]}
    />
  )
}

export function WebTab() {
  return (
    <SettingsForm
      sections={[
        {
          title: 'Web search',
          hint: 'The built-in web tools. Presets with native web search use the provider instead.',
          fields: [
            ...WEB_SEARCH_FIELDS,
          ],
        },
        {
          title: 'Page reading',
          fields: [f('web.fetch.max_chars', 'int', 'Characters kept from a fetched page.', 'Max characters'), f('web.fetch.max_calls_per_turn', 'int', undefined, 'Page reads per reply')],
        },
      ]}
    />
  )
}

export function CodeTab() {
  return (
    <SettingsForm
      sections={[
        {
          title: 'Code interpreter',
          fields: [
            f('code_interpreter.engine', { select: [['', 'Off'], ['jupyter', 'Jupyter']] }, undefined, 'Engine'),
            f('code_interpreter.url', 'text', 'Jupyter server URL.', 'URL'),
            f('code_interpreter.token', 'secret', 'Jupyter token (write-only).', 'Token'),
            f('code_interpreter.timeout', 'int', 'Seconds per code run.', 'Timeout'),
            f('code_interpreter.shared_dir', 'text', 'Directory shared with the kernel, as Kera sees it.', 'Shared directory'),
            f('code_interpreter.kernel_dir', 'text', 'The same directory as the kernel sees it.', 'Kernel directory'),
            f('code_interpreter.max_file_mb', 'int', 'Largest file harvested from a run.', 'Max file size (MB)'),
            f('code_interpreter.prompt', 'textarea', 'Instructions added to the system prompt when the tool is on.', 'Prompt'),
            f('code_interpreter.prelude', 'textarea', 'Python run before every snippet.', 'Prelude'),
            f(
              'code_interpreter.languages',
              'json',
              'Languages execute_code can run: name -> {command: [argv with {file}], ext, aliases, label, check, fallback}. Python (no command) runs in the kernel; others run as a subprocess. Only languages whose program exists in the sandbox are offered.',
              'Languages',
            ),
          ],
        },
      ]}
    />
  )
}

export function SpendSourceTab() {
  return (
    <SettingsForm
      sections={[
        {
          title: 'Spend source',
          hint: "Admin > Spend reads LiteLLM's spend log from its database, e.g. postgres://user:password@host:5432/litellm. To name people, forward each user's email in the X-LiteLLM-Customer-Id header (Connections).",
          fields: [
            f('spend.source.kind', { select: [['', 'Off'], ['litellm_postgres', 'LiteLLM database (Postgres)']] }, undefined, 'Source'),
            f('spend.source.dsn', 'secret', 'Write-only.', 'Database URL'),
          ],
        },
      ]}
    />
  )
}

export function RetrievalTab() {
  return (
    <SettingsForm
      sections={[
        {
          title: 'Chunking',
          fields: [f('rag.chunk_size', 'int', 'Characters per chunk.'), f('rag.chunk_overlap', 'int'), f('rag.markdown_headers', 'bool', 'Split on Markdown headings first.')],
        },
        {
          title: 'Search',
          fields: [
            f('rag.top_k', 'int', 'Chunks retrieved before reranking.', 'Top K'),
            f('rag.top_k_rerank', 'int', 'Chunks kept after reranking.', 'Top K after rerank'),
            f('rag.relevance_threshold', 'float', 'Drop chunks scoring below this (0 keeps all).'),
            f('rag.hybrid.bm25_weight', 'float', 'Weight of keyword search against vector search (0-1).', 'BM25 weight'),
            f('rag.full_context_max_chars', 'int', 'Largest collection sent whole in full-context mode.', 'Full context max chars'),
            f('rag.template', 'textarea', 'Template wrapping retrieved context (empty = built-in).', 'Template'),
            f('rag.placement', { select: [['system', 'In the system prompt'], ['data', 'As a reference message']] }, 'Where retrieved passages go. A reference message sits before the newest user message, is checked by the guardrails like tool output, and keeps the system prompt the same from turn to turn (better for provider caching).', 'Passage placement'),
          ],
        },
        {
          title: 'Embedding',
          hint: 'Changing the embedding model needs a reindex (kera reindex).',
          fields: [f('rag.embedding.connection', 'connection', undefined, 'Connection'), f('rag.embedding.model', 'text', undefined, 'Model'), f('rag.embedding.dim', 'int', undefined, 'Dimensions'), f('rag.embedding.batch', 'int', undefined, 'Batch size')],
        },
        {
          title: 'Document extractor',
          hint: 'Reads PDF, DOCX, PPTX and ODT uploads and OCRs scanned pages. The Docker Compose stack runs it as the extract service and supplies this address (KERA_EXTRACTOR_URL); Reset returns to that. Saved empty, extraction is off.',
          fields: [f('rag.extractor.url', 'text', 'Address of the extraction service.', 'Extractor URL')],
          footer: <ExtractorStatus />,
        },
        {
          title: 'Reranker',
          fields: [
            f('rag.reranker.url', 'text', 'Cohere-style /rerank endpoint: the sidecar or LiteLLM (empty = no reranking).', 'Reranker URL'),
            f('rag.reranker.model', 'text', 'e.g. a Cohere rerank model registered in LiteLLM.', 'Reranker model'),
            f('rag.reranker.api_key', 'secret', 'Bearer token for a hosted reranker (write-only).', 'Reranker API key'),
            f('rag.reranker.local_runs', { select: [['skip', 'Skip (fused order or local URL)'], ['allow', 'Allow']] }, 'Whether chats on local models may send passages to this reranker.', 'Local-model chats'),
            f('rag.reranker.local_url', 'text', 'Reranker used for local-model chats instead (empty = none).', 'Local reranker URL'),
          ],
        },
      ]}
    />
  )
}

// ---- Interface ---------------------------------------------------------------

interface Suggestion {
  title?: string[] | string
  content: string
}

export function InterfaceTab() {
  return (
    <SettingsForm
      sections={[
        {
          title: 'Site',
          fields: [
            f('site.name', 'text', undefined, 'Name'),
            f('site.url', 'text', 'Public URL (used in links and OAuth redirects).', 'URL'),
            f('site.timezone', 'text', 'IANA zone for dates in prompts, e.g. Asia/Kolkata.', 'Timezone'),
            f('models.base_public', 'bool', 'Discovered base models without a row are usable by everyone.', 'Base models public'),
          ],
        },
      ]}
    >
      {(d) => (
        <>
          <Card title="Sign-in" hint="How people get in.">
            <div className="divide-y divide-line">
              <PasswordSwitch d={d} />
            </div>
          </Card>
          <Card title="Models and suggestions" hint="What a new chat starts with.">
            <div className="divide-y divide-line">
              <SettingRow k="site.default_models" label="Default models" hint="Selected in a new chat (the first that a user may use)." d={d} wide>
                <DefaultModels d={d} />
              </SettingRow>
              <SettingRow k="site.model_order" label="Model order" hint="Drag to reorder the model picker. Models not listed follow by name." d={d} wide>
                <ModelOrder d={d} />
              </SettingRow>
              <SettingRow k="site.suggestions" label="Prompt suggestions" hint="Cards on the empty chat screen (a model's own suggestions win)." d={d} wide>
                <Suggestions d={d} />
              </SettingRow>
            </div>
          </Card>
        </>
      )}
    </SettingsForm>
  )
}

/** The switch for auth.password.enabled. Until it holds a value the server decides (on while there is no OAuth login provider), so the page shows that effective state. */
function PasswordSwitch({ d }: { d: Draft }) {
  const key = 'auth.password.enabled'
  const options = useAuthOptions().data
  const v = d.get(key)
  const on = typeof v === 'boolean' ? v : (options?.login_form ?? false)
  const noWay = !on && options?.providers.length === 0
  return (
    <SettingRow
      k={key}
      label="Password sign-in"
      hint={noWay ? 'Off, and no OAuth login provider is set up: nobody can sign in except with kera user login-link on the server.' : 'Email and password sign-in. On by default until you add an OAuth login provider.'}
      d={d}
    >
      <Switch checked={on} onChange={(x) => d.set(key, x)} label="Password sign-in" testId="password-signin-switch" />
    </SettingRow>
  )
}

const strs = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : [])

function DefaultModels({ d }: { d: Draft }) {
  const models = useModels().data ?? []
  const value = strs(d.get('site.default_models'))
  // Ids set but not listed (a model that is unreachable right now) stay visible.
  const options = [...models.map((m) => ({ id: m.id, name: m.name || m.id })), ...value.filter((id) => !models.some((m) => m.id === id)).map((id) => ({ id, name: `${id} (not available)` }))]
  return (
    <div className="flex max-h-56 flex-wrap gap-x-5 gap-y-2 overflow-y-auto">
      {options.map((m) => (
        <Check key={m.id} checked={value.includes(m.id)} onChange={(on) => d.set('site.default_models', on ? [...value, m.id] : value.filter((x) => x !== m.id))}>
          {m.name}
        </Check>
      ))}
    </div>
  )
}

function ModelOrder({ d }: { d: Draft }) {
  const models = useModels().data ?? []
  const order = strs(d.get('site.model_order'))
  const name = (id: string) => models.find((m) => m.id === id)?.name || id
  const rest = models.filter((m) => !order.includes(m.id))
  const [drag, setDrag] = useState<number | null>(null)
  const set = (v: string[]) => d.set('site.model_order', v)
  return (
    <div className="space-y-2">
      <ol className="divide-y divide-line rounded-lg border border-line" data-testid="model-order">
        {order.map((id, i) => (
          <li
            key={id}
            draggable
            onDragStart={() => setDrag(i)}
            onDragOver={(e) => {
              e.preventDefault()
              if (drag !== null && drag !== i) {
                set(move(order, drag, i))
                setDrag(i)
              }
            }}
            onDragEnd={() => setDrag(null)}
            className={cx('flex items-center gap-2 px-2 py-1 text-sm', drag === i && 'bg-accent-soft')}
          >
            <GripVertical size={14} className="cursor-grab text-muted" />
            <span className="w-6 text-right text-xs text-muted tabular-nums">{i + 1}</span>
            <span className="min-w-0 flex-1 truncate">
              {name(id)} {name(id) !== id && <span className="text-xs text-muted">{id}</span>}
            </span>
            <IconButton label={`Move ${id} up`} disabled={i === 0} onClick={() => set(move(order, i, i - 1))}>
              <ArrowUp size={14} />
            </IconButton>
            <IconButton label={`Move ${id} down`} disabled={i === order.length - 1} onClick={() => set(move(order, i, i + 1))}>
              <ArrowDown size={14} />
            </IconButton>
            <IconButton label={`Remove ${id} from the order`} onClick={() => set(order.filter((x) => x !== id))}>
              <X size={14} />
            </IconButton>
          </li>
        ))}
        {!order.length && <li className="p-3 text-sm text-muted">No explicit order: models are sorted by name.</li>}
      </ol>
      {rest.length > 0 && (
        <Select aria-label="Add a model to the order" value="" placeholder="Add a model…" onChange={(v) => v && set([...order, v])} options={rest.map((m) => ({ value: m.id, label: m.name || m.id }))} />
      )}
    </div>
  )
}

function Suggestions({ d }: { d: Draft }) {
  const list = (Array.isArray(d.get('site.suggestions')) ? d.get('site.suggestions') : []) as Suggestion[]
  const set = (v: Suggestion[]) => d.set('site.suggestions', v)
  const title = (s: Suggestion) => (Array.isArray(s.title) ? s.title : s.title ? [s.title] : [])
  const edit = (i: number, patch: Partial<Suggestion>) => set(list.map((s, j) => (j === i ? { ...s, ...patch } : s)))
  return (
    <div className="space-y-2">
      {list.map((s, i) => (
        <div key={i} className="grid gap-2 rounded-lg border border-line p-2 sm:grid-cols-[1fr_1fr_2fr_auto]" data-testid="suggestion-row">
          <input value={title(s)[0] ?? ''} placeholder="Title" aria-label="Suggestion title" onChange={(e) => edit(i, { title: [e.target.value, title(s)[1] ?? ''] })} className={inputCls} />
          <input value={title(s)[1] ?? ''} placeholder="Subtitle" aria-label="Suggestion subtitle" onChange={(e) => edit(i, { title: [title(s)[0] ?? '', e.target.value] })} className={inputCls} />
          <input value={s.content} placeholder="Prompt sent when clicked" aria-label="Suggestion prompt" onChange={(e) => edit(i, { content: e.target.value })} className={inputCls} />
          <IconButton label="Remove suggestion" onClick={() => set(list.filter((_, j) => j !== i))}>
            <X size={14} />
          </IconButton>
        </div>
      ))}
      <Btn onClick={() => set([...list, { title: ['', ''], content: '' }])}>
        <Plus size={14} /> Add suggestion
      </Btn>
    </div>
  )
}
