import { Boxes, LogIn } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { api, type Session } from '../api/client'
import { Button, ErrorNotice, Field } from '../components/ui'
import { LanguageToggle, useI18n } from '../i18n'

export default function Login({ onLogin }: { onLogin: (session: Session) => void }) {
  const { t } = useI18n()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    setBusy(true); setError('')
    api.login(username, password).then(onLogin).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false))
  }
  return <main className="login-screen">
    <div className="login-top"><div className="brand-lockup"><div className="brand-mark"><Boxes /></div><strong>ToolHub</strong></div><LanguageToggle /></div>
    <form className="login-form" onSubmit={submit}>
      <header><h1>{t('Sign in')}</h1><span>Single-user control plane</span></header>
      {error && <ErrorNotice message={error} />}
      <Field label={t('Username')}><input autoFocus autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} /></Field>
      <Field label={t('Password')}><input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} /></Field>
      <Button type="submit" disabled={busy || !username || !password}><LogIn size={17} />{busy ? t('Signing in') : t('Sign in')}</Button>
    </form>
  </main>
}
