import {
  Activity,
  Archive,
  ArchiveRestore,
  Download,
  FileJson,
  KeyRound,
  Pencil,
  Plus,
  RefreshCw,
  ServerCog,
  ToggleLeft,
  ToggleRight,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, APIError, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface MCPOrigin { importSource?: string; importSourceName?: string; serverName?: string; managedRuntime?: string }
interface MCPServer extends Dict {
  id: string
  name: string
  runtimeName: string
  transport: string
  command: string
  args: string[]
  url: string
  enabled: boolean
  healthStatus: string
  source: string
  origin?: MCPOrigin
  authority: string
  credentialMode: string
  sharedSourceId?: string
  envKeys: string[]
  headerKeys: string[]
  bindingCount: number
  hasDrift: boolean
  archivedAt?: string
}
interface Profile { id: string; name: string; description: string; enabled: boolean; source: string; origin?: MCPOrigin; serverIds: string[] }
interface Deployment { id: string; profileName: string; source: string; nodeName: string; runtime: string; state: string; lastError: string; bindings: { id: string; serverName: string; missing: boolean; drift: boolean }[] }
interface SharedSource { id: string; nodeName: string; name: string; mode: string; autoSync: boolean; status: string; lastError: string; blockedSkills: { name: string; path: string; error: string }[]; mcpServers: { id: string; name: string; transport: string; enabled: boolean; authority: string; credentialMode: string; envKeys: string[]; headerKeys: string[] }[]; consumers: { kind: string; inheritsFrom: string; state: string; expectedFingerprint: string; actualFingerprint: string; lastError: string; mcpBindings: { serverName: string; enabled: boolean; missing: boolean; drift: boolean }[] }[] }
interface MCPDiscovery {
  id: string
  kind: string
  nodeName: string
  runtime: string
  name: string
  missing: boolean
  status: string
  lastError: string
  serverId: string
  controlMode: string
  sourceChanged: boolean
  importStatus: string
  observedGeneration: number
  envKeys: string[]
  headerKeys: string[]
}
interface AffectedTarget { nodeId: string; nodeName: string; runtime: string }
interface SecretConfirmation { envKeys: string[]; headerKeys: string[]; targets: AffectedTarget[] }

export default function MCP({ canOperate, canImport }: { canOperate: boolean; canImport: boolean }) {
  const { t } = useI18n()
  const [tab, setTab] = useState('Servers')
  const [modal, setModal] = useState<'server' | 'profile' | null>(null)
  const [editing, setEditing] = useState<MCPServer | null>(null)
  const [importConfirmation, setImportConfirmation] = useState<{ discovery: MCPDiscovery; details: SecretConfirmation } | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [provenance, setProvenance] = useState('all')
  const [showArchived, setShowArchived] = useState(false)
  const state = useData(async () => {
    const [servers, discoveries, profiles, deployments, sharedSources] = await Promise.all([
      api.list<MCPServer>('/mcp/servers'),
      api.list<MCPDiscovery>('/discoveries'),
      api.list<Profile>('/mcp/profiles'),
      api.list<Deployment>('/mcp/deployments'),
      api.list<SharedSource>('/shared-sources'),
    ])
    return {
      servers: servers.items,
      discoveries: discoveries.items.filter((item) => item.kind === 'mcp' && item.runtime === 'hermes' && item.controlMode === 'read_only_source'),
      profiles: profiles.items,
      deployments: deployments.items,
      sharedSources: sharedSources.items,
    }
  }, [])

  const runServerAction = async (key: string, action: () => Promise<unknown>) => {
    setBusy(key)
    setError('')
    setNotice('')
    try {
      await action()
      await state.reload()
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setBusy('')
    }
  }
  const importDiscovery = async (discovery: MCPDiscovery, confirmSecrets = false) => {
    setBusy(`import-${discovery.id}`)
    setError('')
    setNotice('')
    try {
      await api.post(`/discoveries/${discovery.id}/import-mcp`, { observedGeneration: discovery.observedGeneration, confirmSecrets })
      setImportConfirmation(null)
      setNotice(t('Hermes MCP import queued.'))
      await state.reload()
    } catch (reason) {
      const confirmation = secretConfirmationFromError(reason)
      if (!confirmSecrets && confirmation) {
        setImportConfirmation({ discovery, details: confirmation })
      } else {
        setImportConfirmation(null)
        setError((reason as Error).message)
        if (reason instanceof APIError && reason.code === 'source_changed') await state.reload()
      }
    } finally {
      setBusy('')
    }
  }
  const completed = async (message: string) => {
    setModal(null)
    setEditing(null)
    setError('')
    setNotice(message)
    await state.reload()
  }
  const serverProvenance = (server: MCPServer) => [server.source, server.origin?.importSourceName].filter(Boolean).join(' · ')
  const provenanceOptions = [...new Set((state.data?.servers ?? []).map(serverProvenance))].sort()
  const visibleServers = (state.data?.servers ?? []).filter((server) => (showArchived || !server.archivedAt) && (provenance === 'all' || serverProvenance(server) === provenance))
  const addAction = canOperate && tab === 'Profiles'
    ? <Button onClick={() => setModal('profile')}><Plus size={16} />{t('Add profile')}</Button>
    : canOperate && tab === 'Servers'
      ? <Button onClick={() => setModal('server')}><Plus size={16} />{t('Add server')}</Button>
      : undefined

  return <>
    <PageHeader title={t('MCP')} detail={t('Servers, reusable profiles, health, usage, and runtime distribution.')} actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button>{addAction}</>} />
    {error && <ErrorNotice message={error} />}
    {notice && <div className="inline-notice">{notice}</div>}
    <div className="toolbar">
      <div className="segment-scroll"><Segments options={['Servers', 'Discovered', 'Shared Sources', 'Profiles', 'Deployments']} value={tab} onChange={setTab} /></div>
      {tab === 'Servers' && <>
        <label className="toolbar-select"><span>{t('Provenance')}</span><select aria-label={t('MCP provenance')} value={provenance} onChange={(event) => setProvenance(event.target.value)}><option value="all">{t('All sources')}</option>{provenanceOptions.map((source) => <option key={source} value={source}>{source}</option>)}</select></label>
        <label className="toggle-control"><input type="checkbox" checked={showArchived} onChange={(event) => setShowArchived(event.target.checked)} />{t('Show archived')}</label>
      </>}
    </div>
    {state.loading ? <Loading label={t('Loading MCP inventory')} /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : tab === 'Servers'
      ? <ServerTable items={visibleServers} canOperate={canOperate} busy={busy} edit={setEditing} toggle={(server) => runServerAction(`toggle-${server.id}`, () => api.patch(`/mcp/servers/${server.id}`, { enabled: !server.enabled }))} health={(server) => runServerAction(`health-${server.id}`, () => api.post(`/mcp/servers/${server.id}/health`))} archive={(server) => runServerAction(`archive-${server.id}`, () => api.post(`/mcp/servers/${server.id}/${server.archivedAt ? 'unarchive' : 'archive'}`))} />
      : tab === 'Discovered'
        ? <DiscoveryTable items={state.data.discoveries} canImport={canImport} busy={busy} importDiscovery={importDiscovery} />
        : tab === 'Shared Sources'
          ? <SharedSourceTable items={state.data.sharedSources} />
          : tab === 'Profiles'
            ? <ProfileTable items={state.data.profiles} servers={state.data.servers} canOperate={canOperate} saved={() => completed(t('MCP profile updated.'))} />
            : <DeploymentTable items={state.data.deployments} />}
    {modal === 'server' && <ServerModal close={() => setModal(null)} saved={() => completed(t('MCP server created.'))} />}
    {modal === 'profile' && state.data && <ProfileModal servers={state.data.servers} close={() => setModal(null)} saved={() => completed(t('MCP profile created.'))} />}
    {editing && <ServerEditor server={editing} close={() => setEditing(null)} saved={() => completed(t('MCP server updated.'))} />}
    {importConfirmation && <SecretConfirmationModal
      title={t('Confirm Hermes MCP import')}
      details={importConfirmation.details}
      busy={busy === `import-${importConfirmation.discovery.id}`}
      close={() => setImportConfirmation(null)}
      confirm={() => importDiscovery(importConfirmation.discovery, true)}
      confirmLabel={t('Capture and import')}
    />}
  </>
}

function ServerTable({ items, canOperate, busy, edit, toggle, health, archive }: { items: MCPServer[]; canOperate: boolean; busy: string; edit: (item: MCPServer) => void; toggle: (item: MCPServer) => void; health: (item: MCPServer) => void; archive: (item: MCPServer) => void }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('No MCP servers')} detail={t('Add a stdio, SSE, or Streamable HTTP server.')} />
  return <div className="table-scroll"><table><thead><tr><th>{t('Server')}</th><th>{t('Transport')}</th><th>{t('Endpoint')}</th><th>{t('Bindings')}</th><th>{t('State')}</th><th aria-label={t('Actions')} /></tr></thead><tbody>{items.map((item) => {
    const shared = item.authority === 'shared-file'
    const candidate = item.source === 'shared-import'
    const imported = ['mcpm-import', 'shared-import', 'hermes-import'].includes(item.source)
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
    const itemBusy = busy.endsWith(item.id)
    return <tr key={item.id}>
      <td><strong>{item.name}</strong><small>{sourceDetail}</small>{(candidate || conflict) && <span className="label-row">{candidate && <Status value="candidate" />}{conflict && <Status value="name conflict" />}</span>}</td>
      <td>{item.transport}</td>
      <td><code>{item.command || item.url}</code>{item.args?.length > 0 && <small>{t('{n} arguments', { n: item.args.length })}</small>}</td>
      <td>{item.bindingCount ?? 0}<small>{shared ? t('Credential values stay on the node') : t('{n} encrypted keys', { n: (item.envKeys?.length ?? 0) + (item.headerKeys?.length ?? 0) })}</small></td>
      <td><Status value={item.archivedAt ? 'archived' : stateValue} /></td>
      <td className="row-actions">{canOperate && <>
        <IconButton label={t('Edit server')} disabled={shared || Boolean(item.archivedAt) || itemBusy} onClick={() => edit(item)}><Pencil size={16} /></IconButton>
        <IconButton label={shared ? t('Shared-file servers are read-only here') : t('Run health check')} disabled={shared || Boolean(item.archivedAt) || itemBusy} onClick={() => health(item)}><Activity size={16} /></IconButton>
        <IconButton label={shared ? t('Legacy manifests are import-only') : item.enabled ? t('Disable server') : t('Enable server')} disabled={shared || Boolean(item.archivedAt) || itemBusy} onClick={() => toggle(item)}>{item.enabled ? <ToggleRight size={19} /> : <ToggleLeft size={19} />}</IconButton>
        <IconButton label={item.archivedAt ? t('Unarchive server') : t('Archive server')} disabled={shared || itemBusy} onClick={() => archive(item)}>{item.archivedAt ? <ArchiveRestore size={17} /> : <Archive size={17} />}</IconButton>
      </>}</td>
    </tr>
  })}</tbody></table></div>
}

function DiscoveryTable({ items, canImport, busy, importDiscovery }: { items: MCPDiscovery[]; canImport: boolean; busy: string; importDiscovery: (item: MCPDiscovery) => void }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('No Hermes MCP discoveries')} detail={t('Hermes MCP snapshots appear after Agent inventory scans.')} />
  return <div className="table-scroll"><table><thead><tr><th>{t('Candidate')}</th><th>{t('Source')}</th><th>{t('Secret keys')}</th><th>{t('Snapshot')}</th><th>{t('State')}</th><th aria-label={t('Actions')} /></tr></thead><tbody>{items.map((item) => {
    const importing = item.importStatus === 'queued' || item.importStatus === 'importing'
    const reimport = Boolean(item.serverId) || item.importStatus === 'imported' || item.sourceChanged
    return <tr key={item.id}>
      <td><strong>{item.name}</strong><small>{t('Hermes read-only candidate')}</small></td>
      <td>{item.nodeName}<small>{item.runtime}</small></td>
      <td><DiscoveryKeys envKeys={item.envKeys} headerKeys={item.headerKeys} /></td>
      <td>{t('Generation {n}', { n: item.observedGeneration })}<small>{item.sourceChanged ? t('Source changed since the last import') : t('Pinned when import starts')}</small></td>
      <td><span className="label-row"><Status value={item.sourceChanged ? 'source changed' : item.importStatus || item.status} /><Status value="read only" /></span>{item.lastError && <small>{item.lastError}</small>}</td>
      <td className="row-actions">{canImport && <Button variant="secondary" disabled={item.missing || importing || busy === `import-${item.id}`} onClick={() => importDiscovery(item)}><Download size={16} />{t(reimport ? 'Re-import' : 'Import')}</Button>}</td>
    </tr>
  })}</tbody></table></div>
}

function DiscoveryKeys({ envKeys, headerKeys }: { envKeys: string[]; headerKeys: string[] }) {
  const { t } = useI18n()
  const env = envKeys?.join(', ') || '—'
  const headers = headerKeys?.join(', ') || '—'
  return <><code>{env}</code><small>{t('Headers: {keys}', { keys: headers })}</small></>
}

function SharedSourceTable({ items }: { items: SharedSource[] }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('No shared MCP sources')} detail={t('Configure sharedSources locally on an Agent or allow observed auto-probe.')} />
  return <div className="shared-source-list">{items.map((source) => <article key={source.id}><header><FileJson size={19} /><span><strong>{source.name}</strong><small>{source.nodeName} · {t('Read-only import source')}</small></span><Status value={source.status} /></header>{source.lastError && <div className="inline-notice">{source.lastError}</div>}{source.blockedSkills?.length > 0 && <div className="inline-notice">{source.blockedSkills.map((skill) => <div key={skill.name}><strong>{skill.name}</strong> — {skill.error}</div>)}</div>}<div className="shared-mcp-grid"><section><h3>{t('Manifest candidates')}</h3>{source.mcpServers.map((server) => <div key={server.id}><strong>{server.name}</strong><small>{server.transport} · {server.enabled ? t('enabled') : t('disabled')} · {server.credentialMode}</small><small>{t('Env keys: {keys}; header keys: {headers}', { keys: server.envKeys?.join(', ') || '—', headers: server.headerKeys?.join(', ') || '—' })}</small></div>)}</section><section><h3>{t('Observed legacy consumers')}</h3>{source.consumers.map((consumer) => <div key={consumer.kind}><strong>{consumer.kind}{consumer.inheritsFrom ? ` ← ${consumer.inheritsFrom}` : ''}</strong><Status value={consumer.state} /><small>{t('{n} bindings', { n: consumer.mcpBindings?.length ?? 0 })}</small>{consumer.lastError && <small>{consumer.lastError}</small>}</div>)}</section></div></article>)}</div>
}

function ProfileTable({ items, servers, canOperate, saved }: { items: Profile[]; servers: MCPServer[]; canOperate: boolean; saved: () => void }) {
  const { t } = useI18n()
  const managed = items.filter((item) => {
    const runtime = item.origin?.managedRuntime
    return (runtime === 'codex' || runtime === 'claude') && item.name === `toolhub-${runtime}`
  })
  const selectable = servers.filter((server) => server.authority !== 'shared-file' && !server.archivedAt)
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
  return <><div className="profile-list">{items.map((item) => <article key={item.id}><ServerCog size={20} /><div><strong>{item.name}</strong><p>{item.description || t('No description')}</p></div><span>{t('{n} servers', { n: item.serverIds.length })}</span><Status value={item.enabled ? 'enabled' : 'disabled'} /></article>)}</div>{managed.length > 0 && <><h3>{t('Managed runtime membership')}</h3><div className="inline-notice">{t('Membership changes stay observed until the fixed profile is explicitly deployed.')}</div>{error && <ErrorNotice message={error} />}<div className="table-scroll"><table><thead><tr><th>{t('Server')}</th>{managed.map((profile) => <th key={profile.id}>{profile.name}<small>{profile.origin?.managedRuntime}</small></th>)}</tr></thead><tbody>{selectable.map((server) => <tr key={server.id}><td><strong>{server.name}</strong><small>{server.source}</small></td>{managed.map((profile) => <td key={profile.id}><input type="checkbox" checked={selected(profile).includes(server.id)} disabled={!canOperate || saving !== ''} aria-label={t('Include {server} in {profile}', { server: server.name, profile: profile.name })} onChange={() => toggleMembership(profile, server.id)} /></td>)}</tr>)}</tbody></table></div>{canOperate && <div className="modal-actions">{managed.map((profile) => <Button key={profile.id} onClick={() => saveMembership(profile)} disabled={saving !== '' || sameIDs(selected(profile), profile.serverIds)}>{saving === profile.id ? t('Saving {profile}...', { profile: profile.name }) : t('Save {profile}', { profile: profile.name })}</Button>)}</div>}</>}</>
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
  const [args, setArgs] = useState('')
  const [env, setEnv] = useState('')
  const [headers, setHeaders] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const submit = async () => {
    const environment = parseSecretLines(env)
    const headerValues = parseSecretLines(headers)
    if (!environment || !headerValues) {
      setError(t('Use one non-empty NAME=value entry per line.'))
      return
    }
    setSaving(true)
    setError('')
    try {
      await api.post('/mcp/servers', { name, transport, command: transport === 'stdio' ? endpoint : '', args: parseArgs(args), url: transport === 'stdio' ? '' : endpoint, env: environment, headers: headerValues, enabled: true })
      saved()
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setSaving(false)
    }
  }
  return <Modal title={t('Add MCP server')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Name')}><input maxLength={100} value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('Transport')}><select value={transport} onChange={(event) => setTransport(event.target.value)}><option value="stdio">stdio</option><option value="sse">SSE</option><option value="streamable-http">Streamable HTTP</option></select></Field><Field label={transport === 'stdio' ? t('Command') : t('URL')}><input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} /></Field>{transport === 'stdio' && <Field label={t('Arguments')} hint={t('One argument per line.')}><textarea rows={3} value={args} onChange={(event) => setArgs(event.target.value)} /></Field>}<Field label={t('Environment secrets')} hint={t('One NAME=value per line. Values are encrypted before storage.')}><textarea className="secret-input" rows={4} value={env} onChange={(event) => setEnv(event.target.value)} /></Field><Field label={t('HTTP header secrets')} hint={t('One Header-Name=value per line. Values are encrypted before storage.')}><textarea className="secret-input" rows={4} value={headers} onChange={(event) => setHeaders(event.target.value)} /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={saving || !name.trim() || !endpoint.trim()}>{saving ? t('Saving...') : t('Create server')}</Button></div></Modal>
}

function ServerEditor({ server, close, saved }: { server: MCPServer; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState(server.name)
  const [enabled, setEnabled] = useState(server.enabled)
  const [transport, setTransport] = useState(server.transport)
  const [endpoint, setEndpoint] = useState(server.command || server.url)
  const [args, setArgs] = useState((server.args ?? []).join('\n'))
  const [envSet, setEnvSet] = useState('')
  const [headerSet, setHeaderSet] = useState('')
  const [envRemove, setEnvRemove] = useState<string[]>([])
  const [headerRemove, setHeaderRemove] = useState<string[]>([])
  const [confirmation, setConfirmation] = useState<SecretConfirmation | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const toggleRemoval = (kind: 'env' | 'header', key: string) => {
    setConfirmation(null)
    const selected = kind === 'env' ? envRemove : headerRemove
    const next = selected.includes(key) ? selected.filter((value) => value !== key) : [...selected, key]
    if (kind === 'env') setEnvRemove(next)
    else setHeaderRemove(next)
  }
  const submit = async (confirmTargets = false) => {
    const environment = parseSecretLines(envSet)
    const headers = parseSecretLines(headerSet)
    if (!environment || !headers) {
      setError(t('Use one non-empty NAME=value entry per line.'))
      return
    }
    if (envRemove.some((key) => key in environment) || headerRemove.some((key) => key in headers)) {
      setError(t('A secret key cannot be replaced and removed together.'))
      return
    }
    setSaving(true)
    setError('')
    try {
      await api.patch(`/mcp/servers/${server.id}`, {
        name,
        enabled,
        transport,
        command: transport === 'stdio' ? endpoint : '',
        args: transport === 'stdio' ? parseArgs(args) : [],
        url: transport === 'stdio' ? '' : endpoint,
        secretChanges: { env: { set: environment, remove: envRemove }, headers: { set: headers, remove: headerRemove } },
        confirmTargets,
      })
      saved()
    } catch (reason) {
      const required = secretConfirmationFromError(reason)
      if (!confirmTargets && required) setConfirmation(required)
      else setError((reason as Error).message)
    } finally {
      setSaving(false)
    }
  }
  return <Modal title={`${t('Edit MCP server')} · ${server.name}`} close={close}>
    {error && <ErrorNotice message={error} />}
    <Field label={t('Name')}><input maxLength={100} value={name} onChange={(event) => setName(event.target.value)} /></Field>
    <label className="toggle-control"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />{t('Enabled')}</label>
    <Field label={t('Transport')}><select value={transport} onChange={(event) => { setTransport(event.target.value); setConfirmation(null) }}><option value="stdio">stdio</option><option value="sse">SSE</option><option value="streamable-http">Streamable HTTP</option></select></Field>
    <Field label={transport === 'stdio' ? t('Command') : t('URL')}><input value={endpoint} onChange={(event) => { setEndpoint(event.target.value); setConfirmation(null) }} /></Field>
    {transport === 'stdio' && <Field label={t('Arguments')} hint={t('One argument per line.')}><textarea rows={4} value={args} onChange={(event) => { setArgs(event.target.value); setConfirmation(null) }} /></Field>}
    <SecretDeltaEditor title={t('Environment secrets')} keys={server.envKeys ?? []} remove={envRemove} toggleRemove={(key) => toggleRemoval('env', key)} values={envSet} setValues={(value) => { setEnvSet(value); setConfirmation(null) }} hint={t('Set new or replacement values as NAME=value. Existing values are never displayed.')} />
    <SecretDeltaEditor title={t('HTTP header secrets')} keys={server.headerKeys ?? []} remove={headerRemove} toggleRemove={(key) => toggleRemoval('header', key)} values={headerSet} setValues={(value) => { setHeaderSet(value); setConfirmation(null) }} hint={t('Set new or replacement values as Header-Name=value. Existing values are never displayed.')} />
    {confirmation && <SecretConfirmationPanel details={confirmation} />}
    <div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button disabled={saving || !name.trim() || !endpoint.trim()} onClick={() => submit(Boolean(confirmation))}>{saving ? t('Saving...') : confirmation ? t('Confirm affected targets') : t('Save changes')}</Button></div>
  </Modal>
}

function SecretDeltaEditor({ title, keys, remove, toggleRemove, values, setValues, hint }: { title: string; keys: string[]; remove: string[]; toggleRemove: (key: string) => void; values: string; setValues: (value: string) => void; hint: string }) {
  const { t } = useI18n()
  return <section className="secret-editor"><header><strong>{title}</strong><small>{t('{n} configured keys', { n: keys.length })}</small></header>{keys.length > 0 && <div className="check-list secret-key-list">{keys.map((key) => <label key={key}><input type="checkbox" checked={remove.includes(key)} onChange={() => toggleRemove(key)} /><span><code>{key}</code><small>{t('Remove this key')}</small></span></label>)}</div>}<Field label={t('Set / replace')} hint={hint}><textarea className="secret-input" rows={4} value={values} onChange={(event) => setValues(event.target.value)} /></Field></section>
}

function SecretConfirmationModal({ title, details, busy, close, confirm, confirmLabel }: { title: string; details: SecretConfirmation; busy: boolean; close: () => void; confirm: () => void; confirmLabel: string }) {
  const { t } = useI18n()
  return <Modal title={title} close={close}><SecretConfirmationPanel details={details} /><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button disabled={busy} onClick={confirm}>{busy ? t('Queuing...') : confirmLabel}</Button></div></Modal>
}

function SecretConfirmationPanel({ details }: { details: SecretConfirmation }) {
  const { t } = useI18n()
  const keys = [...details.envKeys.map((key) => `env · ${key}`), ...details.headerKeys.map((key) => `header · ${key}`)]
  return <div className="secret-confirmation"><header><KeyRound size={18} /><strong>{t('Secret-key confirmation required')}</strong></header><span>{t('Only the named values are captured or replaced; values remain write-only in the browser.')}</span>{keys.length > 0 && <ul>{keys.map((key) => <li key={key}><code>{key}</code></li>)}</ul>}{details.targets.length > 0 && <section><strong>{t('Affected targets')}</strong><ul>{details.targets.map((target) => <li key={`${target.nodeId}-${target.runtime}`}><span>{target.nodeName} · {target.runtime}</span></li>)}</ul></section>}</div>
}

function parseSecretLines(value: string): Record<string, string> | null {
  const result: Record<string, string> = {}
  for (const raw of value.split('\n')) {
    if (!raw.trim()) continue
    const separator = raw.indexOf('=')
    const key = separator > 0 ? raw.slice(0, separator).trim() : ''
    const secret = separator > 0 ? raw.slice(separator + 1) : ''
    if (!key || !secret || key in result) return null
    result[key] = secret
  }
  return result
}

function parseArgs(value: string): string[] {
  return value.split('\n').map((line) => line.trim()).filter(Boolean)
}

function secretConfirmationFromError(reason: unknown): SecretConfirmation | null {
  if (!(reason instanceof APIError) || reason.code !== 'secret_confirmation_required') return null
  return {
    envKeys: stringArray(reason.details.envKeys),
    headerKeys: stringArray(reason.details.headerKeys),
    targets: affectedTargets(reason.details.targets),
  }
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}

function affectedTargets(value: unknown): AffectedTarget[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item) => {
    if (!item || typeof item !== 'object') return []
    const target = item as Dict
    if (typeof target.nodeId !== 'string' || typeof target.nodeName !== 'string' || typeof target.runtime !== 'string') return []
    return [{ nodeId: target.nodeId, nodeName: target.nodeName, runtime: target.runtime }]
  })
}

function ProfileModal({ servers, close, saved }: { servers: MCPServer[]; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const submit = async () => {
    setSaving(true)
    setError('')
    try {
      await api.post('/mcp/profiles', { name, description, serverIds: selected })
      saved()
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setSaving(false)
    }
  }
  const editableServers = servers.filter((server) => server.authority !== 'shared-file' && !server.archivedAt)
  return <Modal title={t('Create MCP profile')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Name')}><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('Description')}><input value={description} onChange={(event) => setDescription(event.target.value)} /></Field><div className="check-list">{editableServers.map((server) => <label key={server.id}><input type="checkbox" checked={selected.includes(server.id)} onChange={() => setSelected((current) => current.includes(server.id) ? current.filter((id) => id !== server.id) : [...current, server.id])} /><span>{server.name}<small>{server.transport}</small></span></label>)}</div><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={saving || !name.trim()}>{saving ? t('Saving...') : t('Create profile')}</Button></div></Modal>
}
