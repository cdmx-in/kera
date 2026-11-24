import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { NoModels } from './EmptyState'

describe('NoModels', () => {
  it('tells a user to ask an admin, without naming settings they cannot open', () => {
    const html = renderToStaticMarkup(<NoModels admin={false} />)
    expect(html).toContain('Ask an admin')
    expect(html).not.toContain('Admin &gt; Settings')
  })
  it('tells an admin where to connect a model and how to share it', () => {
    const html = renderToStaticMarkup(<NoModels admin />)
    expect(html).toContain('Admin &gt; Settings &gt; Connections')
    expect(html).toContain('Base models public')
  })
})
