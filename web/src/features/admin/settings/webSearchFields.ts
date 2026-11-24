// The web-search fields, in a plain module so a test can check them without a
// DOM (issue #33 added the SearXNG address here).
import type { FieldSpec } from './form'

export const WEB_SEARCH_FIELDS: FieldSpec[] = [
  { key: 'web.search.engine', kind: { select: [['duckduckgo', 'DuckDuckGo'], ['searxng', 'SearXNG'], ['brave', 'Brave']] }, label: 'Engine' },
  { key: 'web.search.url', kind: 'text', hint: 'Base URL of the SearXNG instance, used when the engine is SearXNG.', label: 'SearXNG address' },
  { key: 'web.search.result_count', kind: 'int', label: 'Results per search' },
  { key: 'web.search.max_calls_per_turn', kind: 'int', label: 'Searches per reply' },
]
