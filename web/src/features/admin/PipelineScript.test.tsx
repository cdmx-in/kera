// Render checks for the script tab's pieces: static markup only.
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CodeEditor, DiffView, VersionList } from './PipelineScript'
import { collapseDiff, diffLines } from './pipelines'

describe('DiffView', () => {
  const html = renderToStaticMarkup(<DiffView items={collapseDiff(diffLines('keep\nold\nkeep2', 'keep\nnew\nkeep2'))} />)
  it('shows removed lines red and added lines green, each with a sign and a word for a screen reader', () => {
    expect(html).toMatch(/data-kind="del"[^>]*bg-red-500\/15[^>]*>.*?-<\/span><span class="sr-only">Removed: <\/span><span[^>]*>old/)
    expect(html).toMatch(/data-kind="add"[^>]*bg-green-500\/15[^>]*>.*?\+<\/span><span class="sr-only">Added: <\/span><span[^>]*>new/)
  })
  it('leaves unchanged lines plain', () => {
    expect(html).toMatch(/<div data-kind="same" class="flex px-2">.*?keep<\/span>/)
  })
  it('says so when nothing changed', () => {
    expect(renderToStaticMarkup(<DiffView items={collapseDiff(diffLines('a', 'a'))} />)).toContain('No changes.')
  })
  it('folds a long unchanged run into one line', () => {
    const many = Array.from({ length: 40 }, (_, i) => `l${i}`)
    const h = renderToStaticMarkup(<DiffView items={collapseDiff(diffLines(many.join('\n'), [...many, 'tail'].join('\n')))} />)
    expect(h).toMatch(/\d+ unchanged lines/)
    expect(h).not.toContain('l0<')
  })
})

describe('CodeEditor', () => {
  it('has a gutter numbered to the last line and a labelled textarea that does not wrap', () => {
    const h = renderToStaticMarkup(<CodeEditor label="Contents of run.sh" value={'a\nb\nc'} onChange={() => {}} />)
    expect(h).toMatch(/data-testid="gutter"[^>]*aria-hidden|aria-hidden[^>]*data-testid="gutter"/)
    expect(h).toContain('<pre>1\n2\n3</pre>')
    expect(h).toContain('aria-label="Contents of run.sh"')
    expect(h).toContain('wrap="off"')
    expect(h).not.toContain('readOnly')
  })
  it('counts an empty text as one line, and can be read-only', () => {
    const h = renderToStaticMarkup(<CodeEditor label="x" value="" readOnly />)
    expect(h).toContain('<pre>1</pre>')
    expect(h).toContain('readOnly=""')
  })
})

describe('VersionList', () => {
  const versions = [
    { version: 3, note: 'Fetch less often', created_by: 'Ada', created_at: new Date(Date.now() - 3600_000).toISOString() },
    { version: 2, note: '', created_by: 'Grace', created_at: new Date(Date.now() - 86400_000 * 3).toISOString() },
  ]
  const h = renderToStaticMarkup(<VersionList versions={versions} viewing={2} onView={() => {}} />)
  it('lists version, author, age and note, newest first, with the live one marked', () => {
    expect(h.indexOf('Version 3')).toBeLessThan(h.indexOf('Version 2'))
    expect(h).toContain('Live')
    expect(h).toContain('Ada')
    expect(h).toContain('Fetch less often')
    expect(h).toContain('Grace')
  })
  it('marks the one being read as pressed', () => {
    expect(h.match(/aria-pressed="true"/g)).toHaveLength(1)
    expect(h.match(/aria-pressed="false"/g)).toHaveLength(1)
  })
  it('says so when nothing is saved', () => {
    expect(renderToStaticMarkup(<VersionList versions={[]} viewing={null} onView={() => {}} />)).toContain('Nothing saved yet.')
  })
})
