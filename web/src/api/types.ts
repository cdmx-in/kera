// Shapes shared by the API client, the stream reducer and the components.

export type Role = 'system' | 'user' | 'assistant'
export type MessageStatus = 'streaming' | 'done' | 'error' | 'stopped'

export interface FileRef {
  id: string
  name: string
  size?: number
  contentType?: string
  type?: 'file' | 'image' | 'collection'
}

export interface Source {
  id?: string
  name?: string
  title?: string
  url?: string
  fileId?: string
  page?: number
  snippet?: string
  score?: number
}

export interface StatusLine {
  id?: string
  action?: string
  description: string
  done?: boolean
}

export interface ToolStep {
  callId: string
  name: string
  server?: string
  args?: unknown
  status: 'running' | 'done' | 'error'
  output?: string
  files?: FileRef[]
  urls?: string[]
  statusLines: StatusLine[]
}

export interface Block {
  type: string
  [k: string]: unknown
}

export type Part =
  | { type: 'text'; text: string }
  | { type: 'reasoning'; text: string; durationMs?: number; done: boolean; startedAt?: number }
  | { type: 'steps'; steps: ToolStep[] }
  | { type: 'block'; block: Block }
  | { type: 'compaction'; state?: 'running' | 'done'; summary?: string; before?: number; after?: number }

export interface AskQuestion {
  question: string
  header?: string
  multiSelect?: boolean
  allowOther?: boolean
  options: { label: string; description?: string; recommended?: boolean }[]
}

export type PendingCall =
  | { kind: 'ask_user'; callId: string; questions: AskQuestion[] }
  | { kind: 'confirm'; callId: string; title?: string; message: string; confirmLabel?: string; cancelLabel?: string }

export interface Usage {
  prompt_tokens?: number
  completion_tokens?: number
  [k: string]: unknown
}

export interface Message {
  id: string
  parentId: string | null
  childrenIds: string[]
  role: Role
  model?: string
  createdAt: number
  status: MessageStatus
  parts: Part[]
  files: FileRef[]
  sources: Source[]
  pendingCalls: PendingCall[]
  followUps?: string[]
  usage?: Usage
  error?: { code?: string; message: string }
  /** Transient status line not tied to a tool step (e.g. "Reading files"). */
  statusLine?: StatusLine
  /** Last applied stream event sequence number (resume cursor). */
  seq?: number
}

export interface ModelFeature {
  id: string
  label: string
  icon?: string
  default?: boolean
}

export interface Model {
  id: string
  name: string
  hidden?: boolean
  local?: boolean
  /** chat (default), embedding or rerank; only chat models can be picked for a conversation */
  kind?: string
  features?: ModelFeature[]
  toolIds?: string[]
  suggestionPrompts?: { title?: string | string[]; content: string }[]
  profileImageUrl?: string
  capabilities?: Record<string, boolean>
  info?: { meta?: { description?: string } }
}

export interface Me {
  id: string
  name: string
  email: string
  role: 'admin' | 'user'
  profile_image_url?: string
  token?: string
  permissions?: Record<string, Record<string, boolean>>
}

export interface ChatSummary {
  id: string
  title: string
  updated_at: number
  created_at?: number
  pinned?: boolean
  folder_id?: string | null
}

export interface Folder {
  id: string
  name: string
  parent_id?: string | null
}

export interface ToolServer {
  id: string
  name: string
  kind?: 'openapi' | 'mcp' | 'builtin' | string
  description?: string
  authenticated?: boolean
  auth_type?: 'none' | 'bearer' | 'header' | 'user_oauth' | string
  /** OAuth provider id when auth_type is user_oauth. */
  provider?: string
  /** Turn on in new chats once the user is connected. */
  auto_enable?: boolean
  connected_at?: number
  functions?: string[]
  error?: string
}

export interface ApiKey {
  id: string
  name: string
  prefix?: string
  created_at?: number
  last_used_at?: number | null
  key?: string
}

export interface UploadedFile {
  id: string
  filename: string
  meta?: { name?: string; size?: number; content_type?: string }
  data?: { status?: 'pending' | 'processing' | 'completed' | 'failed'; error?: string }
}

export interface FilePreview {
  /** csv/xlsx render as a table; text/docx as lines; other has no preview. */
  kind: 'csv' | 'xlsx' | 'table' | 'pdf' | 'image' | 'text' | 'docx' | 'other'
  sheets?: string[]
  rows?: string[][]
  text?: string
  truncated?: boolean
}

/** One decoded event of the native SSE dialect. */
export interface StreamEvent {
  type: string
  seq?: number
  messageId?: string
  chatId?: string
  [k: string]: unknown
}
