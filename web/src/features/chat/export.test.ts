import { describe, expect, it } from 'vitest'
import type { Message } from '@/api/types'
import { chatToMarkdown, exportName } from './export'

const msg = (id: string, parentId: string | null, role: Message['role'], text: string, extra: Partial<Message> = {}): Message => ({
  id,
  parentId,
  childrenIds: [],
  role,
  createdAt: 0,
  status: 'done',
  parts: [{ type: 'text', text }],
  files: [],
  sources: [],
  pendingCalls: [],
  ...extra,
})

describe('chatToMarkdown', () => {
  it('exports only the visible branch, in order', () => {
    const messages = {
      u1: msg('u1', null, 'user', 'Totals?', { childrenIds: ['a1', 'a2'], files: [{ id: 'f1', name: 'q3.csv', type: 'file' } as Message['files'][number]] }),
      a1: msg('a1', 'u1', 'assistant', 'Old answer'),
      a2: msg('a2', 'u1', 'assistant', 'New answer', { model: 'kera' }),
    }
    expect(chatToMarkdown('Q3', messages, 'a2')).toBe('# Q3\n\n## You\n\nTotals?\n\n_Attached: q3.csv_\n\n## Assistant (kera)\n\nNew answer\n')
  })
})

describe('exportName', () => {
  it('makes a safe file name', () => {
    expect(exportName('🗂️ Quarterly numbers: Q3/Q4')).toBe('Quarterly-numbers-Q3Q4.md')
    expect(exportName('🗂️')).toBe('chat.md')
  })
})
