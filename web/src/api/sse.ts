// Incremental parser for text/event-stream (the WHATWG framing rules):
// lines end in LF, CRLF or CR; "field: value"; ":" starts a comment (pings);
// a blank line dispatches; multi-line data is joined with "\n".

export interface SSEMessage {
  event: string
  data: string
  id?: string
}

export class SSEParser {
  private buf = ''
  private pendingCR = false
  private data: string[] = []
  private event = ''
  private id: string | undefined
  private hasData = false

  /** Feeds decoded text and returns the messages completed by it. */
  push(chunk: string): SSEMessage[] {
    const out: SSEMessage[] = []
    let text = chunk
    if (this.pendingCR) {
      // A CR at the end of the previous chunk already ended a line; skip its LF.
      if (text.startsWith('\n')) text = text.slice(1)
      this.pendingCR = false
    }
    this.buf += text
    let start = 0
    for (let i = 0; i < this.buf.length; i++) {
      const c = this.buf[i]
      if (c !== '\n' && c !== '\r') continue
      const line = this.buf.slice(start, i)
      if (c === '\r') {
        if (i + 1 === this.buf.length) this.pendingCR = true
        else if (this.buf[i + 1] === '\n') i++
      }
      start = i + 1
      this.line(line, out)
    }
    this.buf = this.buf.slice(start)
    return out
  }

  /** Ends the stream; an unterminated event is discarded, as the spec says. */
  flush(): SSEMessage[] {
    this.buf = ''
    this.reset()
    return []
  }

  private line(line: string, out: SSEMessage[]) {
    if (line === '') {
      if (this.hasData) out.push({ event: this.event || 'message', data: this.data.join('\n'), id: this.id })
      this.reset()
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    switch (field) {
      case 'data':
        this.data.push(value)
        this.hasData = true
        break
      case 'event':
        this.event = value
        break
      case 'id':
        if (!value.includes('\0')) this.id = value
        break
      // "retry" and unknown fields are ignored: reconnects are ours to time.
    }
  }

  private reset() {
    this.data = []
    this.event = ''
    this.hasData = false
    // ponytail: the spec keeps lastEventId across events; we read seq from data instead.
    this.id = undefined
  }
}

/** Reads a fetch body as SSE and calls onMessage for every event. */
export async function readSSE(body: ReadableStream<Uint8Array>, onMessage: (m: SSEMessage) => void, signal?: AbortSignal) {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  const parser = new SSEParser()
  try {
    for (;;) {
      if (signal?.aborted) break
      const { value, done } = await reader.read()
      if (done) break
      for (const m of parser.push(decoder.decode(value, { stream: true }))) onMessage(m)
    }
    for (const m of parser.push(decoder.decode())) onMessage(m)
  } finally {
    reader.releaseLock()
  }
}
