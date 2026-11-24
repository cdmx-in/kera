// Password rules, the same as the server's (internal/auth/password.go): the
// server decides, this only saves a round trip and words the problem.

export const PASSWORD_MIN = 10
export const PASSWORD_MAX_BYTES = 1024

/** What is wrong with a new password, or '' when it is fine. */
export function passwordProblem(password: string, email = ''): string {
  if (new TextEncoder().encode(password).length > PASSWORD_MAX_BYTES) return `Use at most ${PASSWORD_MAX_BYTES} bytes.`
  if ([...password].length < PASSWORD_MIN) return `Use at least ${PASSWORD_MIN} characters.`
  if (!password.trim()) return 'The password cannot be only spaces.'
  if (email && password.toLowerCase() === email.toLowerCase()) return 'Choose a password that is not the email address.'
  return ''
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
