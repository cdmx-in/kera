// Display names for ids shown in admin lists: ids stay as the muted subtitle,
// the title reads like a name ("litellm-web" -> "LiteLLM web").

const known: Record<string, string> = {
  litellm: 'LiteLLM', ollama: 'Ollama', openai: 'OpenAI', mcp: 'MCP', api: 'API', kb: 'KB', oauth: 'OAuth',
  google: 'Google', sheets: 'Sheets', drive: 'Drive', opensearch: 'OpenSearch', openapi: 'OpenAPI', gpt: 'GPT', llm: 'LLM', ai: 'AI', ui: 'UI', url: 'URL',
}

/** Turns an id into a sentence-case title, keeping product names and acronyms as they are written. */
export function displayName(id: string, name?: string): string {
  if (name && name.trim()) return name.trim()
  const words = id.split(/[-_.:\s]+/).filter(Boolean)
  return words
    .map((w, i) => {
      const k = known[w.toLowerCase()]
      if (k) return k
      return i === 0 ? w[0].toUpperCase() + w.slice(1) : w.toLowerCase()
    })
    .join(' ')
}
