// Lazy chunk: lowlight with a dozen common grammars, rendered to React nodes.
import { createElement, type ReactNode } from 'react'
import { createLowlight } from 'lowlight'
import bash from 'highlight.js/lib/languages/bash'
import css from 'highlight.js/lib/languages/css'
import go from 'highlight.js/lib/languages/go'
import java from 'highlight.js/lib/languages/java'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import markdown from 'highlight.js/lib/languages/markdown'
import python from 'highlight.js/lib/languages/python'
import sql from 'highlight.js/lib/languages/sql'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'

const lowlight = createLowlight({ bash, css, go, java, javascript, json, markdown, python, sql, typescript, xml, yaml })
lowlight.registerAlias({ bash: ['sh', 'shell', 'zsh'], javascript: ['js', 'jsx'], typescript: ['ts', 'tsx'], python: ['py'], xml: ['html', 'svg'], yaml: ['yml'], markdown: ['md'] })

interface HNode {
  type: string
  value?: string
  tagName?: string
  properties?: { className?: string[] }
  children?: HNode[]
}

function render(n: HNode, key: number): ReactNode {
  if (n.type === 'text') return n.value
  if (n.type === 'element') return createElement(n.tagName ?? 'span', { key, className: n.properties?.className?.join(' ') }, n.children?.map(render))
  return n.children?.map(render)
}

export function highlight(code: string, lang: string): ReactNode | null {
  if (!lang || !lowlight.registered(lang)) return null
  const tree = lowlight.highlight(lang, code) as unknown as HNode
  return render(tree, 0)
}
