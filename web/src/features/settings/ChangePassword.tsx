// Settings > Account > Password: the current password, then a new one. The
// server ends every other session of the user and hands this one a fresh token.
import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { api, setToken, type ApiError } from '@/api/client'
import type { Me } from '@/api/types'
import { toast } from '@/stores/toast'
import { passwordProblem, PASSWORD_MIN } from '@/lib/password'
import { LField, PasswordInput, noteId } from '@/ui/PasswordInput'
import { Btn, inputCls } from '@/features/admin/ui'

export function ChangePassword({ me }: { me: Me }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [errors, setErrors] = useState<{ current?: string; next?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    const found = { current: current ? undefined : 'Enter your current password.', next: passwordProblem(next, me.email) || undefined }
    if (found.current || found.next) return setErrors(found)
    setBusy(true)
    setErrors({})
    try {
      const res = await api<{ token?: string }>('/api/v1/auths/update/password', { method: 'POST', json: { password: current, new_password: next } })
      if (res?.token) setToken(res.token) // the old token no longer works
    } catch (err) {
      const e = err as ApiError
      setBusy(false)
      if (e.field === 'password') return setErrors({ current: e.message })
      if (e.field === 'new_password') return setErrors({ next: e.message })
      return setErrors({ form: e.message })
    }
    setBusy(false)
    setCurrent('')
    setNext('')
    toast('Password changed. Your other sessions are signed out.')
  }

  return (
    <section data-testid="change-password">
      <h3 className="mb-2 flex items-center gap-1.5 text-sm font-medium">
        <KeyRound size={14} className="text-muted" aria-hidden /> Password
      </h3>
      <form onSubmit={submit} noValidate className="space-y-3 rounded-xl border border-line p-3">
        {/* lets a password manager file the new password under this email */}
        <input type="text" name="username" autoComplete="username" value={me.email} readOnly tabIndex={-1} aria-hidden className="sr-only" />
        <LField id="current-password" label="Current password" error={errors.current}>
          <PasswordInput
            id="current-password"
            value={current}
            onChange={(v) => (setCurrent(v), setErrors({ ...errors, current: undefined }))}
            autoComplete="current-password"
            invalid={!!errors.current}
            describedBy={errors.current ? noteId('current-password') : undefined}
            className={inputCls}
            testId="current-password"
          />
        </LField>
        <LField id="new-password" label="New password" hint={`At least ${PASSWORD_MIN} characters; a few random words work well. Signs you out on your other devices.`} error={errors.next}>
          <PasswordInput
            id="new-password"
            value={next}
            onChange={(v) => (setNext(v), setErrors({ ...errors, next: undefined }))}
            autoComplete="new-password"
            invalid={!!errors.next}
            describedBy={noteId('new-password')}
            className={inputCls}
            testId="new-password"
          />
        </LField>
        {errors.form && (
          <p role="alert" className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-600 dark:text-red-400" data-testid="change-password-error">
            {errors.form}
          </p>
        )}
        <div className="flex justify-end">
          <Btn type="submit" variant="primary" disabled={busy} data-testid="change-password-submit">
            Change password
          </Btn>
        </div>
      </form>
    </section>
  )
}
