import { describe, expect, it } from 'vitest'
import { countLabel, displayFor, humanizeTool, pluralize, summarizeSteps, verbFor } from './display'

describe('humanizeTool', () => {
  it.each([
    ['search_web', undefined, 'search web'],
    ['docs_search_pages', 'docs', 'search pages'],
    ['getPageContentTool', undefined, 'get page content'],
    ['fetch_url', undefined, 'fetch url'],
    ['readHTMLPage', undefined, 'read HTML page'],
    ['x', undefined, 'x'],
  ])('%s (%s) -> %s', (name, server, want) => {
    expect(humanizeTool(name, server)).toBe(want)
  })
})

describe('pluralize', () => {
  it.each([
    ['web search', 'web searches'],
    ['page read', 'page reads'],
    ['query', 'queries'],
    ['day', 'days'],
    ['box', 'boxes'],
    ['code run', 'code runs'],
  ])('%s', (a, b) => expect(pluralize(a)).toBe(b))
})

describe('display', () => {
  const map = {
    search_web: { label: 'web search', kind: 'search' as const, icon: 'search' },
    fetch_url: { label: 'page read', kind: 'read' as const },
    execute_code: { label: 'code run', kind: 'code' as const },
    special: { label: 'person', plural: 'people' },
  }
  it("prefers a tool server's own name for its function", () => {
    const m = { search_kb: { label: 'KB search' }, orion_search_kb: { label: 'Orion search' } }
    expect(displayFor(m, 'search_kb', 'orion').label).toBe('Orion search')
    expect(displayFor(m, 'search_kb', 'astra').label).toBe('KB search')
  })
  it('falls back to the humanizer', () => {
    expect(displayFor(map, 'list_chat_files')).toMatchObject({ label: 'list chat files', kind: 'other' })
  })
  it('uses explicit plurals', () => {
    expect(countLabel(2, map.special)).toBe('2 people')
    expect(countLabel(1, map.special)).toBe('1 person')
  })
  it('picks verbs by kind', () => {
    expect(verbFor('code')).toBe('Executed')
    expect(verbFor('search')).toBe('Explored')
    expect(verbFor(undefined)).toBe('Used')
  })
  it('summarises a group', () => {
    const steps = [{ name: 'search_web' }, { name: 'search_web' }, { name: 'fetch_url' }]
    expect(summarizeSteps(steps, map)).toBe('Explored 2 web searches, 1 page read')
    expect(summarizeSteps([{ name: 'execute_code' }], map)).toBe('Executed 1 code run')
    expect(summarizeSteps([{ name: 'search_web' }, { name: 'execute_code' }], map)).toBe('Explored 1 web search; executed 1 code run')
  })
})
