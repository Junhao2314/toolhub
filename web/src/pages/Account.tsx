import { KeyRound, UserRoundPen } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { api, type User } from '../api/client'
import { Button, ErrorNotice, Field, PageHeader } from '../components/ui'
import { useI18n } from '../i18n'

export default function Account({ user, signedOut }: { user: User; signedOut: () => void }) {
  const { t } = useI18n()
  return <>
    <PageHeader title={t('Account')} detail={t('Update your sign-in credentials. Successful changes sign out every active session.')} />
    <section className="account-summary">
      <div className="user-avatar">{user.displayName.slice(0, 1).toUpperCase()}</div>
      <div><strong>{user.displayName}</strong><span>@{user.username} · {user.email}</span></div>
    </section>
    <div className="credential-grid">
      <UsernameForm user={user} signedOut={signedOut} />
      <PasswordForm recommended={Boolean(user.passwordChangeRecommended)} signedOut={signedOut} />
    </div>
  </>
}

function UsernameForm({ user, signedOut }: { user: User; signedOut: () => void }) {
  const { t } = useI18n()
  const [username, setUsername] = useState(user.username)
  const [currentPassword, setCurrentPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    setError('')
    setBusy(true)
    api.updateOwnUsername(username, currentPassword).then(signedOut).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false))
  }
  return <form className="credential-card" onSubmit={submit}>
    <header><UserRoundPen size={20} /><div><h2>{t('Change username')}</h2><p>{t('3–32 lowercase letters, numbers, dots, underscores, or hyphens.')}</p></div></header>
    {error && <ErrorNotice message={error} />}
    <Field label={t('Username')}><input autoCapitalize="none" value={username} onChange={(event) => setUsername(event.target.value)} minLength={3} maxLength={32} pattern="[A-Za-z0-9._-]+" required /></Field>
    <Field label={t('Current password')}><input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} required /></Field>
    <Button type="submit" disabled={busy || !currentPassword}>{busy ? t('Updating...') : t('Change username and sign out')}</Button>
  </form>
}

function PasswordForm({ recommended, signedOut }: { recommended: boolean; signedOut: () => void }) {
  const { t } = useI18n()
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (newPassword !== confirmation) {
      setError(t('New password confirmation does not match'))
      return
    }
    setError('')
    setBusy(true)
    api.updateOwnPassword(currentPassword, newPassword).then(signedOut).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false))
  }
  return <form className="credential-card" onSubmit={submit}>
    <header><KeyRound size={20} /><div><h2>{t('Change password')}</h2><p>{recommended ? t('A new password is recommended because this account has a temporary password.') : t('Use at least 12 characters. All active sessions will be revoked.')}</p></div></header>
    {error && <ErrorNotice message={error} />}
    <Field label={t('Current password')}><input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} required /></Field>
    <Field label={t('New password')}><input type="password" autoComplete="new-password" minLength={12} value={newPassword} onChange={(event) => setNewPassword(event.target.value)} required /></Field>
    <Field label={t('Confirm new password')}><input type="password" autoComplete="new-password" minLength={12} value={confirmation} onChange={(event) => setConfirmation(event.target.value)} required /></Field>
    <Button type="submit" disabled={busy || !currentPassword || newPassword.length < 12 || newPassword !== confirmation}>{busy ? t('Updating...') : t('Change password and sign out')}</Button>
  </form>
}
