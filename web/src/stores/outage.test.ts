import { afterEach, expect, it, vi } from 'vitest'
import { apiFetch } from '@/api/client'
import { isOutageStatus, useOutage } from './outage'

afterEach(() => {
  vi.unstubAllGlobals()
  useOutage.getState().setDown(false)
})

const respond = (status: number) => vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status })))

it('classifies only server-unreachable statuses as outages', () => {
  expect([502, 503, 504, 520, 524, 530].every(isOutageStatus)).toBe(true)
  expect([400, 403, 404, 409, 500, 501, 531].some(isOutageStatus)).toBe(false)
})

it('a Cloudflare 524 or a network failure raises the outage page; a 500 or an abort does not', async () => {
  respond(524)
  await expect(apiFetch('/api/x')).rejects.toThrow()
  expect(useOutage.getState().down).toBe(true)

  useOutage.getState().setDown(false)
  respond(500)
  await expect(apiFetch('/api/x')).rejects.toThrow()
  expect(useOutage.getState().down).toBe(false)

  vi.stubGlobal('fetch', vi.fn(async () => { throw new DOMException('stopped', 'AbortError') }))
  await expect(apiFetch('/api/x')).rejects.toThrow()
  expect(useOutage.getState().down).toBe(false)

  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
  await expect(apiFetch('/api/x')).rejects.toThrow()
  expect(useOutage.getState().down).toBe(true)
})
