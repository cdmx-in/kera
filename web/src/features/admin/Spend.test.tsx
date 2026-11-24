import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { SpendEmpty } from './Spend'

describe('SpendEmpty', () => {
  const html = renderToStaticMarkup(<SpendEmpty />)
  it('says in plain words where the figures come from', () => {
    expect(html).toContain('Spend tracking is not set up')
    expect(html).toContain('LiteLLM&#x27;s spend log')
  })
  it('lists the setup steps and links the guide', () => {
    expect(html).toContain('docker compose --profile litellm up -d')
    expect(html).toContain('Admin &gt; Settings &gt; Spend')
    expect(html).toContain('href="https://github.com/cdmx-in/kera/blob/main/docs/litellm.md"')
  })
})
