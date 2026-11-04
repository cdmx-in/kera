import { describe, expect, it, vi } from 'vitest'
import { useChat } from '@/stores/chat'

const signals: AbortSignal[] = []
const api = vi.fn(async () => undefined)
vi.mock('@/api/client', () => ({
  api: (...a: unknown[]) => api(...(a as [])),
  apiFetch: (_url: string, init: RequestInit) => {
    signals.push(init.signal!)
    return new Promise<Response>(() => undefined)
  },
}))

const { resumeStream, stopStream } = await import('./stream')

describe('stopStream', () => {
  it('only stops the reply of the open chat', async () => {
    useChat.getState().load('A', '', {}, 'a1')
    resumeStream('A', 'a1')
    useChat.getState().load('B', '', {}, null) // opened another chat; A keeps running in the background
    await stopStream()
    expect(api).not.toHaveBeenCalled()
    expect(signals[0].aborted).toBe(false)

    resumeStream('A', 'a1') // back on A
    await stopStream()
    expect(api).toHaveBeenCalledWith('/api/v1/chats/A/messages/a1/cancel', { method: 'POST' })
    expect(signals[1].aborted).toBe(true)
  })
})
