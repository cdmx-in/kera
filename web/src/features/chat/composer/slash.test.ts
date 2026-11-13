import { describe, expect, it } from 'vitest'
import type { Prompt } from '@/features/workspace/api'
import { filterPrompts, slashQuery } from './slash'

const p = (command: string, title = ''): Prompt => ({ id: command, user_id: 'u', command, title, content: `${command} body`, write_access: false, updated_at: 0 })

describe('slashQuery', () => {
  it('reads a command typed alone', () => {
    expect(slashQuery('/')).toBe('')
    expect(slashQuery('/Sum')).toBe('sum')
  })
  it('ignores ordinary text and paths', () => {
    expect(slashQuery('hello /sum')).toBeNull()
    expect(slashQuery('/sum more words')).toBeNull()
    expect(slashQuery('/usr/bin')).toBeNull()
  })
})

describe('filterPrompts', () => {
  const all = [p('translate', 'Translate to Hindi'), p('summary', 'Summarise'), p('sum-table', 'Sum a column')]
  it('puts prefix matches first and also matches titles', () => {
    expect(filterPrompts(all, 'sum').map((x) => x.command)).toEqual(['summary', 'sum-table'])
    expect(filterPrompts(all, 'hindi').map((x) => x.command)).toEqual(['translate'])
    expect(filterPrompts(all, '')).toHaveLength(3)
  })
  it('caps the list', () => {
    expect(filterPrompts(Array.from({ length: 20 }, (_, i) => p(`c${i}`)), 'c')).toHaveLength(8)
  })
})
