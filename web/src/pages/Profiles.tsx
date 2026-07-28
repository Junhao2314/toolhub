import { Eye, Layers, Pencil, Play, Plus, RefreshCw, Search, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { api, APIError, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface ProfileSummary {
  id: string
  name: string
  description: string
  mcpServerCount: number
  skillCount: number
  activationCount: number
}

interface ProfileActivation {
  id: string
  nodeId: string
  nodeName: string
  runtime: string
  state: string
  lastError: string
}

interface ProfileDetail {
  id: string
  name: string
  description: string
  mcpServerIds: string[]
  skillIds: string[]
  activations: ProfileActivation[]
}

interface SkillOption {
  id: string
  name: string
  slug: string
  sourceKind?: string
  reviewStatus: string
}

interface MCPOption {
  id: string
  name: string
  source: string
  origin?: { importSourceName?: string }
  transport: string
  enabled: boolean
  authority: string
  archivedAt?: string
}

interface TargetNode {
  id: string
  name: string
  status: string
  isLocal: boolean
  runtimeKinds: string[]
}

interface ActivationIssue {
  code?: string
  scope?: string
  reason?: string
  detail?: string
}

interface ActivationPreflight {
  ok: boolean
  errors: ActivationIssue[]
  skipped: ActivationIssue[]
  remoteSecretKeys: string[]
  nodeIsLocal: boolean
  nodeName: string
}

interface MemberItem {
  id: string
  name: string
  detail: string
  provenance: string
  disabled?: boolean
}

export default function Profiles({ canOperate }: { canOperate: boolean }) {
  const { t } = useI18n()
  const state = useData(async () => {
    const [profiles, skills, servers, nodes] = await Promise.all([
      api.list<ProfileSummary>('/profiles'),
      api.list<SkillOption>('/skills'),
      api.list<MCPOption>('/mcp/servers'),
      api.list<TargetNode>('/nodes'),
    ])
    return { profiles: profiles.items, skills: skills.items, servers: servers.items, nodes: nodes.items }
  }, [])
  const [editing, setEditing] = useState<ProfileDetail | 'new' | null>(null)
  const [activating, setActivating] = useState<ProfileSummary | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const openProfile = async (profile: ProfileSummary) => {
    setBusy(profile.id)
    setError('')
    try {
      setEditing(await api.get<ProfileDetail>(`/profiles/${profile.id}`))
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setBusy('')
    }
  }
  const remove = async (profile: ProfileSummary) => {
    if (!confirm(t('Delete Profile {name}?', { name: profile.name }))) return
    setBusy(profile.id)
    setError('')
    try {
      await api.delete(`/profiles/${profile.id}`)
      setNotice(t('Profile deleted.'))
      state.reload()
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setBusy('')
    }
  }
  const completed = (message: string) => {
    setEditing(null)
    setActivating(null)
    setNotice(message)
    state.reload()
  }

  return <>
    <PageHeader
      title={t('Profiles')}
      detail={t('Reusable Skill and MCP selections activated per runtime target.')}
      actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button>{canOperate && <Button onClick={() => setEditing('new')}><Plus size={16} />{t('Add Profile')}</Button>}</>}
    />
    {error && <ErrorNotice message={error} />}
    {notice && <div className="inline-notice">{notice}</div>}
    {state.loading ? <Loading label={t('Loading Profiles')} /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : state.data.profiles.length === 0 ? <Empty title={t('No Profiles')} detail={t('Create a named selection of Skills and MCP servers for repeatable activation.')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Profile')}</th><th>{t('MCP servers')}</th><th>{t('Skills')}</th><th>{t('Active targets')}</th><th aria-label={t('Actions')} /></tr></thead><tbody>{state.data.profiles.map((profile) => <tr key={profile.id}><td><strong>{profile.name}</strong><small>{profile.description || t('No description')}</small></td><td>{profile.mcpServerCount}</td><td>{profile.skillCount}</td><td>{profile.activationCount}</td><td className="row-actions"><IconButton label={canOperate ? t('Edit Profile') : t('View Profile')} disabled={busy === profile.id} onClick={() => openProfile(profile)}>{canOperate ? <Pencil size={16} /> : <Eye size={16} />}</IconButton>{canOperate && <><IconButton label={t('Activate Profile')} onClick={() => setActivating(profile)}><Play size={17} /></IconButton><IconButton label={profile.activationCount ? t('Deactivate all targets before deleting this Profile') : t('Delete Profile')} disabled={profile.activationCount > 0 || busy === profile.id} onClick={() => remove(profile)}><Trash2 size={16} /></IconButton></>}</td></tr>)}</tbody></table></div>}
    {editing && state.data && <ProfileEditor profile={editing === 'new' ? null : editing} skills={state.data.skills} servers={state.data.servers} canOperate={canOperate} close={() => setEditing(null)} saved={() => completed(editing === 'new' ? t('Profile created.') : t('Profile updated.'))} />}
    {activating && state.data && <ActivationModal profile={activating} nodes={state.data.nodes} close={() => setActivating(null)} activated={() => completed(t('Profile activation queued.'))} />}
  </>
}

function ProfileEditor({ profile, skills, servers, canOperate, close, saved }: { profile: ProfileDetail | null; skills: SkillOption[]; servers: MCPOption[]; canOperate: boolean; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState(profile?.name ?? '')
  const [description, setDescription] = useState(profile?.description ?? '')
  const [mcpServerIds, setMCPServerIds] = useState(profile?.mcpServerIds ?? [])
  const [skillIds, setSkillIds] = useState(profile?.skillIds ?? [])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const membershipLocked = !canOperate || Boolean(profile?.activations.length)
  const mcpItems: MemberItem[] = servers.map((server) => ({
    id: server.id,
    name: server.name,
    detail: `${server.transport} · ${server.enabled ? t('enabled') : t('disabled')}${server.archivedAt ? ` · ${t('archived')}` : ''}`,
    provenance: [server.source, server.origin?.importSourceName].filter(Boolean).join(' · '),
    disabled: !server.enabled || server.authority !== 'toolhub' || Boolean(server.archivedAt),
  }))
  const skillItems: MemberItem[] = skills.map((skill) => ({
    id: skill.id,
    name: skill.name,
    detail: `${skill.slug} · ${t(skill.reviewStatus)}`,
    provenance: skill.sourceKind || 'upload',
  }))
  const submit = async () => {
    setSaving(true)
    setError('')
    try {
      let profileID = profile?.id
      if (profileID) {
        await api.patch(`/profiles/${profileID}`, { name, description })
      } else {
        const created = await api.post<{ id: string }>('/profiles', { name, description })
        profileID = created.id
      }
      if (!membershipLocked) await api.put(`/profiles/${profileID}/members`, { mcpServerIds, skillIds })
      saved()
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setSaving(false)
    }
  }
  return <Modal title={profile ? `${canOperate ? t('Edit Profile') : t('View Profile')} · ${profile.name}` : t('Add Profile')} close={close}>
    {error && <ErrorNotice message={error} />}
    <Field label={t('Name')}><input disabled={!canOperate} maxLength={100} value={name} onChange={(event) => setName(event.target.value)} /></Field>
    <Field label={t('Description')}><textarea disabled={!canOperate} maxLength={1000} rows={3} value={description} onChange={(event) => setDescription(event.target.value)} /></Field>
    {profile?.activations.length ? <div className="inline-notice"><Layers size={17} /><span>{t('Members are locked while this Profile is active on {n} target(s).', { n: profile.activations.length })}</span></div> : null}
    <div className="profile-member-grid">
      <MemberGroup title={t('MCP servers')} items={mcpItems} selected={mcpServerIds} setSelected={setMCPServerIds} locked={membershipLocked} />
      <MemberGroup title={t('Skills')} items={skillItems} selected={skillIds} setSelected={setSkillIds} locked={membershipLocked} />
    </div>
    <div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Close')}</Button>{canOperate && <Button disabled={saving || !name.trim()} onClick={submit}>{saving ? t('Saving...') : t('Save Profile')}</Button>}</div>
  </Modal>
}

function MemberGroup({ title, items, selected, setSelected, locked }: { title: string; items: MemberItem[]; selected: string[]; setSelected: (ids: string[]) => void; locked: boolean }) {
  const { t } = useI18n()
  const [query, setQuery] = useState('')
  const [provenance, setProvenance] = useState('all')
  const sources = [...new Set(items.map((item) => item.provenance))].sort()
  const visible = items.filter((item) => `${item.name} ${item.detail}`.toLowerCase().includes(query.toLowerCase()) && (provenance === 'all' || item.provenance === provenance))
  const toggle = (id: string) => setSelected(selected.includes(id) ? selected.filter((item) => item !== id) : [...selected, id])
  return <section className="profile-member-group">
    <header><span><strong>{title}</strong><small>{t('{selected} selected of {total}', { selected: selected.length, total: items.length })}</small></span><label className="member-search"><Search size={14} /><input aria-label={t('Search {group}', { group: title })} value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('Search')} /></label><select aria-label={t('{group} provenance', { group: title })} value={provenance} onChange={(event) => setProvenance(event.target.value)}><option value="all">{t('All sources')}</option>{sources.map((source) => <option key={source} value={source}>{source}</option>)}</select></header>
    <div className="check-list">{visible.length ? visible.map((item) => <label key={item.id}><input type="checkbox" checked={selected.includes(item.id)} disabled={locked || Boolean(item.disabled && !selected.includes(item.id))} onChange={() => toggle(item.id)} /><span><strong>{item.name}</strong><small>{item.detail}</small><small>{item.provenance}</small></span></label>) : <div className="member-empty">{t('No matching members')}</div>}</div>
  </section>
}

function ActivationModal({ profile, nodes, close, activated }: { profile: ProfileSummary; nodes: TargetNode[]; close: () => void; activated: () => void }) {
  const { t } = useI18n()
  const initialNode = nodes.find((node) => node.isLocal) ?? nodes[0]
  const [nodeID, setNodeID] = useState(initialNode?.id ?? '')
  const [runtime, setRuntime] = useState(initialNode?.runtimeKinds.filter((item) => item !== 'shared')[0] ?? '')
  const [result, setResult] = useState<ActivationPreflight | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirmingSecrets, setConfirmingSecrets] = useState(false)
  const selectedNode = nodes.find((node) => node.id === nodeID)
  const runtimes = selectedNode?.runtimeKinds.filter((item) => item !== 'shared') ?? []

  const changeNode = (id: string) => {
    const node = nodes.find((item) => item.id === id)
    setNodeID(id)
    setRuntime(node?.runtimeKinds.filter((item) => item !== 'shared')[0] ?? '')
    setResult(null)
    setError('')
  }
  const preflight = async () => {
    setBusy(true)
    setError('')
    setResult(null)
    try {
      setResult(await api.preflightProfile<ActivationPreflight>(profile.id, nodeID, runtime))
    } catch (reason) {
      if (reason instanceof APIError && reason.status === 409) {
        setResult(preflightFromError(reason))
      } else {
        setError((reason as Error).message)
      }
    } finally {
      setBusy(false)
    }
  }
  const activate = async (confirmSecrets: boolean) => {
    setBusy(true)
    setError('')
    try {
      await api.activateProfile(profile.id, nodeID, runtime, confirmSecrets)
      activated()
    } catch (reason) {
      if (reason instanceof APIError && reason.status === 409) {
        setConfirmingSecrets(false)
        setResult(preflightFromError(reason))
      } else {
        setError((reason as Error).message)
      }
    } finally {
      setBusy(false)
    }
  }
  const confirmable = Boolean(result?.remoteSecretKeys.length) && (result?.errors ?? []).every((issue) => issue.code === 'remote_secret_confirmation_required')

  if (confirmingSecrets && result) return <Modal title={t('Confirm remote secret delivery')} close={() => setConfirmingSecrets(false)}>
    {error && <ErrorNotice message={error} />}
    <div className="secret-confirmation"><strong>{t('Profile {profile} will deliver secret references to {node}.', { profile: profile.name, node: result.nodeName })}</strong><span>{t('Only these key names are shown; secret values remain encrypted.')}</span><ul>{result.remoteSecretKeys.map((key) => <li key={key}><code>{key}</code></li>)}</ul></div>
    <div className="modal-actions"><Button variant="secondary" onClick={() => setConfirmingSecrets(false)}>{t('Back')}</Button><Button disabled={busy} onClick={() => activate(true)}>{busy ? t('Queuing...') : t('Confirm and activate')}</Button></div>
  </Modal>

  return <Modal title={`${t('Activate Profile')} · ${profile.name}`} close={close}>
    {error && <ErrorNotice message={error} />}
    {!nodes.length ? <Empty title={t('No nodes enrolled')} detail={t('Enroll and scan a node before activating a Profile.')} /> : <div className="activation-target-fields"><Field label={t('Node')}><select value={nodeID} onChange={(event) => changeNode(event.target.value)}>{nodes.map((node) => <option key={node.id} value={node.id}>{node.name}{node.isLocal ? ` · ${t('Project host')}` : ''}</option>)}</select></Field><Field label={t('Runtime')}><select value={runtime} onChange={(event) => { setRuntime(event.target.value); setResult(null) }}>{runtimes.map((item) => <option key={item} value={item}>{item}</option>)}</select></Field></div>}
    {selectedNode && <div className="target-summary"><span><strong>{selectedNode.name}</strong><small>{runtime || t('No runtime available')}</small></span><Status value={selectedNode.status} /></div>}
    {result && <PreflightResult result={result} />}
    <div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button>{!result && <Button disabled={busy || !nodeID || !runtime} onClick={preflight}>{busy ? t('Checking...') : t('Run preflight')}</Button>}{result?.ok && <Button disabled={busy} onClick={() => activate(false)}>{busy ? t('Queuing...') : t('Activate')}</Button>}{!result?.ok && confirmable && <Button disabled={busy} onClick={() => setConfirmingSecrets(true)}>{t('Review secret keys')}</Button>}{result && !result.ok && !confirmable && <Button variant="secondary" disabled={busy} onClick={preflight}>{t('Run preflight again')}</Button>}</div>
  </Modal>
}

function PreflightResult({ result }: { result: ActivationPreflight }) {
  const { t } = useI18n()
  return <div className={`preflight-result ${result.ok ? 'passed' : 'blocked'}`}><header><strong>{result.ok ? t('Preflight passed') : t('Preflight blocked')}</strong><Status value={result.ok ? 'ready' : 'blocked'} /></header>{result.errors.length > 0 && <IssueList title={t('Blocking issues')} issues={result.errors} />}{result.skipped.length > 0 && <IssueList title={t('Skipped during activation')} issues={result.skipped} />}{result.remoteSecretKeys.length > 0 && <IssueList title={t('Remote secret key names')} issues={result.remoteSecretKeys.map((detail) => ({ detail }))} />}</div>
}

function IssueList({ title, issues }: { title: string; issues: ActivationIssue[] }) {
  return <section><strong>{title}</strong><ul>{issues.map((issue, index) => <li key={`${issue.code ?? issue.reason ?? 'issue'}-${index}`}><span>{issue.code || issue.reason || issue.scope}</span>{issue.detail && <small>{issue.detail}</small>}</li>)}</ul></section>
}

function preflightFromError(error: APIError): ActivationPreflight {
  const issues = Array.isArray(error.details.issues) ? error.details.issues as ActivationIssue[] : [{ code: error.code, detail: error.message }]
  const skipped = Array.isArray(error.details.skipped) ? error.details.skipped as ActivationIssue[] : []
  const secretKeys = Array.isArray(error.details.secretKeys) ? error.details.secretKeys.map(String) : []
  return { ok: false, errors: issues, skipped, remoteSecretKeys: secretKeys, nodeIsLocal: false, nodeName: stringDetail(error.details, 'nodeName') }
}

function stringDetail(details: Dict, key: string): string {
  return typeof details[key] === 'string' ? details[key] as string : ''
}
