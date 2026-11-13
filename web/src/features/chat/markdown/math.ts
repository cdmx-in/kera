import remarkMath from 'remark-math'
import type { Options } from 'react-markdown'

/**
 * remark-math with single-dollar maths off: "$5 and $10" is money, not a formula
 * (finance answers are full of it). Formulas are $$...$$, which this plugin reads
 * inline or as a block depending on its own lines.
 */
export const mathPlugin: NonNullable<Options['remarkPlugins']>[number] = [remarkMath, { singleDollarTextMath: false }]

// Only text that looks like TeX is converted: "\[1\]" and "\(note\)" stay as written.
const looksLikeTeX = (s: string) => /[\\^_={]/.test(s)

/**
 * Models write maths as \( x \) and \[ x \]; remark-math reads the $$ form, so convert those
 * (outside code). Inline code and fences are left alone.
 */
export function texDelimiters(text: string): string {
  if (!text.includes('\\')) return text
  return text
    .split(/(```[\s\S]*?(?:```|$)|`[^`\n]*`)/)
    .map((seg, i) =>
      i % 2
        ? seg
        : seg
            .replace(/\\\[([\s\S]+?)\\\]/g, (m, tex: string) => (looksLikeTeX(tex) ? `\n\n$$\n${tex.trim()}\n$$\n\n` : m))
            .replace(/\\\((.+?)\\\)/g, (m, tex: string) => (looksLikeTeX(tex) ? `$$${tex.trim()}$$` : m)),
    )
    .join('')
}
