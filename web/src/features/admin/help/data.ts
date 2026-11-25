// The content of Admin > Help ("How Kera answers"): boxes, setting keys, defaults and the worked example.
// One source for the page, for the SVG diagrams (docs/images/how-it-answers-*.svg, shown in README.md and
// docs/how-it-answers.md) and for the Mermaid text versions in docs/how-it-answers.md.
// Tests keep them in step: help.test.tsx (page, simulator, the boxes, numbers and settings of the SVGs and the Mermaid ids)
// and internal/settings/help_data_test.go (every key and default below against internal/settings/defaults.go, every fixed
// number against the Go code, and the files of the SVGs).

/** Every setting the page names. `def` is the default as defaults.go holds it ('(empty)' for ""). */
export const SETTINGS = [
  { key: 'tasks.query', def: 'true', where: 'Settings > Tasks' },
  { key: 'tasks.title', def: 'true', where: 'Settings > Tasks' },
  { key: 'tasks.tags', def: 'true', where: 'Settings > Tasks' },
  { key: 'tasks.follow_ups', def: 'true', where: 'Settings > Tasks' },
  { key: 'chat.compaction.enable', def: 'true', where: 'Settings > Chat' },
  { key: 'chat.compaction.threshold', def: '120000', where: 'Settings > Chat' },
  { key: 'chat.compaction.retention', def: '30', where: 'Settings > Chat' },
  { key: 'chat.context.digest', def: 'true', where: 'Settings > Chat' },
  { key: 'chat.memory.enable', def: 'false', where: 'Settings > Chat' },
  { key: 'chat.memory.review_every', def: '5', where: 'Settings > Chat' },
  { key: 'chat.context.enable', def: 'true', where: 'Settings > Chat' },
  { key: 'chat.context.window_local', def: '16000', where: 'Settings > Chat' },
  { key: 'chat.context.clear_at', def: '0.7', where: 'Settings > Chat' },
  { key: 'chat.context.clear_to', def: '0.5', where: 'Settings > Chat' },
  { key: 'chat.context.keep_rounds', def: '4', where: 'Settings > Chat' },
  { key: 'chat.context.compact', def: 'true', where: 'Settings > Chat' },
  { key: 'tools.spill_chars', def: '0', where: 'Settings > Chat' },
  { key: 'chat.model_retries', def: '2', where: 'Settings > Chat' },
  { key: 'chat.repeat_limit', def: '3', where: 'Settings > Chat' },
  { key: 'chat.parallel_tools', def: 'true', where: 'Settings > Chat' },
  { key: 'chat.max_tool_iterations', def: '30', where: 'Settings > Chat' },
  { key: 'cowork.max_tool_iterations', def: '100', where: 'Config file (kera apply)' },
  { key: 'tools.max_result_chars', def: '100000', where: 'Settings API; each tool server has its own' },
  { key: 'web.search.max_calls_per_turn', def: '3', where: 'Settings > Web search' },
  { key: 'web.fetch.max_calls_per_turn', def: '3', where: 'Settings > Web search' },
  { key: 'cowork.web.max_searches', def: '15', where: 'Settings > Web search' },
  { key: 'cowork.web.max_fetches', def: '15', where: 'Settings > Web search' },
  { key: 'rag.full_context_max_chars', def: '400000', where: 'Settings > Retrieval' },
  { key: 'rag.top_k', def: '30', where: 'Settings > Retrieval' },
  { key: 'rag.top_k_rerank', def: '8', where: 'Settings > Retrieval' },
  { key: 'rag.fuse_queries', def: 'true', where: 'Settings > Retrieval' },
  { key: 'rag.hybrid.bm25_weight', def: '0.5', where: 'Settings > Retrieval' },
  { key: 'rag.hybrid.head_weight', def: '0.25', where: 'Settings > Retrieval' },
  { key: 'rag.embedding.model', def: 'nomic-embed-text', where: 'Settings > Retrieval' },
  { key: 'rag.embedding.query_prefix', def: '(empty)', where: 'Settings > Retrieval' },
  { key: 'rag.reranker.url', def: '(empty)', where: 'Settings > Retrieval' },
  { key: 'rag.reranker.local_url', def: '(empty)', where: 'Settings > Retrieval' },
  { key: 'rag.reranker.local_runs', def: 'skip', where: 'Settings > Retrieval' },
  { key: 'rag.relevance_threshold', def: '0', where: 'Settings > Retrieval' },
  { key: 'rag.min_results', def: '3', where: 'Settings > Retrieval' },
  { key: 'rag.neighbors', def: '1', where: 'Settings > Retrieval' },
  { key: 'rag.passage_max_chars', def: '3000', where: 'Settings > Retrieval' },
  { key: 'rag.placement', def: 'system', where: 'Settings > Retrieval' },
  { key: 'rag.template', def: '(empty)', where: 'Settings > Retrieval' },
  { key: 'guardrails.pii', def: 'log', where: 'Settings > Guardrails' },
  { key: 'guardrails.injection', def: 'log', where: 'Settings > Guardrails' },
  { key: 'guardrails.blocklist', def: '[]', where: 'Settings > Guardrails' },
  { key: 'guardrails.moderation', def: 'off', where: 'Settings > Guardrails' },
  { key: 'guardrails.local_exempt', def: 'true', where: 'Settings > Guardrails' },
  { key: 'guardrails.output_pii', def: 'off', where: 'Settings > Guardrails' },
  { key: 'trace.enable', def: 'true', where: 'Settings > Chat' },
  { key: 'trace.retention_days', def: '30', where: 'Settings > Chat' },
  { key: 'audit.retention_days', def: '365', where: 'Audit page' },
] as const

export type SettingKey = (typeof SETTINGS)[number]['key']

const bySetting = new Map<string, (typeof SETTINGS)[number]>(SETTINGS.map((s) => [s.key, s]))
export const setting = (key: SettingKey) => bySetting.get(key)!
/** The default of a numeric setting. */
export const num = (key: SettingKey) => Number(setting(key).def)

/** Numbers that are fixed in the Go code, not settings. The Go test finds each snippet in its file. */
export const FIXED = [
  { what: 'Share of the window at which an overflow is suspected', file: 'internal/engine/budget.go', snippet: 'overflowAt = 0.97' },
  { what: 'Rounds a mid-run summary leaves untouched', file: 'internal/engine/budget.go', snippet: 'compactKeepRounds = 2' },
  { what: 'Smallest automatic spill size (characters)', file: 'internal/engine/budget.go', snippet: 'b.spillAt = max(8000, b.window/2)' },
  { what: 'Characters kept from the start of a spilled result', file: 'internal/engine/spill.go', snippet: 'spillHead = 2000' },
  { what: 'Characters kept from the end of a spilled result', file: 'internal/engine/spill.go', snippet: 'spillTail = 1000' },
  { what: 'Calls of one round that run at once', file: 'internal/engine/toolloop.go', snippet: 'maxParallel = 4' },
  { what: 'Constant of the rank fusion', file: 'internal/knowledge/search.go', snippet: 'rrfK = 60' },
  { what: 'Queries fused in one search', file: 'internal/knowledge/search.go', snippet: 'maxQueries = 5' },
  { what: 'Minutes a failed reranker is skipped', file: 'internal/knowledge/search.go', snippet: 'rerankCooldown = 5 * time.Minute' },
  { what: 'Queries the task model writes for a follow-up', file: 'internal/engine/tasks.go', snippet: 'len(qs) < 3' },
] as const

/** Constants the page shows next to the defaults. */
export const OVERFLOW_AT = 0.97
export const SPILL_MIN_CHARS = 8000
export const RRF_K = 60

export type DocId = 'answers' | 'retrieval' | 'context' | 'trace' | 'audit' | 'guardrails'
const REPO = 'https://github.com/cdmx-in/kera/blob/main/'
export const DOCS: Record<DocId, { title: string; path: string; blurb: string }> = {
  answers: { title: 'How Kera answers', path: 'docs/how-it-answers.md', blurb: 'This page as animated diagrams, for reading on GitHub.' },
  retrieval: { title: 'How knowledge search works', path: 'docs/retrieval.md', blurb: 'The ranking steps, their settings, what an upgrade needs, and how to check a change on your own questions.' },
  context: { title: 'Context management inside a run', path: 'docs/context-management.md', blurb: 'Window tracking, spilling, clearing, mid-run summaries, retries, the round cap and the repeat guard.' },
  trace: { title: 'Run trace and prompt layout', path: 'docs/run-trace.md', blurb: 'What each step of a trace records, and where retrieved passages go in the prompt.' },
  audit: { title: 'Audit log', path: 'docs/audit.md', blurb: 'What the audit log records and leaves out, retention and export.' },
  guardrails: { title: 'Guardrails', path: 'README.md#guardrails', blurb: 'The checks on every model call, with their modes.' },
}
export const docUrl = (id: DocId) => REPO + DOCS[id].path

/** A box of a diagram: what it does, the settings that steer it, where to look, what to read. */
export interface Box {
  id: string
  /** The label of the box in the SVG and Mermaid diagrams. Plain words, no punctuation that Mermaid reads. */
  short: string
  title: string
  summary: string
  what: string[]
  keys: SettingKey[]
  /** Notes on settings or numbers that have no key (a per-assistant option, a fixed limit). */
  notes?: string[]
  look: string[]
  doc: DocId
  kind?: 'guard' | 'model' | 'store' | 'decision'
  /** Loop exits only: the condition that takes the run here. */
  when?: string
}

// ---- A. How a question is answered ------------------------------------------------------------------------------

export const FLOW: Box[] = [
  {
    id: 'ask',
    short: 'User message',
    title: 'The question arrives',
    summary: 'Kera finds the assistant, saves your message and starts a run.',
    what: [
      'You send a message from the web app or the API. Kera finds the assistant (the model preset) you chose and saves your message and an empty reply.',
      'The run is detached from your browser. Closing the tab does not stop it.',
      'Hook servers, if you have any, can change the message first (inlet hooks) and the reply last (outlet hooks).',
    ],
    keys: [],
    notes: ['Hook servers are set in Settings > Hook servers.'],
    look: ['Admin > Traces lists the run as soon as it finishes.'],
    doc: 'trace',
  },
  {
    id: 'context',
    short: 'Build context',
    title: 'Build the context',
    summary: 'System prompt, attached files, earlier turns, and a summary when the chat is long.',
    what: [
      'Kera puts together what the model will read: the assistant’s system prompt (plus the task prompt for a Cowork task), a list of the attached files, and the earlier turns of the chat as text.',
      'An earlier reply that used tools carries a short digest of the steps it took. A hosted model gets the tool names only.',
      'When the chat is long, older turns are replaced by a summary (compaction between turns). The newest part of the chat stays as written.',
      'Memory is not copied into the prompt. The model reads it only when it calls a memory tool, and memory is off until an admin turns it on and the user opts in.',
    ],
    keys: ['chat.compaction.enable', 'chat.compaction.threshold', 'chat.compaction.retention', 'chat.context.digest', 'chat.memory.enable'],
    notes: ['chat.compaction.retention is the share of turns (in percent) kept as written. For a local model, or a model that declares its window, the threshold is 70% of the window instead of chat.compaction.threshold.'],
    look: ['In Admin > Traces, a compaction step named summarise shows the turns summarised and the estimated tokens before and after.', 'The first model step shows the messages and characters sent.'],
    doc: 'context',
  },
  {
    id: 'mode',
    short: 'Knowledge mode',
    title: 'Decide how to use knowledge',
    summary: 'Each assistant has a knowledge mode: retrieve, tools or full.',
    kind: 'decision',
    what: [
      'Each knowledge collection of an assistant has a mode (Workspace > Models > Knowledge): relevant parts in the prompt (retrieve), the model searches with tools (tools), whole documents in the prompt (full), or automatic.',
      'Automatic means retrieve when a reranker works. Without one it means tools if the run has tool servers, and otherwise retrieve.',
      'Full puts whole documents in the prompt, up to a size limit.',
      'In tools mode nothing is added to the prompt. The model calls list_knowledge, search_knowledge_files, query_knowledge_files or view_knowledge_file when it decides to. These tools are offered in every mode when the assistant has knowledge.',
    ],
    keys: ['rag.full_context_max_chars', 'rag.reranker.url'],
    notes: ['The mode itself is an option of the assistant, not a setting.'],
    look: ['A run in retrieve or full mode has a retrieval step in Admin > Traces. A run in tools mode has none, and calls to query_knowledge_files show as tool steps.'],
    doc: 'retrieval',
  },
  {
    id: 'retrieve',
    short: 'Retrieve passages',
    title: 'Retrieve passages',
    summary: 'Vector, keyword and heading search, fused, optionally reranked, widened and numbered.',
    what: [
      'Retrieve mode searches the collections the user can read. The next section follows one question through every step with example numbers.',
      'The result is a short list of numbered passages, best first, at most rag.top_k_rerank of them before neighbours are added.',
    ],
    keys: ['tasks.query', 'rag.top_k', 'rag.top_k_rerank', 'rag.fuse_queries', 'rag.neighbors', 'rag.reranker.url'],
    look: ['Admin > Traces, retrieval step: collections searched, passages found, the top scores, and whether the search was reranked.', 'A search that found nothing is marked as a warning in the waterfall.'],
    doc: 'retrieval',
  },
  {
    id: 'place',
    short: 'Place passages',
    title: 'Place the passages in the prompt',
    summary: 'As text in the system prompt, or as a separate data message.',
    what: [
      'With placement system, the numbered passages go inside the system prompt, with an instruction to cite by number and to say so when the sources lack the answer.',
      'With placement data, the system prompt keeps only the instructions. The passages become their own message just before your newest message, marked as untrusted reference material. They are built for each run and are not saved with the chat.',
      'Data placement keeps the system prompt the same from turn to turn, so providers that cache the start of a prompt can reuse it. The guardrails also check that message like tool output.',
    ],
    keys: ['rag.placement', 'rag.template'],
    look: ['Compare the cached share of follow-up turns in Admin > Traces before and after switching.', 'Guardrail events named injection.data count injection phrases found in a data message.'],
    doc: 'trace',
  },
  {
    id: 'guardIn',
    short: 'Guardrails in',
    title: 'Guardrails on the way in',
    summary: 'Every call to a model is scanned first, in every round.',
    kind: 'guard',
    what: [
      'Every call to a model passes through the guard. It scans the messages and tool results that are about to be sent: personal data (log, redact or block), injection phrases, an admin blocklist, and optional moderation.',
      'It runs again before each later round, so tool results are scanned too. The system prompt is skipped. Local models are exempt by default.',
    ],
    keys: ['guardrails.pii', 'guardrails.injection', 'guardrails.blocklist', 'guardrails.moderation', 'guardrails.local_exempt'],
    look: ['Settings > Guardrails > Recent events lists what the guard saw, by rule name and count. The matched text is never stored.', 'A blocked call fails with the code guardrail_blocked.'],
    doc: 'guardrails',
  },
  {
    id: 'model',
    short: 'Call the model',
    title: 'Call the model',
    summary: 'The model reads the messages and the tool definitions, then answers or asks for tools.',
    kind: 'model',
    what: [
      'The model gets the messages and the definitions of every tool the run has, and streams its reply. It either answers or asks for tool calls.',
      'A call that fails before any output (429, a 5xx error, a connection error or a timeout) is repeated after 1 s and then 2 s. A failure after the first output is never repeated, because the text is already on screen.',
    ],
    keys: ['chat.model_retries'],
    look: ['Admin > Traces, model step: prompt, cached and completion tokens, stop reason, tools offered, and the attempt number of a retry.'],
    doc: 'context',
  },
  {
    id: 'tools',
    short: 'Run tool calls',
    title: 'Run the tool calls',
    summary: 'Built-in tools, tool servers and skills; read-only calls run together.',
    what: [
      'Tools are built-in sets (web, knowledge, files, memory, code), tool servers (OpenAPI or MCP, the Model Context Protocol) and skills. A skill adds only its name and description to the prompt; its text loads when the model asks.',
      'The calls of one round run together, at most 4 at once, only when there are several and all belong to the web, knowledge or files sets. Any other tool makes the round run one call after another. Results keep the order of the calls.',
      'The same tool with the same arguments three times in a row is not run the third time. The model is told to try something else.',
      'A tool server cuts a result at its size cap and says so in the result.',
    ],
    keys: ['chat.parallel_tools', 'chat.repeat_limit', 'tools.max_result_chars', 'web.search.max_calls_per_turn', 'web.fetch.max_calls_per_turn', 'cowork.web.max_searches', 'cowork.web.max_fetches'],
    notes: ['The web budgets of a chat reply are the two per-turn keys; a Cowork task uses cowork.web.max_searches and cowork.web.max_fetches.'],
    look: ['Admin > Traces, tool steps: name, server, size of arguments and result, failed, truncated, and repeat_refused.', 'A failed tool step is red; a truncated one is amber.'],
    doc: 'context',
  },
  {
    id: 'window',
    short: 'Fit to window',
    title: 'Keep the prompt inside the window',
    summary: 'Spill long results to files, clear old results, then summarise the run.',
    what: [
      'Right after a tool runs, a result longer than the spill size is stored as a file. The model gets the start, the end and the file name, and can read the rest with view_file. The file tools join a run automatically when it has other tools.',
      'Before each call, Kera estimates the prompt size. Above 70% of the window, the results of rounds older than the newest 4 are replaced by one line each, oldest first, down to 50%.',
      'If the prompt is still above 70%, the run so far is summarised by the task model and the summary replaces it. The newest 2 rounds stay.',
      'When a call reports 97% or more of the window, the trace records overflow_suspected. Ollama silently drops the oldest messages of a prompt that is too long, so this is the only sign.',
    ],
    keys: ['chat.context.enable', 'chat.context.window_local', 'chat.context.clear_at', 'chat.context.clear_to', 'chat.context.keep_rounds', 'chat.context.compact', 'tools.spill_chars'],
    notes: ['The window comes from the model parameter context_window, then num_ctx, then the setting: window_local for a local model, chat.compaction.threshold for a hosted one.'],
    look: ['Admin > Traces, steps named spill, clear, summarise_run and overflow_suspected, with the tokens before and after.'],
    doc: 'context',
  },
  {
    id: 'answer',
    short: 'Answer with citations',
    title: 'Answer with citations',
    summary: 'The loop ends when the model answers without tool calls, or at the round cap.',
    what: [
      'When the model answers without tool calls, the loop ends. At the round cap the model gets one more call without tools and a line asking for a final report, so a long task ends with a report instead of a status line.',
      'Passages are numbered once per run. The same passage keeps its number in the prompt and in later searches, so [3] means one passage everywhere. The web app turns [n] into a link to the sources list.',
    ],
    keys: ['chat.max_tool_iterations', 'cowork.max_tool_iterations'],
    look: ['Admin > Traces, last model step: stop reason stop, or round_cap when the cap was reached.'],
    doc: 'context',
  },
  {
    id: 'guardOut',
    short: 'Guardrails out',
    title: 'Guardrails on the way out',
    summary: 'The reply is scanned as it streams.',
    kind: 'guard',
    what: [
      'The reply is checked for personal data while it streams. It is off by default. With redact, a short tail of the stream is held back so that a value split across chunks is still caught.',
    ],
    keys: ['guardrails.output_pii'],
    look: ['Settings > Guardrails > Recent events, rules marked (reply).'],
    doc: 'guardrails',
  },
  {
    id: 'save',
    short: 'Save and trace',
    title: 'Saved and traced',
    summary: 'The reply, a usage row and the trace are stored; the audit log records admin changes.',
    kind: 'store',
    what: [
      'The reply is saved with the chat after each round and at the end. A usage row (tokens and time) is written. The steps of the run are written to the trace in one batch.',
      'Background tasks then run: a title, tags and follow-up suggestions. Every few user messages a memory review runs if memory is on.',
      'The audit log records what admins change (settings, secrets, users and so on). It does not record chat answers. What the guardrails did to model calls is in the guardrail events.',
    ],
    keys: ['trace.enable', 'trace.retention_days', 'tasks.title', 'tasks.tags', 'tasks.follow_ups', 'chat.memory.review_every', 'audit.retention_days'],
    look: ['Admin > Traces for the run, Admin > Audit for admin changes, Settings > Guardrails > Recent events for redaction.', 'A trace holds sizes, counts and timings. It never holds prompts, answers, tool arguments or tool results.'],
    doc: 'trace',
  },
]

/** The branches out of the knowledge decision, for the diagram. */
export const MODE_BRANCHES = [
  { mode: 'retrieve', goes: 'Search the collections, then place the passages' },
  { mode: 'full', goes: 'Whole documents go straight to placement' },
  { mode: 'tools', goes: 'Nothing is added; the model searches later with tools' },
]

/** Ids of the boxes inside the loop (each round repeats them). */
export const LOOP_BOXES = ['guardIn', 'model', 'tools', 'window']

// ---- B. The retrieval pipeline ---------------------------------------------------------------------------------

export const RETRIEVAL: Box[] = [
  {
    id: 'q',
    short: 'Queries',
    title: 'Write the queries',
    summary: 'The first question as typed; later turns get up to three keyword queries.',
    what: [
      'On the first turn of a chat, the search uses your message as written.',
      'On later turns, the task model writes one to three keyword queries from the conversation, so a follow-up such as “and the second one?” still searches for something useful. The tool query_knowledge_files sends the model’s own query instead.',
      'Up to five queries are searched together. The reranker, when there is one, judges every passage against the first query.',
    ],
    keys: ['tasks.query'],
    look: ['Queries are not stored in the trace. Admin > Traces shows the retrieval step that followed them.'],
    doc: 'retrieval',
  },
  {
    id: 'vec',
    short: 'Vector search',
    title: 'Vector search',
    summary: 'Finds passages that mean something similar.',
    what: [
      'An embedding model turns the query into a list of numbers. Kera returns the chunks whose numbers are nearest, which finds passages that say the same thing in other words.',
      'It keeps the best rag.top_k candidates (30).',
    ],
    keys: ['rag.top_k', 'rag.embedding.model', 'rag.embedding.query_prefix'],
    notes: ['Changing the embedding model or its prefixes needs kera reindex.'],
    look: ['If an exact word is missing from the answer, check the keyword rank before blaming this step.'],
    doc: 'retrieval',
  },
  {
    id: 'kw',
    short: 'Keyword search',
    title: 'Keyword search',
    summary: 'Finds passages that contain the words.',
    what: [
      'Postgres full-text search over the text of each chunk, with the words of the query joined by OR. It finds exact words, codes and names that a vector search can miss.',
      'It keeps the best rag.top_k candidates (30). Its weight against vector search is rag.hybrid.bm25_weight.',
    ],
    keys: ['rag.hybrid.bm25_weight', 'rag.top_k'],
    notes: ['The setting is named bm25, but the ranking is Postgres ts_rank_cd, not BM25.'],
    look: ['The weight is a share from 0 to 1: keyword gets that share and vector gets the rest.'],
    doc: 'retrieval',
  },
  {
    id: 'head',
    short: 'Heading search',
    title: 'Heading and file name search',
    summary: 'Finds chunks whose section title or file name matches.',
    what: [
      'A third keyword ranking looks at the file name and the heading path of each chunk (for example Returns > Time limit). A chunk can say the right thing under a heading that a short question names.',
      'Its weight is rag.hybrid.head_weight. Set it to 0 to switch it off.',
    ],
    keys: ['rag.hybrid.head_weight'],
    notes: ['Chunks indexed before this existed have no heading row until you run kera reindex --heads.'],
    look: ['New and re-indexed files get heading rows when they are indexed.'],
    doc: 'retrieval',
  },
  {
    id: 'fuse',
    short: 'Fuse rankings',
    title: 'Fuse the rankings',
    summary: 'Reciprocal rank fusion adds up each passage’s rank in every list.',
    what: [
      'Reciprocal rank fusion (RRF) gives each passage a score from each list it appears in: weight / (60 + rank). The scores are added. A passage that is second in two lists beats one that is first in only one.',
      'With rag.fuse_queries on, the lists of all queries go into one fusion. Off, each query is searched alone and the results are joined.',
      'The best rag.top_k fused passages go on.',
    ],
    keys: ['rag.fuse_queries', 'rag.hybrid.bm25_weight', 'rag.hybrid.head_weight', 'rag.top_k'],
    notes: ['Vector weight is 1 minus rag.hybrid.bm25_weight. The heading weight is added on top, not taken from the other two. The constant 60 is fixed.'],
    look: ['Admin > Traces, retrieval step: the top scores. Without a reranker these are fusion scores, which are small numbers by design.'],
    doc: 'retrieval',
  },
  {
    id: 'rerank',
    short: 'Rerank (optional)',
    title: 'Rerank (optional)',
    summary: 'A reranker model re-reads the candidates against the question.',
    what: [
      'When a reranker is configured, it reads the first query and each candidate together and gives a relevance score. That is a closer reading than adding up ranks.',
      'Passages below rag.relevance_threshold are dropped, but never below rag.min_results. A threshold ranks and never empties the answer. The best rag.top_k_rerank are kept. The default threshold is 0, which keeps all.',
      'Runs on a local model do not send passages to an external reranker unless rag.reranker.local_runs is allow. They can use rag.reranker.local_url.',
      'If the reranker fails, Kera uses the fused order for 5 minutes. Without a reranker, the best rag.top_k_rerank of the fused list are kept.',
    ],
    keys: ['rag.reranker.url', 'rag.reranker.local_url', 'rag.reranker.local_runs', 'rag.relevance_threshold', 'rag.min_results', 'rag.top_k_rerank'],
    look: ['Admin > Traces, retrieval step: reranked yes or no.', 'A reranker adds time to every search; the waterfall shows how much.'],
    doc: 'retrieval',
  },
  {
    id: 'nb',
    short: 'Add neighbours',
    title: 'Add the neighbouring chunks',
    summary: 'Each kept chunk is widened by the chunks before and after it.',
    what: [
      'A chunk is a cut of about 1,000 characters. Kera adds the rag.neighbors chunks before and after each kept chunk from the same file, so the model reads the passage, not a fragment.',
      'Chunks that touch are joined into one passage, and the overlap two chunks share is written once. A passage is cut at rag.passage_max_chars. Files come in the order of their best hit, and the passages of a file in document order.',
      'rag.neighbors set to 0 returns the lone chunk.',
    ],
    keys: ['rag.neighbors', 'rag.passage_max_chars'],
    look: ['A larger neighbour count makes the prompt bigger. Check the prompt tokens of the first model step.'],
    doc: 'retrieval',
  },
  {
    id: 'num',
    short: 'Number citations',
    title: 'Number the passages',
    summary: 'Passages become [1] to [n], and a number never changes during the run.',
    what: [
      'Each passage gets a citation number. The passages in the prompt and the results of every later search share one numbering, so [3] is the same passage wherever the model read it and in the sources list of the answer.',
      'A passage found again by a later search keeps its number.',
    ],
    keys: [],
    look: ['In the answer, click a number to open its source.'],
    doc: 'retrieval',
  },
]

// ---- The worked example ----------------------------------------------------------------------------------------

export interface Candidate {
  id: string
  file: string
  heading: string
  text: string
  chunk: number
  /** 1-based ranks in each list; null = not in the list. */
  vec: number | null
  kw: number | null
  head: number | null
  rerank: number
}

/** Illustrative: invented passages and invented ranks. Only the arithmetic is real. */
export const EXAMPLE = {
  question: 'How many days do I have to return an item?',
  threshold: 0.5,
  weights: { vec: 0.5, kw: 0.5, head: 0.25 },
  candidates: [
    { id: 'A', file: 'returns.md', heading: 'Returns > Time limit', text: 'You can return an item within 30 days of delivery.', chunk: 4, vec: 2, kw: 3, head: 1, rerank: 0.92 },
    { id: 'B', file: 'shipping.md', heading: 'Shipping > Delivery times', text: 'Delivery takes 3 to 5 days.', chunk: 1, vec: 3, kw: 1, head: null, rerank: 0.12 },
    { id: 'C', file: 'returns.md', heading: 'Returns > Refunds', text: 'Refunds are paid within 10 days after we get the item back.', chunk: 8, vec: 1, kw: 2, head: 3, rerank: 0.41 },
    { id: 'D', file: 'warranty.md', heading: 'Warranty', text: 'Items carry a 12 month warranty.', chunk: 7, vec: 5, kw: null, head: null, rerank: 0.03 },
    { id: 'E', file: 'terms.md', heading: 'Terms > Exceptions', text: 'Sale items cannot be returned.', chunk: 2, vec: 4, kw: null, head: 2, rerank: 0.3 },
  ] as Candidate[],
}

/** weight / (k + rank) summed over the lists the candidate is in. */
export function fusedScore(c: Candidate, w = EXAMPLE.weights, k = RRF_K): number {
  const part = (rank: number | null, weight: number) => (rank === null ? 0 : weight / (k + rank))
  return part(c.vec, w.vec) + part(c.kw, w.kw) + part(c.head, w.head)
}

/** The sum written out, for the page: "0.5/(60+2) + 0.5/(60+3) + 0.25/(60+1)". */
export function fusedSum(c: Candidate, w = EXAMPLE.weights, k = RRF_K): string {
  const parts: string[] = []
  if (c.vec !== null) parts.push(`${w.vec}/(${k}+${c.vec})`)
  if (c.kw !== null) parts.push(`${w.kw}/(${k}+${c.kw})`)
  if (c.head !== null) parts.push(`${w.head}/(${k}+${c.head})`)
  return parts.join(' + ')
}

export interface Passage {
  n: number
  file: string
  from: number
  to: number
  best: string
}

/** Rerank as the code does: sort by score, keep those at or above the threshold, but at least minResults. */
export function rerankKeep(cs: Candidate[], threshold: number, minResults: number, topK: number): { kept: Candidate[]; byFloor: string[] } {
  const sorted = [...cs].sort((a, b) => b.rerank - a.rerank)
  const above = sorted.filter((c) => c.rerank >= threshold).length
  const keep = Math.min(Math.max(above, Math.min(minResults, sorted.length)), topK)
  return { kept: sorted.slice(0, keep), byFloor: sorted.slice(above, keep).map((c) => c.id) }
}

/** Widen each kept chunk by n neighbours; join touching runs; files in the order of their best hit. */
export function passagesOf(kept: Candidate[], n: number): Passage[] {
  const out: Omit<Passage, 'n'>[] = []
  const files: string[] = []
  for (const c of kept) if (!files.includes(c.file)) files.push(c.file)
  for (const file of files) {
    const runs = kept
      .filter((c) => c.file === file)
      .map((c) => ({ from: Math.max(0, c.chunk - n), to: c.chunk + n, best: c.id }))
      .sort((a, b) => a.from - b.from)
    const joined: typeof runs = []
    for (const r of runs) {
      const last = joined[joined.length - 1]
      if (last && r.from <= last.to + 1) last.to = Math.max(last.to, r.to)
      else joined.push({ ...r })
    }
    for (const r of joined) out.push({ file, ...r })
  }
  return out.map((p, i) => ({ ...p, n: i + 1 }))
}

// ---- C. The agent loop -----------------------------------------------------------------------------------------

export const LOOP: Box[] = [
  {
    id: 'modelCall',
    short: 'Model call',
    title: 'Model call',
    summary: 'The model reads everything so far and answers or asks for tools.',
    kind: 'model',
    what: [
      'One round starts with a call to the model with the system prompt, the turns, any retrieved passages, the tool definitions and everything the earlier rounds added.',
      'Before the call, Kera fits the prompt to the window (see the meter below). Every call also goes through the guardrails.',
    ],
    keys: ['chat.max_tool_iterations', 'cowork.max_tool_iterations'],
    look: ['Admin > Traces, model steps, one per round.'],
    doc: 'context',
  },
  {
    id: 'run',
    short: 'Run tool calls',
    title: 'Run the tool calls',
    summary: 'Each call runs; read-only calls of one round run together.',
    what: [
      'The calls the model asked for are run. Up to 4 read-only calls (web, knowledge, files) run at once. Others run one after another.',
      'An unknown tool name or a failed tool comes back to the model as an error text, so the model can try again or change course.',
      'The repeat guard refuses the third identical call in a row.',
    ],
    keys: ['chat.parallel_tools', 'chat.repeat_limit'],
    look: ['Tool steps with is_error or repeat_refused in Admin > Traces.'],
    doc: 'context',
  },
  {
    id: 'results',
    short: 'Results',
    title: 'Results go back',
    summary: 'Long results become files; the rest is appended in full.',
    what: [
      'Each result is added to the conversation as a tool message. A result longer than the spill size is stored as a file and the model sees its start, its end and the file name.',
      'A tool server cuts any result at its own size cap.',
    ],
    keys: ['tools.spill_chars', 'tools.max_result_chars'],
    look: ['A spill step and the truncated flag of a tool step in Admin > Traces.'],
    doc: 'context',
  },
  {
    id: 'fit',
    short: 'Fit to window',
    title: 'Fit the prompt to the window',
    summary: 'Clear old results at 70%, summarise if that is not enough.',
    what: [
      'Before the next call: above clear_at of the window, old results are cleared down to clear_to. If the prompt is still above clear_at, the run so far is summarised. The system prompt, earlier turns and your request are never touched.',
      'A cleared result keeps its message, so every tool call still has an answer. If the summary call fails, the run goes on and clears harder.',
    ],
    keys: ['chat.context.enable', 'chat.context.clear_at', 'chat.context.clear_to', 'chat.context.keep_rounds', 'chat.context.compact'],
    look: ['Steps named clear and summarise_run in Admin > Traces.'],
    doc: 'context',
  },
  {
    id: 'retry',
    when: 'The call failed before any output, and tries are left. The run goes back to the model call.',
    short: 'Retry',
    title: 'Retry a failed call',
    summary: 'A call that failed before any output is tried again.',
    what: [
      'A model call that fails before any output, with a 429, a 5xx error, a connection error or a timeout, is repeated up to chat.model_retries times, after 1 s and then 2 s.',
      'Each try is its own model step in the trace. A failure after the first output is not repeated.',
    ],
    keys: ['chat.model_retries'],
    look: ['Model steps with an attempt number, and will_retry on the one that failed.'],
    doc: 'context',
  },
  {
    id: 'final',
    when: 'A round came back with no tool calls.',
    short: 'Final answer',
    title: 'Final answer',
    summary: 'The model answered without tool calls.',
    what: ['A round with no tool calls ends the loop. The reply is the answer, with its citations.'],
    keys: [],
    look: ['The last model step has stop reason stop.'],
    doc: 'context',
  },
  {
    id: 'cap',
    when: 'The number of rounds reached the cap.',
    short: 'Round cap report',
    title: 'Round cap, then a final report',
    summary: 'After the last allowed round, one more call without tools asks for a report.',
    what: [
      'At chat.max_tool_iterations rounds (30 in a chat, 100 in a task) the model gets one more call, without tools, with a line asking it to write what was done, what was found and what is still open.',
      'If that call fails, the run still ends normally and keeps the work that was saved.',
    ],
    keys: ['chat.max_tool_iterations', 'cowork.max_tool_iterations'],
    look: ['The last model step has stop reason round_cap and the flag final_report.'],
    doc: 'context',
  },
  {
    id: 'fail',
    when: 'The retries are used up, or the failure came after output began.',
    short: 'Run fails',
    title: 'Error',
    summary: 'The call kept failing, or failed after output began.',
    what: [
      'When the retries are used up, or a failure comes after output began, the run ends with an error code (for example provider_error or timeout). The work saved so far stays in the chat.',
      'A trace step stores the short code and the HTTP status, never the server’s message.',
    ],
    keys: ['chat.model_retries'],
    look: ['A red model step in Admin > Traces.'],
    doc: 'trace',
  },
]

// ---- The window simulator --------------------------------------------------------------------------------------

export const WINDOWS = [
  { id: 'local', label: 'Local model, no declared window', tokens: num('chat.context.window_local'), key: 'chat.context.window_local' as SettingKey },
  { id: 'hosted', label: 'Hosted model, no declared window', tokens: num('chat.compaction.threshold'), key: 'chat.compaction.threshold' as SettingKey },
]

export type Zone = 'quiet' | 'clear' | 'overflow'

export interface Sim {
  zone: Zone
  tokens: number
  clearMark: number
  clearTarget: number
  overflowMark: number
  spillChars: number
}

/** What the loop does at a prompt of `tokens` in a window, with the default settings. */
export function simulate(window: number, tokens: number): Sim {
  const clearMark = Math.round(num('chat.context.clear_at') * window)
  const overflowMark = Math.round(OVERFLOW_AT * window)
  const zone: Zone = tokens >= overflowMark ? 'overflow' : tokens > clearMark ? 'clear' : 'quiet'
  return {
    zone,
    tokens,
    clearMark,
    clearTarget: Math.round(num('chat.context.clear_to') * window),
    overflowMark,
    spillChars: Math.max(SPILL_MIN_CHARS, Math.round(window / 2)),
  }
}

// ---- D. What to tune and what to measure ----------------------------------------------------------------------

export interface Symptom {
  symptom: string
  look: string
  keys: SettingKey[]
  measure: string
}

export const TUNING: Symptom[] = [
  {
    symptom: 'The answer misses a fact that is in the documents',
    look: 'Admin > Traces, retrieval step: how many passages were found, the top scores, and whether the search was reranked. Then read the sources under the answer.',
    keys: ['rag.top_k_rerank', 'rag.neighbors', 'rag.hybrid.head_weight', 'rag.fuse_queries', 'rag.reranker.url', 'rag.min_results'],
    measure: 'Keep questions with the facts a good answer must hold. Compare the share of facts found in the top 5, 10 and 30 passages before and after one change (docs/retrieval.md).',
  },
  {
    symptom: 'The answer invents a code, a name or a number',
    look: 'Admin > Traces: a retrieval step with 0 passages is a warning. Check the assistant’s knowledge mode and whether the passage is in the sources list at all.',
    keys: ['rag.hybrid.bm25_weight', 'rag.hybrid.head_weight', 'rag.template', 'rag.reranker.url'],
    measure: 'Ask the same questions before and after the change. Differences smaller than the spread between two identical runs are noise (docs/retrieval.md).',
  },
  {
    symptom: 'It is slow',
    look: 'Admin > Traces waterfall: which step is long. A reranker, many rounds, a slow tool or retries each look different.',
    keys: ['rag.top_k', 'rag.neighbors', 'chat.parallel_tools', 'chat.model_retries', 'chat.max_tool_iterations'],
    measure: 'Run time and the time of each step in the trace. Compare runs of similar questions.',
  },
  {
    symptom: 'The prompt is too big, or the model forgets the start',
    look: 'Admin > Traces: prompt tokens per round, steps named clear, summarise_run and overflow_suspected, and the tools offered.',
    keys: ['chat.context.window_local', 'chat.context.clear_at', 'tools.spill_chars', 'rag.top_k_rerank', 'rag.passage_max_chars', 'tools.max_result_chars'],
    measure: 'Largest prompt per round before and after a change; how often a run needed clearing (docs/context-management.md).',
  },
  {
    symptom: 'A long task stalls, loops or stops early',
    look: 'Admin > Traces: number of rounds, a last model step with stop reason round_cap, tool steps with repeat_refused, and model steps that were retried.',
    keys: ['chat.max_tool_iterations', 'cowork.max_tool_iterations', 'chat.repeat_limit', 'chat.model_retries', 'chat.context.compact', 'chat.context.keep_rounds'],
    measure: 'How many runs end at the cap and how many calls were retried (docs/context-management.md).',
  },
  {
    symptom: 'Follow-up turns cost as much as the first',
    look: 'Admin > Traces: the cached share of the prompt on later turns. Providers cache the start of a prompt, and passages inside the system prompt change it.',
    keys: ['rag.placement'],
    measure: 'Cached share of follow-up turns under each placement, and the quality of answers on your own questions (docs/run-trace.md).',
  },
]

/** Links of section E, in reading order. */
export const READING: DocId[] = ['answers', 'retrieval', 'context', 'trace', 'audit', 'guardrails']

// ---- Sections ---------------------------------------------------------------------------------------------------

export const SECTIONS = [
  { id: 'answered', label: 'How a question is answered' },
  { id: 'retrieval', label: 'The retrieval pipeline' },
  { id: 'loop', label: 'The agent loop' },
  { id: 'tune', label: 'What to tune and measure' },
  { id: 'docs', label: 'Read more' },
]
