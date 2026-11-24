import { describe, expect, it } from 'vitest'
import { generatePassword, passwordProblem } from './password'
import vectors from './passwordVectors.json'

describe('passwordProblem', () => {
  it('accepts ten characters and no composition rule', () => {
    expect(passwordProblem('alllowercase')).toBe('')
    expect(passwordProblem('7391528640')).toBe('')
  })
  it('counts characters, not bytes', () => {
    expect(passwordProblem('日本語のパスワードです')).toBe('')
    expect(passwordProblem('日本語のパス')).toMatch(/at least 10/)
  })
  it('refuses short, blank, long and the email address', () => {
    expect(passwordProblem('short')).toMatch(/at least 10/)
    expect(passwordProblem('          ')).toMatch(/only spaces/)
    expect(passwordProblem('x'.repeat(1025))).toMatch(/at most 1024/)
    expect(passwordProblem('x'.repeat(1023) + 'y')).toBe('')
    expect(passwordProblem('Ann@Example.com', 'ann@example.com')).toMatch(/not your email/)
  })
})

describe('the same rules as the server', () => {
  it.each(vectors)('$name', (v) => {
    expect(passwordProblem(v.password, v.email)).toBe(v.message)
  })
})

describe('generatePassword', () => {
  it('is four groups of five that pass the rules and never hold a look-alike', () => {
    for (let i = 0; i < 50; i++) {
      const p = generatePassword()
      expect(p).toMatch(/^[a-zA-Z2-9]{5}(-[a-zA-Z2-9]{5}){3}$/)
      expect(p).not.toMatch(/[01OlI]/)
      expect(passwordProblem(p)).toBe('')
    }
  })
  it('differs every time', () => {
    expect(new Set(Array.from({ length: 20 }, () => generatePassword())).size).toBe(20)
  })
  it('skips biased bytes', () => {
    // 54 letters: bytes 216..255 are past the last whole multiple and must not be used
    const bytes = new Uint8Array(32).fill(255)
    let calls = 0
    const p = generatePassword((n) => (calls++ < 3 ? bytes : new Uint8Array(n).fill(0)))
    expect(calls).toBe(4)
    expect(p).toBe('aaaaa-aaaaa-aaaaa-aaaaa')
  })
})
