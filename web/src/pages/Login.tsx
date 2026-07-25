import { useState, type FormEvent } from 'react'
import { Boxes, LockKeyhole } from 'lucide-react'
import { api, type Session } from '../api/client'
import { Button, ErrorNotice, Field } from '../components/ui'

export default function Login({ onLogin }: { onLogin: (session: Session) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    setError('')
    setSubmitting(true)
    api.login(email, password).then(onLogin).catch((reason: Error) => setError(reason.message)).finally(() => setSubmitting(false))
  }
  return <main className="login-screen">
    <section className="login-brand">
      <div className="brand-mark"><Boxes /></div>
      <h1>ToolHub</h1>
      <p>Skills and MCP operations across your Tailnet.</p>
      <div className="network-line"><span /><i /><i /><i /></div>
    </section>
    <form className="login-form" onSubmit={submit}>
      <header><LockKeyhole size={20} /><div><h2>Sign in</h2><p>Use your ToolHub administrator or operator account.</p></div></header>
      {error && <ErrorNotice message={error} />}
      <Field label="Email"><input type="email" autoComplete="username" value={email} onChange={(event) => setEmail(event.target.value)} required autoFocus /></Field>
      <Field label="Password"><input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} minLength={12} required /></Field>
      <Button type="submit" disabled={submitting}>{submitting ? 'Signing in...' : 'Sign in'}</Button>
    </form>
  </main>
}
