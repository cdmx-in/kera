import type { Message } from '@/api/types'
import { branchIds } from '@/stores/chat'
import { messageText } from './reduce'

/** The visible branch of a chat as Markdown: the title, then one section per turn. Attachments are named, not embedded. */
export function chatToMarkdown(title: string, messages: Record<string, Message>, currentId: string | null): string {
  const out = [`# ${title || 'Chat'}`]
  for (const id of branchIds(messages, currentId)) {
    const m = messages[id]
    const who = m.role === 'user' ? 'You' : m.model ? `Assistant (${m.model})` : 'Assistant'
    const files = m.files.length ? `\n\n_Attached: ${m.files.map((f) => f.name ?? f.id).join(', ')}_` : ''
    out.push(`## ${who}\n\n${messageText(m).trim()}${files}`)
  }
  return out.join('\n\n') + '\n'
}

/** A file name from a chat title: letters, digits and dashes, at most 60 characters. */
export function exportName(title: string): string {
  const base = title
    .normalize('NFKD')
    .replace(/[^\w\s-]/g, '')
    .trim()
    .replace(/\s+/g, '-')
    .slice(0, 60)
  return `${base || 'chat'}.md`
}

/** Saves text as a file through a temporary link (no dependency). */
export function download(name: string, text: string, type = 'text/markdown') {
  const url = URL.createObjectURL(new Blob([text], { type: `${type};charset=utf-8` }))
  const a = Object.assign(document.createElement('a'), { href: url, download: name })
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
