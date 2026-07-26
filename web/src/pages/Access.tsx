import { Clipboard, KeyRound, Plus, RefreshCw, Shield, UserCog } from 'lucide-react'
import { useState } from 'react'
import { api, type Dict, type User } from '../api/client'
import { Button, Empty, ErrorNotice, Field, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface Audit extends Dict { id: string; actor?: string; action: string; resourceType: string; resourceId: string; outcome: string; createdAt: string; metadata: Dict }
interface UserCreation { user: User; temporaryPassword?: string }
interface PasswordReset { reset: boolean; temporaryPassword?: string }

export default function Access({ currentUserID, sessionInvalidated }: { currentUserID: string; sessionInvalidated: () => void }) {
  const { t } = useI18n()
  const [tab, setTab] = useState('Users')
  const [add, setAdd] = useState(false)
  const [resetUser, setResetUser] = useState<User | null>(null)
  const state = useData(async () => {
    const [users, audit] = await Promise.all([api.list<User>('/users'), api.list<Audit>('/audit')])
    return { users: users.items, audit: audit.items }
  }, [])
  return <>
    <PageHeader title={t('Users & Audit')} detail={t('Role assignments, temporary credentials, and immutable operational history.')} actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button>{tab === 'Users' && <Button onClick={() => setAdd(true)}><Plus size={16} />{t('Add user')}</Button>}</>} />
    <div className="toolbar"><Segments options={['Users', 'Audit']} value={tab} onChange={setTab} /></div>
    {state.loading ? <Loading label={t('Loading access data')} /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : tab === 'Users' ? <Users items={state.data.users} reset={setResetUser} /> : <AuditTable items={state.data.audit} />}
    {add && <UserModal close={() => setAdd(false)} saved={state.reload} />}
    {resetUser && <PasswordModal user={resetUser} isSelf={resetUser.id === currentUserID} close={() => setResetUser(null)} saved={state.reload} sessionInvalidated={sessionInvalidated} />}
  </>
}

function Users({ items, reset }: { items: User[]; reset: (user: User) => void }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('No users')} detail={t('The bootstrap administrator should appear here.')} />
  return <div className="user-list">{items.map((user) => <article key={user.id}><div className="user-avatar">{user.displayName.slice(0, 1).toUpperCase()}</div><div><strong>{user.displayName}</strong><span>@{user.username} · {user.email}</span></div><div className="role-list">{user.roles.map((role) => <Status key={role} value={role} />)}</div><Status value={user.disabled ? 'disabled' : user.passwordChangeRecommended ? 'temporary password' : 'active'} /><div className="user-actions"><Button variant="secondary" onClick={() => reset(user)}><KeyRound size={15} />{t('Reset password')}</Button></div></article>)}</div>
}

function AuditTable({ items }: { items: Audit[] }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('Audit log is empty')} detail={t('Authentication and configuration changes appear here.')} />
  return <div className="table-scroll"><table><thead><tr><th>{t('Time')}</th><th>{t('Actor')}</th><th>{t('Action')}</th><th>{t('Resource')}</th><th>{t('Outcome')}</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td>{new Date(item.createdAt).toLocaleString()}</td><td>{item.actor ?? t('system')}</td><td><strong>{item.action}</strong></td><td>{item.resourceType}<small>{item.resourceId}</small></td><td><Status value={item.outcome} /></td></tr>)}</tbody></table></div>
}

function UserModal({ close, saved }: { close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [displayName, setName] = useState('')
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [passwordMode, setPasswordMode] = useState('random')
  const [role, setRole] = useState('viewer')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [created, setCreated] = useState<UserCreation | null>(null)
  const submit = () => {
    setError('')
    setBusy(true)
    api.post<UserCreation>('/users', { displayName, username, email, passwordMode, ...(passwordMode === 'manual' ? { password } : {}), role }).then((result) => {
      saved()
      if (result.temporaryPassword) setCreated(result)
      else close()
    }).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false))
  }
  if (created?.temporaryPassword) return <TemporaryPasswordModal title={t('Temporary password for @{username}', { username: created.user.username })} password={created.temporaryPassword} close={close} />
  return <Modal title={t('Add user')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Display name')}><input value={displayName} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('Username')} hint={t('3–32 lowercase letters, numbers, dots, underscores, or hyphens')}><input autoCapitalize="none" minLength={3} maxLength={32} pattern="[A-Za-z0-9._-]+" value={username} onChange={(event) => setUsername(event.target.value)} /></Field><Field label={t('Email')}><input type="email" value={email} onChange={(event) => setEmail(event.target.value)} /></Field><PasswordMode value={passwordMode} setValue={setPasswordMode} password={password} setPassword={setPassword} /><Field label={t('Role')}><select value={role} onChange={(event) => setRole(event.target.value)}><option value="viewer">{t('Viewer')}</option><option value="operator">{t('Operator')}</option><option value="admin">{t('Admin')}</option></select></Field><div className="role-explainer"><Shield size={18} /><span><strong>{t(role)}</strong>{role === 'admin' ? t(' can manage users, credentials, and approvals.') : role === 'operator' ? t(' can manage nodes, skills, jobs, and MCP.') : t(' has read-only visibility.')}</span></div><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={busy || !displayName || username.length < 3 || !email || (passwordMode === 'manual' && password.length < 12)}><UserCog size={15} />{busy ? t('Creating...') : t('Create user')}</Button></div></Modal>
}

function PasswordModal({ user, isSelf, close, saved, sessionInvalidated }: { user: User; isSelf: boolean; close: () => void; saved: () => void; sessionInvalidated: () => void }) {
  const { t } = useI18n()
  const [mode, setMode] = useState('random')
  const [password, setPassword] = useState('')
  const [temporaryPassword, setTemporaryPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const finish = () => { close(); if (isSelf) sessionInvalidated() }
  const submit = () => {
    setError('')
    setBusy(true)
    api.post<PasswordReset>(`/users/${user.id}/password`, { mode, ...(mode === 'manual' ? { password } : {}) }).then((result) => {
      saved()
      if (result.temporaryPassword) setTemporaryPassword(result.temporaryPassword)
      else finish()
    }).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false))
  }
  if (temporaryPassword) return <TemporaryPasswordModal title={t('Temporary password for @{username}', { username: user.username })} password={temporaryPassword} close={finish} />
  return <Modal title={t('Reset password for {name}', { name: user.displayName })} close={close}>{error && <ErrorNotice message={error} />}<p className="inline-notice">{t('Every active session for this user will be signed out immediately.')}</p><PasswordMode value={mode} setValue={setMode} password={password} setPassword={setPassword} /><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={busy || (mode === 'manual' && password.length < 12)}><KeyRound size={15} />{busy ? t('Resetting...') : t('Reset password')}</Button></div></Modal>
}

function PasswordMode({ value, setValue, password, setPassword }: { value: string; setValue: (value: string) => void; password: string; setPassword: (value: string) => void }) {
  const { t } = useI18n()
  return <div className="credential-mode"><strong>{t('Temporary password')}</strong><label><input type="radio" name="password-mode" checked={value === 'random'} onChange={() => setValue('random')} />{t('Generate a secure random password')}</label><label><input type="radio" name="password-mode" checked={value === 'manual'} onChange={() => setValue('manual')} />{t('Set a temporary password manually')}</label>{value === 'manual' && <Field label={t('Temporary password')}><input type="password" minLength={12} value={password} onChange={(event) => setPassword(event.target.value)} /></Field>}</div>
}

function TemporaryPasswordModal({ title, password, close }: { title: string; password: string; close: () => void }) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const copy = () => navigator.clipboard.writeText(password).then(() => setCopied(true)).catch(() => setCopied(false))
  return <Modal title={title} close={close}><p className="inline-notice">{t('This password is shown once. Copy it before closing.')}</p><code className="temporary-password">{password}</code><div className="modal-actions"><Button variant="secondary" onClick={copy}><Clipboard size={15} />{copied ? t('Copied') : t('Copy password')}</Button><Button onClick={close}>{t('Done')}</Button></div></Modal>
}
