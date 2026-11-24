// Accessibility checks found by an axe audit of the chat page: static markup only (no browser).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Composer } from './Composer'

const html = renderToStaticMarkup(
  <QueryClientProvider client={new QueryClient()}>
    <Composer onSend={async () => true} />
  </QueryClientProvider>,
)
const textarea = html.match(/<textarea[^>]*>/)?.[0] ?? ''

describe('Composer', () => {
  it('names the message box and does not give it aria-expanded, which a textbox may not carry', () => {
    expect(textarea).toContain('aria-label="Message"')
    expect(textarea).not.toContain('aria-expanded')
  })
  it('shows focus on the box with the full accent colour (3:1 against the page needs more than accent at 60%)', () => {
    expect(html).toContain('focus-within:border-accent ')
    expect(html).not.toContain('focus-within:border-accent/')
  })
})
