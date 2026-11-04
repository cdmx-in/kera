// Settings tabs that are plain forms over settings keys.
import { useState } from 'react'
import { ArrowDown, ArrowUp, GripVertical, Plus, X } from 'lucide-react'
import { useModels } from '@/api/queries'
import { IconButton } from '@/ui/Dialog'
import { cx } from '@/lib/format'
import { move } from '../lib'
import { Btn, Card, Check, inputCls } from '../ui'
import { SettingRow, SettingsForm, type Draft, type FieldSpec } from './form'

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
            f('tasks.autocomplete', 'bool', undefined, 'Composer autocomplete'),
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
          title: 'Memory',
          fields: [
            f('chat.memory.enable', 'bool', 'Let the assistant remember details across chats (users opt in).', 'Enable memory'),
            f('chat.memory.review_every', 'int', 'Review the conversation for memories every N user messages.', 'Review every'),
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
            f('web.search.engine', { select: [['duckduckgo', 'DuckDuckGo'], ['searxng', 'SearXNG'], ['brave', 'Brave']] }, undefined, 'Engine'),
            f('web.search.result_count', 'int', undefined, 'Results per search'),
            f('web.search.max_calls_per_turn', 'int', undefined, 'Searches per reply'),
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
          ],
        },
        {
          title: 'Embedding',
          hint: 'Changing the embedding model needs a reindex (kera reindex).',
          fields: [f('rag.embedding.connection', 'connection', undefined, 'Connection'), f('rag.embedding.model', 'text', undefined, 'Model'), f('rag.embedding.dim', 'int', undefined, 'Dimensions'), f('rag.embedding.batch', 'int', undefined, 'Batch size')],
        },
        {
          title: 'Reranker and extractor',
          fields: [
            f('rag.reranker.url', 'text', 'Rerank sidecar URL (empty = no reranking).', 'Reranker URL'),
            f('rag.reranker.model', 'text', undefined, 'Reranker model'),
            f('rag.extractor.url', 'text', 'Document extraction sidecar URL.', 'Extractor URL'),
            f('rag.extractor.model', 'text', undefined, 'Extractor model'),
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
      )}
    </SettingsForm>
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
        <select aria-label="Add a model to the order" value="" onChange={(e) => e.target.value && set([...order, e.target.value])} className={inputCls}>
          <option value="">Add a model…</option>
          {rest.map((m) => (
            <option key={m.id} value={m.id}>
              {m.name || m.id}
            </option>
          ))}
        </select>
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
