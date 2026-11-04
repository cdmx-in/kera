import { describe, expect, it } from 'vitest'
import { SSEParser, readSSE, type SSEMessage } from './sse'

function feed(chunks: string[]) {
  const p = new SSEParser()
  return chunks.flatMap((c) => p.push(c))
}

describe('SSEParser', () => {
  it('parses a simple event', () => {
    expect(feed(['event: delta\ndata: {"text":"hi"}\nid: 3\n\n'])).toEqual([{ event: 'delta', data: '{"text":"hi"}', id: '3' }])
  })

  it('handles any chunk boundary', () => {
    const text = 'event: delta\r\ndata: {"seq":1}\r\n\r\nevent: done\rdata: {}\r\r'
    const whole = feed([text])
    expect(whole).toHaveLength(2)
    for (let i = 1; i < text.length; i++) {
      expect(feed([text.slice(0, i), text.slice(i)])).toEqual(whole)
    }
    expect(feed(text.split(''))).toEqual(whole)
  })

  it('joins multi-line data and strips one leading space', () => {
    expect(feed(['data: a\ndata:  b\ndata\n\n'])).toEqual([{ event: 'message', data: 'a\n b\n', id: undefined }])
  })

  it('ignores comments, pings and retry', () => {
    expect(feed([': ping\n\n', 'retry: 1000\n\n', ':\n', 'data: x\n\n'])).toEqual([{ event: 'message', data: 'x', id: undefined }])
  })

  it('drops an unterminated trailing event', () => {
    const p = new SSEParser()
    expect(p.push('data: half')).toEqual([])
    expect(p.flush()).toEqual([])
  })

  it('reads a byte stream with split UTF-8 sequences', async () => {
    const bytes = new TextEncoder().encode('data: héllo ✓\n\n')
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        for (const b of bytes) c.enqueue(new Uint8Array([b]))
        c.close()
      },
    })
    const got: SSEMessage[] = []
    await readSSE(body, (m) => got.push(m))
    expect(got).toEqual([{ event: 'message', data: 'héllo ✓', id: undefined }])
  })
})
