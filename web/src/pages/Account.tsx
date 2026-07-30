import { KeyRound, Save, UserRound } from 'lucide-react'
import { useState } from 'react'
import { api, type AccountUser } from '../api/client'
import { Button, ErrorNotice, Field, PageHeader, Status } from '../components/ui'
import { useI18n } from '../i18n'

export default function Account({ user, signedOut }: { user: AccountUser; signedOut: () => void }) {
  const { t } = useI18n()
  return <>
    <PageHeader title={t('Account')} detail={t('Credentials and active sessions')} />
    <div className="account-grid">
      <section className="settings-section"><header><UserRound size={19} /><h2>{t('Username')}</h2><Status value={user.passwordChangeRecommended ? 'temporary password' : 'active'} /></header><UsernameForm initial={user.username} signedOut={signedOut} /></section>
      <section className="settings-section"><header><KeyRound size={19} /><h2>{t('Password')}</h2></header><PasswordForm signedOut={signedOut} /></section>
    </div>
  </>
}

function UsernameForm({ initial, signedOut }: { initial: string; signedOut: () => void }) {
  const { t } = useI18n()
  const [username, setUsername] = useState(initial)
  const [currentPassword, setCurrentPassword] = useState('')
  const [error, setError] = useState('')
  const submit = () => api.updateOwnUsername(username, currentPassword).then(signedOut).catch((reason: Error) => setError(reason.message))
  return <div className="stack-form">{error && <ErrorNotice message={error} />}<Field label={t('Username')}><input value={username} onChange={(event) => setUsername(event.target.value)} /></Field><Field label={t('Current password')}><input type="password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></Field><Button onClick={submit} disabled={!username || !currentPassword || username === initial}><Save size={16} />{t('Update username')}</Button></div>
}

function PasswordForm({ signedOut }: { signedOut: () => void }) {
  const { t } = useI18n()
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const submit = () => {
    if (newPassword !== confirmation) { setError(t('Passwords do not match')); return }
    api.updateOwnPassword(currentPassword, newPassword).then(signedOut).catch((reason: Error) => setError(reason.message))
  }
  return <div className="stack-form">{error && <ErrorNotice message={error} />}<Field label={t('Current password')}><input type="password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></Field><Field label={t('New password')}><input type="password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} /></Field><Field label={t('Confirm password')}><input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></Field><Button onClick={submit} disabled={!currentPassword || !newPassword || !confirmation}><Save size={16} />{t('Update password')}</Button></div>
}
