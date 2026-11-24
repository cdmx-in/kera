import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { Model } from '@/api/types'
import { ModelIcon } from './ModelPicker'

describe('ModelIcon', () => {
  it('writes the initial in the accent ink, which reads on the soft accent tile in both themes', () => {
    const html = renderToStaticMarkup(<ModelIcon m={{ id: 'llama', name: 'Llama' } as Model} />)
    expect(html).toContain('>L</span>')
    expect(html).toContain('bg-accent-soft')
    expect(html).toContain('text-accent-ink')
    expect(html).not.toMatch(/text-accent(?!-ink)/)
  })
})
