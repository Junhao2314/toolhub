import { Plus, RefreshCw, Shield, UserCog } from 'lucide-react'
import { useState } from 'react'
import { api, type Dict, type User } from '../api/client'
import { Button, Empty, ErrorNotice, Field, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'

interface Audit extends Dict { id: string; actor?: string; action: string; resourceType: string; resourceId: string; outcome: string; createdAt: string; metadata: Dict }

export default function Access() {
  const [tab, setTab] = useState('Users')
  const [add, setAdd] = useState(false)
  const state = useData(async () => {
    const [users, audit] = await Promise.all([api.list<User>('/users'), api.list<Audit>('/audit')])
    return { users: users.items, audit: audit.items }
  }, [])
  return <>
    <PageHeader title="Users & Audit" detail="Role assignments and immutable operational history." actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />Refresh</Button>{tab === 'Users' && <Button onClick={() => setAdd(true)}><Plus size={16} />Add user</Button>}</>} />
    <div className="toolbar"><Segments options={['Users', 'Audit']} value={tab} onChange={setTab} /></div>
    {state.loading ? <Loading label="Loading access data" /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : tab === 'Users' ? <Users items={state.data.users} /> : <AuditTable items={state.data.audit} />}
    {add && <UserModal close={() => setAdd(false)} saved={() => { setAdd(false); state.reload() }} />}
  </>
}

function Users({ items }: { items: User[] }) {
  if (!items.length) return <Empty title="No users" detail="The bootstrap administrator should appear here." />
  return <div className="user-list">{items.map((user) => <article key={user.id}><div className="user-avatar">{user.displayName.slice(0, 1).toUpperCase()}</div><div><strong>{user.displayName}</strong><span>{user.email}</span></div><div className="role-list">{user.roles.map((role) => <Status key={role} value={role} />)}</div><Status value={user.disabled ? 'disabled' : 'active'} /></article>)}</div>
}

function AuditTable({ items }: { items: Audit[] }) {
  if (!items.length) return <Empty title="Audit log is empty" detail="Authentication and configuration changes appear here." />
  return <div className="table-scroll"><table><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Resource</th><th>Outcome</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td>{new Date(item.createdAt).toLocaleString()}</td><td>{item.actor ?? 'system'}</td><td><strong>{item.action}</strong></td><td>{item.resourceType}<small>{item.resourceId}</small></td><td><Status value={item.outcome} /></td></tr>)}</tbody></table></div>
}

function UserModal({ close, saved }: { close: () => void; saved: () => void }) {
  const [displayName, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('viewer')
  const [error, setError] = useState('')
  const submit = () => api.post('/users', { displayName, email, password, role }).then(saved).catch((reason: Error) => setError(reason.message))
  return <Modal title="Add user" close={close}>{error && <ErrorNotice message={error} />}<Field label="Display name"><input value={displayName} onChange={(event) => setName(event.target.value)} /></Field><Field label="Email"><input type="email" value={email} onChange={(event) => setEmail(event.target.value)} /></Field><Field label="Temporary password"><input type="password" minLength={12} value={password} onChange={(event) => setPassword(event.target.value)} /></Field><Field label="Role"><select value={role} onChange={(event) => setRole(event.target.value)}><option value="viewer">Viewer</option><option value="operator">Operator</option><option value="admin">Admin</option></select></Field><div className="role-explainer"><Shield size={18} /><span><strong>{role}</strong>{role === 'admin' ? ' can manage users, credentials, and approvals.' : role === 'operator' ? ' can manage nodes, skills, jobs, and MCP.' : ' has read-only visibility.'}</span></div><div className="modal-actions"><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={submit} disabled={!displayName || !email || password.length < 12}><UserCog size={15} />Create user</Button></div></Modal>
}
