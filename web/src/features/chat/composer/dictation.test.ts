import { describe, expect, it, vi } from 'vitest'
import { dictationStatus, installDictation, startDictation } from './dictation'

function fake(status: string) {
  const log: { localAtStart?: boolean; instance?: Fake } = {}
  class Fake {
    lang = ''
    continuous = false
    interimResults = true
    processLocally = false
    onresult: ((e: never) => void) | null = null
    onerror: ((e: { error: string }) => void) | null = null
    onend: (() => void) | null = null
    stopped = false
    constructor() {
      log.instance = this
    }
    start() {
      log.localAtStart = this.processLocally
    }
    stop() {
      this.stopped = true
      this.onend?.()
    }
    static available = vi.fn(async () => status)
    static install = vi.fn(async () => true)
  }
  return { w: { SpeechRecognition: Fake as never }, log, Fake }
}

describe('dictationStatus', () => {
  it('asks for on-device recognition and passes the answer on', async () => {
    const { w, Fake } = fake('downloadable')
    expect(await dictationStatus('en-IN', w)).toBe('downloadable')
    expect(Fake.available).toHaveBeenCalledWith({ langs: ['en-IN'], processLocally: true })
  })
  it('is unavailable without the API, or when the check fails', async () => {
    expect(await dictationStatus('en-US', {})).toBe('unavailable')
    expect(await dictationStatus('en-US', { SpeechRecognition: class {} as never })).toBe('unavailable') // no available()
    const { w, Fake } = fake('available')
    Fake.available.mockRejectedValueOnce(new Error('x'))
    expect(await dictationStatus('en-US', w)).toBe('unavailable')
  })
  it('installs the language pack for on-device use', async () => {
    const { w, Fake } = fake('downloadable')
    expect(await installDictation('en-US', w)).toBe(true)
    expect(Fake.install).toHaveBeenCalledWith({ langs: ['en-US'], processLocally: true })
    expect(await installDictation('en-US', {})).toBe(false)
  })
})

describe('startDictation', () => {
  it('sets processLocally before start() and passes only finished phrases on', () => {
    const { w, log } = fake('available')
    const texts: string[] = []
    const ends: (string | undefined)[] = []
    const stop = startDictation('en-US', (t) => texts.push(t), (p) => ends.push(p), w)
    expect(log.localAtStart).toBe(true) // the audio never goes to a server
    expect(log.instance).toMatchObject({ lang: 'en-US', continuous: true, interimResults: false })
    const result = (text: string, isFinal: boolean) => Object.assign([{ transcript: text }], { isFinal })
    ;(log.instance!.onresult as (e: unknown) => void)({ resultIndex: 1, results: [result('already sent', true), result(' book a room ', true), result('and then', false)] })
    expect(texts).toEqual(['book a room'])
    stop()
    expect(log.instance!.stopped).toBe(true)
    expect(ends).toEqual([undefined])
  })
  it('explains a refused microphone, and is quiet about silence', () => {
    const a = fake('available')
    const ends: (string | undefined)[] = []
    startDictation('en-US', () => undefined, (p) => ends.push(p), a.w)
    a.log.instance!.onerror!({ error: 'not-allowed' })
    a.log.instance!.onend!()
    const b = fake('available')
    startDictation('en-US', () => undefined, (p) => ends.push(p), b.w)
    b.log.instance!.onerror!({ error: 'no-speech' })
    b.log.instance!.onend!()
    expect(ends).toEqual(['Microphone access was refused', undefined])
  })
  it('reports a browser without speech recognition', () => {
    const ends: (string | undefined)[] = []
    startDictation('en-US', () => undefined, (p) => ends.push(p), {})
    expect(ends[0]).toMatch(/not available/)
  })
})
