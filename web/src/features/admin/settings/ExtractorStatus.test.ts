import { describe, expect, it } from 'vitest'
import { extractorVerdict } from './ExtractorStatus'

describe('extractorVerdict', () => {
  const ok = { url: 'http://extract:8000', ok: true, ms: 12, types: ['.docx', '.pdf'], ocr: true, ocr_langs: 'eng' }
  it('says nothing is set when there is no address', () => {
    expect(extractorVerdict({ url: '', ok: false, ms: 0 })).toMatchObject({ tone: 'amber', text: expect.stringContaining('No extractor address') })
  })
  it('names the cause when it does not answer', () => {
    const v = extractorVerdict({ url: 'http://extract:8000', ok: false, ms: 3, error: 'not reachable: connection refused' })
    expect(v.tone).toBe('red')
    expect(v.text).toContain('http://extract:8000')
    expect(v.text).toContain('connection refused')
  })
  it('lists what it reads and whether OCR is on', () => {
    expect(extractorVerdict(ok)).toEqual({ tone: 'green', text: 'Running at http://extract:8000 · reads DOCX, PDF · OCR of scans on (eng) · 12 ms' })
    expect(extractorVerdict({ ...ok, ocr: false })).toMatchObject({ tone: 'amber', text: expect.stringContaining('OCR is not installed') })
    expect(extractorVerdict({ ...ok, ocr: undefined }).text).not.toContain('OCR') // an older extractor does not say
  })
})
