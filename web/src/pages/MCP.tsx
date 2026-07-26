import { Activity, FileJson, Plus, RefreshCw, ServerCog, ToggleLeft, ToggleRight } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface MCPOrigin { importSource?: string; importSourceName?: string; serverName?: string; managedRuntime?: string }
interface MCPServer extends Dict { id: string; name: string; runtimeName: string; transport: string; command: string; url: string; enabled: boolean; healthStatus: string; source: string; origin?: MCPOrigin; authority: string; credentialMode: string; sharedSourceId?: string; envRefs: Record<string, string>; headerRefs: Record<string, string>; bindingCount: number; hasDrift: boolean }
interface Profile { id: string; name: string; description: string; enabled: boolean; source: string; origin?: MCPOrigin; serverIds: string[] }
interface Deployment { id: string; profileName: string; source: string; nodeName: string; runtime: string; state: string; lastError: string; bindings: { id: string; serverName: string; missing: boolean; drift: boolean }[] }
interface SharedSource { id: string; nodeName: string; name: string; mode: string; autoSync: boolean; status: string; lastError: string; blockedSkills: { name: string; path: string; error: string }[]; mcpServers: { id: string; name: string; transport: string; enabled: boolean; authority: string; credentialMode: string; envKeys: string[]; headerKeys: string[] }[]; consumers: { kind: string; inheritsFrom: string; state: string; expectedFingerprint: string; actualFingerprint: string; lastError: string; mcpBindings: { serverName: string; enabled: boolean; missing: boolean; drift: boolean }[] }[] }

export default function MCP() {
  const { t } = useI18n()
  const [tab, setTab] = useState('Servers')
  const [modal, setModal] = useState<'server' | 'profile' | null>(null)
  const [error, setError] = useState('')
  const state = useData(async () => {
    const [servers, profiles, deployments, sharedSources] = await Promise.all([api.list<MCPServer>('/mcp/servers'), api.list<Profile>('/mcp/profiles'), api.list<Deployment>('/mcp/deployments'), api.list<SharedSource>('/shared-sources')])
    return { servers: servers.items, profiles: profiles.items, deployments: deployments.items, sharedSources: sharedSources.items }
  }, [])
  const toggle = (server: MCPServer) => api.patch(`/mcp/servers/${server.id}`, { enabled: !server.enabled }).then(state.reload).catch((reason: Error) => setError(reason.message))
  const health = (id: string) => api.post(`/mcp/servers/${id}/health`).then(state.reload).catch((reason: Error) => setError(reason.message))
  const addAction = tab === 'Profiles' ? <Button onClick={() => setModal('profile')}><Plus size={16} />{t('Add profile')}</Button> : tab === 'Servers' ? <Button onClick={() => setModal('server')}><Plus size={16} />{t('Add server')}</Button> : undefined
  return <>
    <PageHeader title={t('MCP')} detail={t('Servers, reusable profiles, health, usage, and runtime distribution.')} actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button>{addAction}</>} />
    {error && <ErrorNotice message={error} />}
    <div className="toolbar"><Segments options={['Servers', 'Shared Sources', 'Profiles', 'Deployments']} value={tab} onChange={setTab} /></div>
    {state.loading ? <Loading label={t('Loading MCP inventory')} /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : tab === 'Servers' ? <ServerTable items={state.data.servers} toggle={toggle} health={health} /> : tab === 'Shared Sources' ? <SharedSourceTable items={state.data.sharedSources} /> : tab === 'Profiles' ? <ProfileTable items={state.data.profiles} servers={state.data.servers} saved={state.reload} /> : <DeploymentTable items={state.data.deployments} />}
    {modal === 'server' && <ServerModal close={() => setModal(null)} saved={() => { setModal(null); state.reload() }} />}
    {modal === 'profile' && state.data && <ProfileModal servers={state.data.servers} close={() => setModal(null)} saved={() => { setModal(null); state.reload() }} />}
  </>
}

function ServerTable({ items, toggle, health }: { items: MCPServer[]; toggle: (item: MCPServer) => void; health: (id: string) => void }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('No MCP servers')} detail={t('Add a stdio, SSE, or Streamable HTTP server.')} />
  return <div className="table-scroll"><table><thead><tr><th>{t('Server')}</th><th>{t('Transport')}</th><th>{t('Endpoint')}</th><th>{t('Bindings')}</th><th>{t('State')}</th><th /></tr></thead><tbody>{items.map((item) => {
    const shared = item.authority === 'shared-file'
    const candidate = item.source === 'shared-import'
    const imported = item.source === 'mcpm-import' || candidate
    const conflict = candidate && item.name !== item.runtimeName
    const sourceName = item.origin?.importSourceName || item.origin?.importSource || item.source
    const sourceDetail = shared
      ? t('Observed legacy manifest · node-local credentials')
      : item.source === 'runtime-auto'
        ? t('Auto-managed · runtime name {name}', { name: item.runtimeName })
        : imported
          ? t('Imported from {source} · runtime name {name}', { source: sourceName, name: item.runtimeName })
          : item.source
    const stateValue = item.hasDrift ? 'drift' : conflict ? 'conflict' : candidate && !item.enabled ? 'candidate' : !item.enabled ? 'disabled' : item.healthStatus
    return <tr key={item.id}><td><strong>{item.name}</strong><small>{sourceDetail}</small>{(candidate || conflict) && <span className="label-row">{candidate && <Status value="candidate" />}{conflict && <Status value="name conflict" />}</span>}</td><td>{item.transport}</td><td><code>{item.command || item.url}</code></td><td>{item.bindingCount ?? 0}<small>{shared ? t('Credential values stay on the node') : t('{n} encrypted refs', { n: Object.keys(item.envRefs ?? {}).length + Object.keys(item.headerRefs ?? {}).length })}</small></td><td><Status value={stateValue} /></td><td className="row-actions"><IconButton label={shared ? t('Shared-file servers are read-only here') : t('Run health check')} disabled={shared} onClick={() => health(item.id)}><Activity size={16} /></IconButton><IconButton label={shared ? t('Legacy manifests are import-only') : item.enabled ? t('Disable server') : t('Enable server')} disabled={shared} onClick={() => toggle(item)}>{item.enabled ? <ToggleRight size={19} /> : <ToggleLeft size={19} />}</IconButton></td></tr>
  })}</tbody></table></div>
}

function SharedSourceTable({ items }: { items: SharedSource[] }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('No shared MCP sources')} detail={t('Configure sharedSources locally on an Agent or allow observed auto-probe.')} />
  return <div className="shared-source-list">{items.map((source) => <article key={source.id}><header><FileJson size={19} /><span><strong>{source.name}</strong><small>{source.nodeName} · {t('Read-only import source')}</small></span><Status value={source.status} /></header>{source.lastError && <div className="inline-notice">{source.lastError}</div>}{source.blockedSkills?.length > 0 && <div className="inline-notice">{source.blockedSkills.map((skill) => <div key={skill.name}><strong>{skill.name}</strong> — {skill.error}</div>)}</div>}<div className="shared-mcp-grid"><section><h3>{t('Manifest candidates')}</h3>{source.mcpServers.map((server) => <div key={server.id}><strong>{server.name}</strong><small>{server.transport} · {server.enabled ? t('enabled') : t('disabled')} · {server.credentialMode}</small><small>{t('Env keys: {keys}; header keys: {headers}', { keys: server.envKeys?.join(', ') || '—', headers: server.headerKeys?.join(', ') || '—' })}</small></div>)}</section><section><h3>{t('Observed legacy consumers')}</h3>{source.consumers.map((consumer) => <div key={consumer.kind}><strong>{consumer.kind}{consumer.inheritsFrom ? ` ← ${consumer.inheritsFrom}` : ''}</strong><Status value={consumer.state} /><small>{t('{n} bindings', { n: consumer.mcpBindings?.length ?? 0 })}</small>{consumer.lastError && <small>{consumer.lastError}</small>}</div>)}</section></div></article>)}</div>
}

function ProfileTable({ items, servers, saved }: { items: Profile[]; servers: MCPServer[]; saved: () => void }) {
  const { t } = useI18n()
  const managed = items.filter((item) => {
    const runtime = item.origin?.managedRuntime
    return (runtime === 'codex' || runtime === 'claude') && item.name === `toolhub-${runtime}`
  })
  const selectable = servers.filter((server) => server.authority !== 'shared-file')
  const [drafts, setDrafts] = useState<Record<string, string[]>>({})
  const [saving, setSaving] = useState('')
  const [error, setError] = useState('')
  useEffect(() => setDrafts(Object.fromEntries(managed.map((profile) => [profile.id, [...profile.serverIds]]))), [items])
  if (!items.length) return <Empty title={t('No MCP profiles')} detail={t('Group servers into a reusable runtime profile.')} />
  const selected = (profile: Profile) => drafts[profile.id] ?? profile.serverIds
  const toggleMembership = (profile: Profile, serverID: string) => setDrafts((current) => {
    const members = current[profile.id] ?? profile.serverIds
    return { ...current, [profile.id]: members.includes(serverID) ? members.filter((id) => id !== serverID) : [...members, serverID] }
  })
  const saveMembership = (profile: Profile) => {
    setSaving(profile.id)
    setError('')
    api.put(`/mcp/profiles/${profile.id}/servers`, { serverIds: selected(profile) }).then(saved).catch((reason: Error) => setError(reason.message)).finally(() => setSaving(''))
  }
  return <><div className="profile-list">{items.map((item) => <article key={item.id}><ServerCog size={20} /><div><strong>{item.name}</strong><p>{item.description || t('No description')}</p></div><span>{t('{n} servers', { n: item.serverIds.length })}</span><Status value={item.enabled ? 'enabled' : 'disabled'} /></article>)}</div>{managed.length > 0 && <><h3>{t('Managed runtime membership')}</h3><div className="inline-notice">{t('Membership changes stay observed until the fixed profile is explicitly deployed.')}</div>{error && <ErrorNotice message={error} />}<div className="table-scroll"><table><thead><tr><th>{t('Server')}</th>{managed.map((profile) => <th key={profile.id}>{profile.name}<small>{profile.origin?.managedRuntime}</small></th>)}</tr></thead><tbody>{selectable.map((server) => <tr key={server.id}><td><strong>{server.name}</strong><small>{server.source}</small></td>{managed.map((profile) => <td key={profile.id}><input type="checkbox" checked={selected(profile).includes(server.id)} disabled={saving !== ''} aria-label={t('Include {server} in {profile}', { server: server.name, profile: profile.name })} onChange={() => toggleMembership(profile, server.id)} /></td>)}</tr>)}</tbody></table></div><div className="modal-actions">{managed.map((profile) => <Button key={profile.id} onClick={() => saveMembership(profile)} disabled={saving !== '' || sameIDs(selected(profile), profile.serverIds)}>{saving === profile.id ? t('Saving {profile}...', { profile: profile.name }) : t('Save {profile}', { profile: profile.name })}</Button>)}</div></>}</>
}

function sameIDs(left: string[], right: string[]) {
  if (left.length !== right.length) return false
  const sortedLeft = [...left].sort()
  const sortedRight = [...right].sort()
  return sortedLeft.every((value, index) => value === sortedRight[index])
}

function DeploymentTable({ items }: { items: Deployment[] }) {
  const { t } = useI18n()
  return items.length ? <div className="table-scroll"><table><thead><tr><th>{t('Profile')}</th><th>{t('Node / runtime')}</th><th>{t('Bindings')}</th><th>{t('State')}</th><th>{t('Last error')}</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td><strong>{item.profileName}</strong><small>{item.source === 'runtime-auto' ? t('Auto-managed') : item.source}</small></td><td>{item.nodeName}<small>{item.runtime}</small></td><td>{item.bindings?.length ?? 0}{item.bindings?.some((binding) => binding.drift || binding.missing) && <small>{t('drift / missing detected')}</small>}</td><td><Status value={item.bindings?.some((binding) => binding.drift || binding.missing) ? 'drift' : item.state} /></td><td>{item.lastError || '—'}</td></tr>)}</tbody></table></div> : <Empty title={t('No MCP deployments')} detail={t('Discovered MCP servers are automatically baselined; manual profiles can also be deployed.')} />
}

function ServerModal({ close, saved }: { close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [transport, setTransport] = useState('stdio')
  const [endpoint, setEndpoint] = useState('')
  const [env, setEnv] = useState('')
  const [headers, setHeaders] = useState('')
  const [error, setError] = useState('')
  const submit = () => {
    const environment = parseSecretLines(env)
    const headerValues = parseSecretLines(headers)
    api.post('/mcp/servers', { name, transport, command: transport === 'stdio' ? endpoint : '', args: [], url: transport === 'stdio' ? '' : endpoint, env: environment, headers: headerValues, enabled: true }).then(saved).catch((reason: Error) => setError(reason.message))
  }
  return <Modal title={t('Add MCP server')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Name')}><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('Transport')}><select value={transport} onChange={(event) => setTransport(event.target.value)}><option value="stdio">stdio</option><option value="sse">SSE</option><option value="streamable-http">Streamable HTTP</option></select></Field><Field label={transport === 'stdio' ? t('Command') : t('URL')}><input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} /></Field><Field label={t('Environment secrets')} hint={t('One NAME=value per line. Values are encrypted before storage.')}><textarea rows={4} value={env} onChange={(event) => setEnv(event.target.value)} /></Field><Field label={t('HTTP header secrets')} hint={t('One Header-Name=value per line. Values are encrypted before storage.')}><textarea rows={4} value={headers} onChange={(event) => setHeaders(event.target.value)} /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={!name || !endpoint}>{t('Create server')}</Button></div></Modal>
}

function parseSecretLines(value: string): Record<string, string> {
  return Object.fromEntries(value.split('\n').map((line) => {
    const separator = line.indexOf('=')
    return separator > 0 ? [line.slice(0, separator).trim(), line.slice(separator + 1)] : null
  }).filter((pair): pair is [string, string] => pair !== null && pair[0] !== '' && pair[1] !== ''))
}

function ProfileModal({ servers, close, saved }: { servers: MCPServer[]; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const submit = () => api.post('/mcp/profiles', { name, description, serverIds: selected }).then(saved)
  const editableServers = servers.filter((server) => server.authority !== 'shared-file')
  return <Modal title={t('Create MCP profile')} close={close}><Field label={t('Name')}><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('Description')}><input value={description} onChange={(event) => setDescription(event.target.value)} /></Field><div className="check-list">{editableServers.map((server) => <label key={server.id}><input type="checkbox" checked={selected.includes(server.id)} onChange={() => setSelected((current) => current.includes(server.id) ? current.filter((id) => id !== server.id) : [...current, server.id])} /><span>{server.name}<small>{server.transport}</small></span></label>)}</div><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={!name}>{t('Create profile')}</Button></div></Modal>
}
