import { Activity, Plus, RefreshCw, ServerCog, ToggleLeft, ToggleRight } from 'lucide-react'
import { useState } from 'react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'

interface MCPServer extends Dict { id: string; name: string; transport: string; command: string; url: string; enabled: boolean; healthStatus: string; source: string; envRefs: Record<string, string> }
interface Profile { id: string; name: string; description: string; enabled: boolean; serverIds: string[] }
interface Deployment { id: string; profileName: string; nodeName: string; runtime: string; state: string; lastError: string }

export default function MCP() {
  const [tab, setTab] = useState('Servers')
  const [modal, setModal] = useState<'server' | 'profile' | null>(null)
  const [error, setError] = useState('')
  const state = useData(async () => {
    const [servers, profiles, deployments] = await Promise.all([api.list<MCPServer>('/mcp/servers'), api.list<Profile>('/mcp/profiles'), api.list<Deployment>('/mcp/deployments')])
    return { servers: servers.items, profiles: profiles.items, deployments: deployments.items }
  }, [])
  const toggle = (server: MCPServer) => api.patch(`/mcp/servers/${server.id}`, { enabled: !server.enabled }).then(state.reload).catch((reason: Error) => setError(reason.message))
  const health = (id: string) => api.post(`/mcp/servers/${id}/health`).then(state.reload).catch((reason: Error) => setError(reason.message))
  return <>
    <PageHeader title="MCP" detail="Servers, reusable profiles, health, usage, and runtime distribution." actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />Refresh</Button><Button onClick={() => setModal(tab === 'Profiles' ? 'profile' : 'server')}><Plus size={16} />Add {tab === 'Profiles' ? 'profile' : 'server'}</Button></>} />
    {error && <ErrorNotice message={error} />}
    <div className="toolbar"><Segments options={['Servers', 'Profiles', 'Deployments']} value={tab} onChange={setTab} /></div>
    {state.loading ? <Loading label="Loading MCP inventory" /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : tab === 'Servers' ? <ServerTable items={state.data.servers} toggle={toggle} health={health} /> : tab === 'Profiles' ? <ProfileTable items={state.data.profiles} /> : <DeploymentTable items={state.data.deployments} />}
    {modal === 'server' && <ServerModal close={() => setModal(null)} saved={() => { setModal(null); state.reload() }} />}
    {modal === 'profile' && state.data && <ProfileModal servers={state.data.servers} close={() => setModal(null)} saved={() => { setModal(null); state.reload() }} />}
  </>
}

function ServerTable({ items, toggle, health }: { items: MCPServer[]; toggle: (item: MCPServer) => void; health: (id: string) => void }) {
  if (!items.length) return <Empty title="No MCP servers" detail="Add a stdio, SSE, or Streamable HTTP server." />
  return <div className="table-scroll"><table><thead><tr><th>Server</th><th>Transport</th><th>Endpoint</th><th>Secrets</th><th>Health</th><th /></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td><strong>{item.name}</strong><small>{item.source}</small></td><td>{item.transport}</td><td><code>{item.command || item.url}</code></td><td>{Object.keys(item.envRefs ?? {}).length} refs</td><td><Status value={item.healthStatus} /></td><td className="row-actions"><IconButton label="Run health check" onClick={() => health(item.id)}><Activity size={16} /></IconButton><IconButton label={item.enabled ? 'Disable server' : 'Enable server'} onClick={() => toggle(item)}>{item.enabled ? <ToggleRight size={19} /> : <ToggleLeft size={19} />}</IconButton></td></tr>)}</tbody></table></div>
}

function ProfileTable({ items }: { items: Profile[] }) {
  return items.length ? <div className="profile-list">{items.map((item) => <article key={item.id}><ServerCog size={20} /><div><strong>{item.name}</strong><p>{item.description || 'No description'}</p></div><span>{item.serverIds.length} servers</span><Status value={item.enabled ? 'enabled' : 'disabled'} /></article>)}</div> : <Empty title="No MCP profiles" detail="Group servers into a reusable runtime profile." />
}

function DeploymentTable({ items }: { items: Deployment[] }) {
  return items.length ? <div className="table-scroll"><table><thead><tr><th>Profile</th><th>Node</th><th>Runtime</th><th>State</th><th>Last error</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td><strong>{item.profileName}</strong></td><td>{item.nodeName}</td><td>{item.runtime}</td><td><Status value={item.state} /></td><td>{item.lastError || '—'}</td></tr>)}</tbody></table></div> : <Empty title="No MCP deployments" detail="Deploy a profile to a node and runtime target." />
}

function ServerModal({ close, saved }: { close: () => void; saved: () => void }) {
  const [name, setName] = useState('')
  const [transport, setTransport] = useState('stdio')
  const [endpoint, setEndpoint] = useState('')
  const [env, setEnv] = useState('')
  const [error, setError] = useState('')
  const submit = () => {
    const environment = Object.fromEntries(env.split('\n').map((line) => line.split('=')).filter((pair) => pair.length === 2))
    api.post('/mcp/servers', { name, transport, command: transport === 'stdio' ? endpoint : '', args: [], url: transport === 'stdio' ? '' : endpoint, env: environment, enabled: true }).then(saved).catch((reason: Error) => setError(reason.message))
  }
  return <Modal title="Add MCP server" close={close}>{error && <ErrorNotice message={error} />}<Field label="Name"><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label="Transport"><select value={transport} onChange={(event) => setTransport(event.target.value)}><option value="stdio">stdio</option><option value="sse">SSE</option><option value="streamable-http">Streamable HTTP</option></select></Field><Field label={transport === 'stdio' ? 'Command' : 'URL'}><input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} /></Field><Field label="Environment secrets" hint="One NAME=value per line. Values are encrypted before storage."><textarea rows={4} value={env} onChange={(event) => setEnv(event.target.value)} /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={submit} disabled={!name || !endpoint}>Create server</Button></div></Modal>
}

function ProfileModal({ servers, close, saved }: { servers: MCPServer[]; close: () => void; saved: () => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const submit = () => api.post('/mcp/profiles', { name, description, serverIds: selected }).then(saved)
  return <Modal title="Create MCP profile" close={close}><Field label="Name"><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label="Description"><input value={description} onChange={(event) => setDescription(event.target.value)} /></Field><div className="check-list">{servers.map((server) => <label key={server.id}><input type="checkbox" checked={selected.includes(server.id)} onChange={() => setSelected((current) => current.includes(server.id) ? current.filter((id) => id !== server.id) : [...current, server.id])} /><span>{server.name}<small>{server.transport}</small></span></label>)}</div><div className="modal-actions"><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={submit} disabled={!name}>Create profile</Button></div></Modal>
}
