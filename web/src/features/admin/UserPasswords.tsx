// Admin > Users: "Add user" and "Set password". The password is typed or
// generated here, sent once and never shown again; no route returns it or its hash.
import { useState } from 'react'
import { Copy, Dices } from 'lucide-react'
import { api, type ApiError } from '@/api/client'
import { toast } from '@/stores/toast'
import { Dialog, DialogHeader } from '@/ui/Dialog'
import { Select } from '@/ui/Select'
import { LField, PasswordInput, noteId } from '@/ui/PasswordInput'
import { copyText } from '@/lib/format'
import { generatePassword, passwordProblem, PASSWORD_MIN } from '@/lib/password'
import { enc, type AdminUser, type RoleName } from './api'
import { NAME_MAX } from './lib'
import { Btn, Check, inputCls } from './ui'

type Errors = Partial<Record<'email' | 'name' | 'role' | 'password', string>>

/** The password box with its two helpers: make a strong one, copy it. */
function PasswordField({ id, label, value, onChange, error, hint, optional }: { id: string; label: string; value: string; onChange: (v: string) => void; error?: string; hint: string; optional?: boolean }) {
  const [show, setShow] = useState(false)
  return (
    <LField
      id={id}
      label={label}
      hint={hint}
      error={error}
      action={
        <span className="flex gap-1">
          {value && (
            <Btn variant="ghost" className="!px-2 !py-0.5 text-xs" onClick={async () => (await copyText(value, 'Could not copy: select the password and copy it by hand')) && toast('Password copied')}>
              <Copy size={12} aria-hidden /> Copy
            </Btn>
          )}
          <Btn
            variant="ghost"
            className="!px-2 !py-0.5 text-xs"
            onClick={() => {
              onChange(generatePassword())
              setShow(true) // the admin has to see it to pass it on
            }}
            data-testid="generate-password"
          >
            <Dices size={12} aria-hidden /> Generate
          </Btn>
        </span>
      }
    >
      <PasswordInput
        id={id}
        value={value}
        onChange={onChange}
        autoComplete="new-password"
        show={show}
        onShowChange={setShow}
        invalid={!!error}
        describedBy={error || hint ? noteId(id) : undefined}
        className={inputCls}
        required={!optional}
        testId="new-password"
      />
    </LField>
  )
}

const roles: { value: RoleName; label: string }[] = [
  { value: 'user', label: 'User' },
  { value: 'admin', label: 'Admin' },
]

/** Server answers name the field at fault; anything else is a toast. */
function showError(err: unknown, set: (e: Errors) => void) {
  const e = err as ApiError
  if (e.field && ['email', 'name', 'role', 'password'].includes(e.field)) return set({ [e.field]: e.message })
  toast(e.message, 'error')
}

export function AddUserDialog({ passwordRequired, onClose, onAdded }: { passwordRequired: boolean; onClose: () => void; onAdded: () => void }) {
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [role, setRole] = useState<RoleName>('user')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const found: Errors = {}
    if (!email.includes('@')) found.email = 'Enter an email address like name@example.com.'
    if (name.length > NAME_MAX) found.name = `The name is too long (${NAME_MAX} characters at most).`
    if (password || passwordRequired) found.password = passwordProblem(password, email.trim()) || undefined
    if (Object.values(found).some(Boolean)) return setErrors(found)
    setBusy(true)
    try {
      await api('/api/v1/auths/add', { method: 'POST', json: { email: email.trim(), name: name.trim(), role, password } })
    } catch (err) {
      setBusy(false)
      return showError(err, setErrors)
    }
    toast(`${email.trim()} added`)
    onAdded()
    onClose()
  }
  return (
    <Dialog open onClose={onClose} label="Add user" testId="add-user" className="w-[min(28rem,calc(100vw-2rem))]">
      <DialogHeader title="Add user" onClose={onClose} />
      <form onSubmit={submit} noValidate className="space-y-4 p-4">
        <LField id="add-user-email" label="Email" hint="They sign in with this address." error={errors.email}>
          <input
            id="add-user-email"
            type="email"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            required
            value={email}
            onChange={(e) => (setEmail(e.target.value), setErrors({ ...errors, email: undefined }))}
            aria-invalid={!!errors.email || undefined}
            aria-describedby={noteId('add-user-email')}
            className={inputCls}
            data-testid="add-user-email"
          />
        </LField>
        <LField id="add-user-name" label="Name (optional)" hint="Shown to others. Defaults to the part of the email before @." error={errors.name}>
          <input id="add-user-name" value={name} maxLength={NAME_MAX} autoComplete="off" onChange={(e) => (setName(e.target.value), setErrors({ ...errors, name: undefined }))} aria-describedby={noteId('add-user-name')} className={inputCls} data-testid="add-user-name" />
        </LField>
        <LField id="add-user-role" label="Role" hint="Admins manage users and settings.">
          <Select value={role} onChange={(v) => setRole(v as RoleName)} options={roles} id="add-user-role" />
        </LField>
        <PasswordField
          id="add-user-password"
          label={passwordRequired ? 'Initial password' : 'Initial password (optional)'}
          value={password}
          onChange={(v) => (setPassword(v), setErrors({ ...errors, password: undefined }))}
          error={errors.password}
          hint={
            passwordRequired
              ? `At least ${PASSWORD_MIN} characters. Pass it on once; they can change it under Settings > Account.`
              : 'Leave empty if they sign in with SSO. Otherwise at least 10 characters; they can change it under Settings > Account.'
          }
          optional={!passwordRequired}
        />
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary" disabled={busy} data-testid="add-user-submit">
            Add user
          </Btn>
        </div>
      </form>
    </Dialog>
  )
}

export function SetPasswordDialog({ user, onClose, onSaved }: { user: AdminUser; onClose: () => void; onSaved: () => void }) {
  const [password, setPassword] = useState('')
  const [revoke, setRevoke] = useState(false)
  const [error, setError] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const problem = passwordProblem(password, user.email)
    if (problem) return setError(problem)
    setBusy(true)
    try {
      await api(`/api/v1/users/${enc(user.id)}/password`, { method: 'POST', json: { password, revoke_api_keys: revoke } })
    } catch (err) {
      setBusy(false)
      return showError(err, (e) => setError(e.password))
    }
    toast(`Password set for ${user.email}${revoke ? '; API keys revoked' : ''}`)
    onSaved()
    onClose()
  }
  return (
    <Dialog open onClose={onClose} label="Set password" testId="set-password" className="w-[min(28rem,calc(100vw-2rem))]">
      <DialogHeader title={`${user.has_password ? 'Reset' : 'Set'} password for ${user.name || user.email}`} onClose={onClose} />
      <form onSubmit={submit} noValidate className="space-y-4 p-4">
        <p className="text-sm text-muted">
          {user.email} is signed out everywhere and uses this password from now on. You cannot read it again, so copy it before you save.
        </p>
        <PasswordField
          id="set-password-new"
          label="New password"
          value={password}
          onChange={(v) => (setPassword(v), setError(undefined))}
          error={error}
          hint={`At least ${PASSWORD_MIN} characters. Pass it on once; they can change it under Settings > Account.`}
        />
        <div className="space-y-1" data-testid="api-keys-note">
          <Check checked={revoke} onChange={setRevoke}>
            Also revoke their API keys{user.api_key_count > 0 ? ` (${user.api_key_count})` : ''}
          </Check>
          <p className="text-xs text-muted">API keys keep working after a password change. Tick this if the account may have been compromised; otherwise they stay.</p>
        </div>
        <div className="flex justify-end gap-2">
          <Btn onClick={onClose}>Cancel</Btn>
          <Btn type="submit" variant="primary" disabled={busy} data-testid="set-password-submit">
            Set password
          </Btn>
        </div>
      </form>
    </Dialog>
  )
}
