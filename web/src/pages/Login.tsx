import { useState, type FormEvent } from 'react'
import { Boxes, LockKeyhole } from 'lucide-react'
import { api, type Session } from '../api/client'
import { Button, ErrorNotice, Field } from '../components/ui'
import { LanguageToggle, useI18n } from '../i18n'

export default function Login({ onLogin }: { onLogin: (session: Session) => void }) {
  const { t } = useI18n()
  const [identifier, setIdentifier] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    setError('')
    setSubmitting(true)
    api.login(identifier, password).then(onLogin).catch((reason: Error) => setError(reason.message)).finally(() => setSubmitting(false))
  }
  return <main className="login-screen">
    <LanguageToggle />
    <section className="login-brand">
      <div className="login-blob blob-1"></div>
      <div className="login-blob blob-2"></div>
      <div className="login-brand-content">
        <div className="brand-mark"><Boxes size={28} /></div>
        <h1>ToolHub</h1>
        <p>{t('Skills and MCP operations across your Tailnet.')}</p>
        <div className="network-line"><span /><i /><i /><i /></div>
      </div>
    </section>
    <section className="login-panel">
      <div className="login-blob blob-3"></div>
      <form className="login-form" onSubmit={submit}>
        <header><LockKeyhole size={24} /><div><h2>{t('Welcome back')}</h2><p>{t('Sign in to your administrator or operator account.')}</p></div></header>
        {error && <ErrorNotice message={error} />}
        <Field label={t('Username or email')}><input type="text" autoCapitalize="none" autoComplete="username" value={identifier} onChange={(event) => setIdentifier(event.target.value)} required autoFocus placeholder="username or name@example.com" /></Field>
        <Field label={t('Password')}><input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} minLength={12} required placeholder="••••••••••••" /></Field>
        <Button type="submit" disabled={submitting}>{submitting ? t('Signing in...') : t('Sign in to ToolHub')}</Button>
      </form>
    </section>
  </main>
}
