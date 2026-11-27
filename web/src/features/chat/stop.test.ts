import { describe, expect, it, vi } from 'vitest'
import type { Message } from '@/api/types'
import { useChat } from '@/stores/chat'

const signals: AbortSignal[] = []
class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}
const api = vi.fn(async (..._a: unknown[]): Promise<unknown> => undefined)
vi.mock('@/api/client', () => ({
  ApiError,
  api: (...a: unknown[]) => api(...a),
  apiFetch: (_url: string, init: RequestInit) => {
    signals.push(init.signal!)
    return new Promise<Response>((_, reject) => init.signal!.addEventListener('abort', () => reject(new Error('aborted')))) // as fetch does
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

  it('keeps listening when the server could not cancel, and stops when the run had already ended', async () => {
    useChat.getState().load('A', '', {}, 'a2')
    resumeStream('A', 'a2')
    api.mockRejectedValueOnce(new ApiError(502, 'Bad gateway'))
    await stopStream()
    expect(signals[2].aborted).toBe(false) // the answer is still being written: do not show "Stopped"

    api.mockRejectedValueOnce(new ApiError(404, 'not running'))
    await stopStream()
    expect(signals[2].aborted).toBe(true)
  })

  it('takes the question card away with the reply it stops', async () => {
    const asked: Message = { id: 'a3', parentId: null, childrenIds: [], role: 'assistant', model: 'm', createdAt: 0, status: 'streaming', parts: [], files: [], sources: [], pendingCalls: [{ kind: 'ask_user', callId: 'c1', questions: [] }] }
    useChat.getState().load('A', '', { a3: asked }, 'a3')
    resumeStream('A', 'a3')
    await stopStream()
    await vi.waitFor(() => expect(useChat.getState().messages.a3.status).toBe('stopped'))
    expect(useChat.getState().messages.a3.pendingCalls).toEqual([])
  })
})
