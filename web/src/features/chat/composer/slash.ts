import type { Prompt } from '@/features/workspace/api'

/** The command being typed when the whole draft is "/word", else null. */
export function slashQuery(text: string): string | null {
  const m = /^\/([a-z0-9_-]*)$/i.exec(text)
  return m ? m[1].toLowerCase() : null
}

/** Prompts whose command starts with q, then those matching it elsewhere in the command or title; at most 8. */
export function filterPrompts(prompts: Prompt[], q: string): Prompt[] {
  const starts = prompts.filter((p) => p.command.startsWith(q))
  const rest = prompts.filter((p) => !p.command.startsWith(q) && (p.command.includes(q) || p.title.toLowerCase().includes(q)))
  return [...starts, ...rest].slice(0, 8)
}
