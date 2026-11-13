import { describe, expect, it } from 'vitest'
import type { ChatSummary } from './types'
import { CHAT_PAGE, nextChatPage } from './queries'

const page = (n: number): ChatSummary[] => Array.from({ length: n }, (_, i) => ({ id: `c${i}`, title: '', updated_at: 0 }))

describe('nextChatPage', () => {
  it('asks for the next page while pages come back full', () => {
    expect(nextChatPage(page(CHAT_PAGE), [page(CHAT_PAGE)])).toBe(2)
    expect(nextChatPage(page(CHAT_PAGE), [page(CHAT_PAGE), page(CHAT_PAGE)])).toBe(3)
  })
  it('stops after a short page', () => {
    expect(nextChatPage(page(12), [page(CHAT_PAGE), page(12)])).toBeUndefined()
    expect(nextChatPage([], [[]])).toBeUndefined()
  })
})
