// Password rules, the same as the server's (internal/auth/password.go and weak.go): the
// server decides, this only saves a round trip and words the problem. Shared test vectors
// (passwordVectors.json) and the list (commonPasswords.json, copied from the server's) keep them equal.
import common from './commonPasswords.json'

export const PASSWORD_MIN = 10
export const PASSWORD_MAX_BYTES = 1024

const exact = new Set<string>(common.exact)
const words = new Set<string>(common.words)

const bytes = (s: string) => new TextEncoder().encode(s).length
const letters = (s: string) => s.replace(/[^\p{L}]/gu, '')
const unleet = (s: string) => s.replace(/[@$013457!]/g, (c) => ({ '@': 'a', $: 's', '0': 'o', '1': 'l', '3': 'e', '4': 'a', '5': 's', '7': 't', '!': 'i' })[c] ?? c)

/** One character, or a block of up to four that repeats ("abababababab"). */
function repeated(s: string): boolean {
  const r = Array.from(s)
  for (let p = 1; p <= 4 && p < r.length; p++) if (r.every((c, i) => i < p || c === r[i - p])) return true
  return false
}

const base = ['01234567890123456789', 'abcdefghijklmnopqrstuvwxyz', 'qwertyuiop', 'asdfghjkl', 'zxcvbnm', 'qwertyuiopasdfghjklzxcvbnm', '1234567890qwertyuiopasdfghjklzxcvbnm']
const runs = [...base, ...base.map((b) => Array.from(b).reverse().join(''))]

/** Nothing is left but digits and symbols once the word kera and the parts of the email name are taken out. */
function onlyContext(lower: string, email: string): boolean {
  const ctx = ['kera']
  const at = email.toLowerCase().indexOf('@')
  if (at >= 0) {
    const local = email.toLowerCase().slice(0, at)
    if (bytes(letters(local)) >= 4) ctx.push(letters(local))
    for (const part of local.split(/[^\p{L}]+/u)) if (bytes(part) >= 4) ctx.push(part)
  }
  let rest = lower
  let removed = false
  for (const w of ctx) {
    if (rest.includes(w)) {
      rest = rest.split(w).join('')
      removed = true
    }
  }
  return removed && letters(rest) === ''
}

function weakness(password: string, email: string): string {
  const lower = password.toLowerCase()
  const core = lower.replace(/^[^\p{L}]+|[^\p{L}]+$/gu, '') // without the digits and symbols around it
  if (onlyContext(lower, email)) return 'Do not build the password from your email name or the word kera.'
  if (exact.has(lower) || words.has(letters(lower)) || words.has(letters(unleet(core)))) return 'That password is too common. Choose something longer or less usual.'
  if (repeated(lower) || runs.some((r) => r.includes(lower))) return 'Avoid repeated characters and simple runs like 1234567890 or abcdefghij.'
  return ''
}

/** What is wrong with a new password, or '' when it is fine. */
export function passwordProblem(password: string, email = ''): string {
  if (bytes(password) > PASSWORD_MAX_BYTES) return `Use at most ${PASSWORD_MAX_BYTES} bytes.`
  if ([...password].length < PASSWORD_MIN) return `Use at least ${PASSWORD_MIN} characters.`
  if (!password.trim()) return 'The password cannot be only spaces.'
  if (email && password.toLowerCase() === email.toLowerCase()) return 'Choose a password that is not your email address.'
  return weakness(password, email)
}

// No 0/O, 1/l/I: a generated password is read aloud or retyped from a message.
const ALPHABET = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'

/** Four groups of five random characters, e.g. "kP7mQ-x3Rtn-bW9cD-hN4vZ" (about 115 bits). */
export function generatePassword(random: (n: number) => Uint8Array = (n) => crypto.getRandomValues(new Uint8Array(n))): string {
  const out: string[] = []
  const limit = 256 - (256 % ALPHABET.length) // reject bytes past the last whole multiple, so no letter is likelier
  while (out.length < 20) {
    for (const b of random(32)) if (b < limit && out.length < 20) out.push(ALPHABET[b % ALPHABET.length])
  }
  return [0, 5, 10, 15].map((i) => out.slice(i, i + 5).join('')).join('-')
}
