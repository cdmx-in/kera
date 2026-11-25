import { describe, expect, it } from 'vitest'
import type { Model, ToolServer } from '@/api/types'
import { defaultToolIds } from '@/features/chat/model-select'
import { checkConnections, effectiveToolIds, groupTools, leftOut, pickable, searchTools, selectedApps, toolStatus } from './tools'

const srv = (id: string, over: Partial<ToolServer> = {}): ToolServer => ({ id, name: id, kind: 'mcp', auth_type: 'none', authenticated: true, functions: ['a', 'b'], ...over })
const sheets = srv('sheets', { auth_type: 'user_oauth', provider: 'google', description: 'Read and fill spreadsheets', functions: ['read_sheet', 'write_cells'] })
const mail = srv('mail', { auth_type: 'user_oauth', local_only: true, description: 'Read mail' })
const kb = srv('kb', { description: 'Team knowledge base', functions: ['search'] })
const all = [sheets, mail, kb, srv('core', { kind: 'builtin' })]

describe('toolStatus', () => {
  it('is ready when connected, or when it needs no account', () => {
    expect(toolStatus(sheets, false)).toMatchObject({ state: 'ready', selectable: true, label: 'Connected' })
    expect(toolStatus(kb, false)).toMatchObject({ state: 'ready', selectable: true, label: 'Ready' })
  })
  it('needs Connect for an account that is not linked, and cannot be switched on', () => {
    const s = toolStatus({ ...sheets, authenticated: false }, false)
    expect(s).toMatchObject({ state: 'connect', selectable: false, connect: 'Connect', label: 'Connect needed' })
  })
  it('is unavailable when the server fails; a linked account can be reconnected, a shared tool cannot', () => {
    expect(toolStatus({ ...sheets, error: 'unavailable' }, false)).toMatchObject({ state: 'error', selectable: false, connect: 'Reconnect' })
    expect(toolStatus({ ...kb, error: 'unavailable' }, false)).toMatchObject({ state: 'error', selectable: false, connect: undefined })
  })
  it('keeps a local-only app for local assistants: with a hosted one it is greyed with the reason', () => {
    expect(toolStatus(mail, false)).toMatchObject({ state: 'local', selectable: false, label: 'Local assistants only' })
    expect(toolStatus(mail, false).reason).toMatch(/local assistant/)
    expect(toolStatus(mail, true)).toMatchObject({ state: 'ready', selectable: true })
  })
  it('puts Connect before the local-only reason, as the old list did, and mentions both', () => {
    const s = toolStatus({ ...mail, authenticated: false }, false)
    expect(s.state).toBe('connect')
    expect(s.reason).toMatch(/local assistant/)
  })
})

describe('listing', () => {
  it('leaves out the built-in sets: they ride on the assistant', () => {
    expect(pickable(all).map((s) => s.id)).toEqual(['sheets', 'mail', 'kb'])
    expect(pickable(undefined)).toEqual([])
  })
  it('groups your accounts apart from shared tools and drops an empty group', () => {
    expect(groupTools(pickable(all)).map((g) => [g.id, g.items.map((s) => s.id)])).toEqual([['accounts', ['sheets', 'mail']], ['shared', ['kb']]])
    expect(groupTools(pickable(all), true).map((g) => g.items.map((s) => s.id))).toEqual([['sheets', 'mail'], ['kb']])
    expect(groupTools([mail, sheets], false).map((g) => g.items.map((s) => s.id))).toEqual([['sheets', 'mail']]) // what can be switched on comes first
    expect(groupTools([kb]).map((g) => g.id)).toEqual(['shared'])
    expect(groupTools([])).toEqual([])
  })
  it('searches names, descriptions and function names, every word', () => {
    expect(searchTools(all, 'sheet').map((s) => s.id)).toEqual(['sheets'])
    expect(searchTools(all, 'spreadsheets read').map((s) => s.id)).toEqual(['sheets'])
    expect(searchTools(all, 'write_cells').map((s) => s.id)).toEqual(['sheets'])
    expect(searchTools(all, '  ').length).toBe(all.length)
    expect(searchTools(all, 'zzz')).toEqual([])
    expect(searchTools([kb], 'renamed', () => 'Renamed').length).toBe(1)
  })
})

describe('which apps join the run', () => {
  const on = ['server:sheets', 'mail', 'builtin:code', 'assistant-only']
  it('selectedApps lists the switched-on apps that can join', () => {
    expect(selectedApps(on, all, false).map((s) => s.id)).toEqual(['sheets'])
    expect(selectedApps(on, all, true).map((s) => s.id)).toEqual(['sheets', 'mail'])
  })
  it('leftOut lists the switched-on apps that cannot', () => {
    expect(leftOut(on, all, false).map((s) => s.id)).toEqual(['mail'])
    expect(leftOut(on, all, true)).toEqual([])
    expect(leftOut(['sheets'], [{ ...sheets, authenticated: false }], false).map((s) => s.id)).toEqual(['sheets'])
  })
  it('effectiveToolIds drops those and keeps ids the picker does not list (built-in sets, assistant-only servers)', () => {
    expect(effectiveToolIds(on, all, false)).toEqual(['server:sheets', 'builtin:code', 'assistant-only'])
    expect(effectiveToolIds(on, all, true)).toEqual(on)
    expect(effectiveToolIds(on, undefined, false)).toEqual(on)
  })
})

describe('checkConnections', () => {
  it('reports each switched-on app: OK with its tool count, or what is wrong', () => {
    const list = [sheets, { ...kb, error: 'unavailable' }, { ...srv('wiki'), functions: [] }, { ...srv('crm', { auth_type: 'user_oauth' }), authenticated: false }, mail]
    const r = checkConnections(['sheets', 'kb', 'wiki', 'crm', 'mail', 'nope'], list, false)
    expect(r.map((x) => [x.id, x.ok])).toEqual([['sheets', true], ['kb', false], ['wiki', false], ['crm', false], ['mail', false]])
    expect(r[0].text).toBe('OK, 2 tools available')
    expect(r[1].text).toBe('Not answering')
    expect(r[2].text).toMatch(/offers no tools/)
    expect(r[3]).toMatchObject({ text: 'Not connected', connect: 'Connect' })
    expect(r[4].text).toBe('Needs a local assistant')
  })
  it('asks a linked account to reconnect when it fails, and says "1 tool" in the singular', () => {
    const r = checkConnections(['sheets', 'kb'], [{ ...sheets, error: 'unavailable' }, kb], false)
    expect(r[0]).toMatchObject({ ok: false, connect: 'Reconnect' })
    expect(r[1].text).toBe('OK, 1 tool available')
  })
})

describe('defaultToolIds', () => {
  const m = { id: 'm', name: 'M', toolIds: ['server:kb', 'builtin:web'] } as Model
  it('is the assistant\'s own tools plus connected apps that switch on by themselves', () => {
    const servers = [kb, { ...sheets, auto_enable: true }, { ...mail, auto_enable: true, authenticated: false }]
    const integrations = [{ id: 'i', kind: 'oauthToolServer' as const, toolServerId: 'sheets', label: 'Sheets', autoEnable: true }]
    expect(defaultToolIds(m, servers, [])).toEqual(['kb', 'builtin:web', 'sheets'])
    expect([...new Set(defaultToolIds(m, servers, integrations))]).toEqual(['kb', 'builtin:web', 'sheets'])
    expect(defaultToolIds({ ...m, toolIds: undefined }, undefined, [])).toEqual([])
  })
})
