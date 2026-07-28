import { ArrowLeftRight, MonitorCog, RefreshCw, RotateCw, ShieldOff } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, APIError, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface TargetNode {
  id: string
  name: string
  status: string
  isLocal: boolean
  runtimeKinds: string[]
}

interface ActivationIssue {
  scope?: string
  reason?: string
  detail?: string
}

interface TargetActivation {
  profileId: string
  profileName: string
  previousProfileId: string
  previousProfileName: string
  state: string
  lastError: string
  skipped: ActivationIssue[]
  activatedAt: string
  activatedBy: string
}

interface TargetMCPServer {
  id: string
  name: string
  runtimeName: string
  transport: string
  endpoint: string
  enabled: boolean
  source: string
  originName: string
  missing: boolean
  drift: boolean
}

interface TargetSkill {
  deploymentId: string
  skillId: string
  name: string
  slug: string
  desiredEnabled: boolean
  actualEnabled: boolean
  state: string
  desiredVersionId: string
  actualVersionId: string
  sha256: string
  lastError: string
}

interface TargetView {
  node: { id: string; name: string; status: string; isLocal: boolean }
  runtime: string
  capabilities: { skills: boolean; mcp: boolean; mcpNote: string }
  activation: TargetActivation | null
  mcp: { mcpmProfile: string; deploymentId: string; state: string; servers: TargetMCPServer[] }
  skills: TargetSkill[]
  drift: { mcp: number; skills: number }
}

interface SecretConfirmation {
  profileID: string
  profileName: string
  nodeName: string
  keys: string[]
}

export default function Targets({ canOperate }: { canOperate: boolean }) {
  const { t } = useI18n()
  const nodes = useData(() => api.list<TargetNode>('/nodes'), [])
  const [nodeID, setNodeID] = useState('')
  const [runtime, setRuntime] = useState('')
  useEffect(() => {
    if (!nodes.data?.items.length || nodeID) return
    const initial = nodes.data.items.find((node) => node.isLocal) ?? nodes.data.items[0]
    setNodeID(initial.id)
    setRuntime(initial.runtimeKinds.filter((item) => item !== 'shared')[0] ?? '')
  }, [nodes.data, nodeID])
  const selectedNode = nodes.data?.items.find((node) => node.id === nodeID)
  const runtimes = selectedNode?.runtimeKinds.filter((item) => item !== 'shared') ?? []
  const changeNode = (id: string) => {
    const node = nodes.data?.items.find((item) => item.id === id)
    setNodeID(id)
    setRuntime(node?.runtimeKinds.filter((item) => item !== 'shared')[0] ?? '')
  }
  return <>
    <PageHeader title={t('Runtime View')} detail={t('Effective Profile, MCP, Skills, and drift for one runtime target.')} actions={<Button variant="secondary" onClick={nodes.reload}><RefreshCw size={16} />{t('Refresh nodes')}</Button>} />
    {nodes.loading ? <Loading label={t('Loading runtime targets')} /> : nodes.error || !nodes.data ? <ErrorNotice message={nodes.error} retry={nodes.reload} /> : !nodes.data.items.length ? <Empty title={t('No nodes enrolled')} detail={t('Enroll and scan a node to inspect its runtime state.')} /> : <><div className="runtime-selector"><label><span>{t('Node')}</span><select aria-label={t('Node')} value={nodeID} onChange={(event) => changeNode(event.target.value)}>{nodes.data.items.map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}</select></label><label><span>{t('Runtime')}</span><select aria-label={t('Runtime')} value={runtime} onChange={(event) => setRuntime(event.target.value)}>{runtimes.map((item) => <option key={item} value={item}>{item}</option>)}</select></label>{selectedNode && <span className="runtime-selector-state"><Status value={selectedNode.status} />{selectedNode.isLocal && <small>{t('Project host')}</small>}</span>}</div>{runtime ? <TargetDetail key={`${nodeID}:${runtime}`} nodeID={nodeID} runtime={runtime} canOperate={canOperate} /> : <Empty title={t('No runtime inventory')} detail={t('Run an inventory scan before opening Runtime View.')} />}</>}
  </>
}

function TargetDetail({ nodeID, runtime, canOperate }: { nodeID: string; runtime: string; canOperate: boolean }) {
  const { t } = useI18n()
  const state = useData(() => api.targetView<TargetView>(nodeID, runtime), [nodeID, runtime])
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [confirmation, setConfirmation] = useState<SecretConfirmation | null>(null)

  const queueProfile = async (profileID: string, profileName: string, confirmSecrets = false) => {
    setBusy(profileID)
    setError('')
    try {
      await api.activateProfile(profileID, nodeID, runtime, confirmSecrets)
      setConfirmation(null)
      setNotice(t('Profile activation queued.'))
      state.reload()
    } catch (reason) {
      if (reason instanceof APIError && reason.code === 'remote_secret_confirmation_required') {
        setConfirmation({ profileID, profileName, nodeName: stringDetail(reason.details, 'nodeName'), keys: arrayDetail(reason.details, 'secretKeys') })
      } else {
        setError((reason as Error).message)
      }
    } finally {
      setBusy('')
    }
  }
  const deactivate = async () => {
    if (!confirm(t('Deactivate this Profile target?'))) return
    setBusy('deactivate')
    setError('')
    try {
      await api.deactivateTarget(nodeID, runtime)
      setNotice(t('Profile deactivated. Manual target controls are available again.'))
      state.reload()
    } catch (reason) {
      setError((reason as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (state.loading) return <Loading label={t('Loading Runtime View')} />
  if (state.error || !state.data) return <ErrorNotice message={state.error} retry={state.reload} />
  const target = state.data
  const enabledSkills = target.skills.filter((skill) => skill.desiredEnabled)
  return <div className="runtime-view">
    {error && <ErrorNotice message={error} />}
    {notice && <div className="inline-notice">{notice}</div>}
    <section className="runtime-band activation-band"><header><div><MonitorCog size={19} /><span><h2>{t('Active Profile')}</h2><p>{t('Desired state ownership for this node and runtime.')}</p></span></div><span className="drift-summary"><Status value={target.activation?.state ?? 'manual'} /><IconButton label={t('Refresh Runtime View')} onClick={state.reload}><RefreshCw size={16} /></IconButton></span></header>{target.activation ? <div className="activation-detail"><dl><div><dt>{t('Profile')}</dt><dd>{target.activation.profileName}</dd></div><div><dt>{t('Activated by')}</dt><dd>{target.activation.activatedBy || t('system')}</dd></div><div><dt>{t('Activated at')}</dt><dd>{new Date(target.activation.activatedAt).toLocaleString()}</dd></div><div><dt>{t('Previous Profile')}</dt><dd>{target.activation.previousProfileName || '—'}</dd></div></dl>{target.activation.skipped?.length > 0 && <div className="runtime-note"><strong>{t('Skipped during activation')}</strong>{target.activation.skipped.map((issue, index) => <span key={`${issue.reason}-${index}`}>{issue.reason}<small>{issue.detail}</small></span>)}</div>}{target.activation.lastError && <ErrorNotice message={target.activation.lastError} />}{canOperate && <div className="runtime-actions"><Button variant="secondary" disabled={busy !== ''} onClick={() => queueProfile(target.activation!.profileId, target.activation!.profileName)}><RotateCw size={16} />{t('Retry activation')}</Button>{target.activation.previousProfileId && <Button variant="secondary" disabled={busy !== ''} onClick={() => queueProfile(target.activation!.previousProfileId, target.activation!.previousProfileName)}><ArrowLeftRight size={16} />{t('Return to {profile}', { profile: target.activation.previousProfileName })}</Button>}<Button variant="danger" disabled={busy !== ''} onClick={deactivate}><ShieldOff size={16} />{t('Deactivate Profile')}</Button></div>}</div> : <div className="manual-control"><strong>{t('Manual target control')}</strong><span>{t('No Profile owns this target. Skills and MCP matrices can be edited directly.')}</span></div>}</section>
    <section className="runtime-band"><header><div><span><h2>{t('Effective MCP servers')}</h2><p>{target.mcp.mcpmProfile || t('Observed runtime configuration')}</p></span></div><span className="drift-summary"><strong>{target.mcp.servers.length}</strong><small>{t('servers')}</small><Status value={target.drift.mcp ? 'drift' : target.mcp.state} /></span></header>{!target.capabilities.mcp && target.capabilities.mcpNote && <div className="inline-notice">{target.capabilities.mcpNote}</div>}{target.mcp.servers.length ? <div className="table-scroll"><table><thead><tr><th>{t('Server')}</th><th>{t('Transport')}</th><th>{t('Endpoint')}</th><th>{t('Source')}</th><th>{t('State')}</th></tr></thead><tbody>{target.mcp.servers.map((server) => <tr key={server.id}><td><strong>{server.name}</strong><small>{server.runtimeName}</small></td><td>{server.transport}</td><td><code>{server.endpoint}</code></td><td>{server.originName || server.source}</td><td><Status value={server.missing ? 'missing' : server.drift ? 'drift' : 'in sync'} /></td></tr>)}</tbody></table></div> : <Empty title={t('No effective MCP servers')} detail={t('This target currently has an empty MCP selection.')} />}</section>
    <section className="runtime-band"><header><div><span><h2>{t('Effective Skills')}</h2><p>{t('Enabled desired deployments for this runtime.')}</p></span></div><span className="drift-summary"><strong>{enabledSkills.length}</strong><small>{t('Skills')}</small><Status value={target.drift.skills ? 'drift' : 'in sync'} /></span></header>{enabledSkills.length ? <div className="table-scroll"><table><thead><tr><th>{t('Skill')}</th><th>{t('Hash')}</th><th>{t('Desired')}</th><th>{t('Actual')}</th><th>{t('State')}</th><th>{t('Last error')}</th></tr></thead><tbody>{enabledSkills.map((skill) => { const drift = skill.desiredEnabled !== skill.actualEnabled || skill.desiredVersionId !== skill.actualVersionId; return <tr key={skill.deploymentId}><td><strong>{skill.name}</strong><small>{skill.slug}</small></td><td><code>{skill.sha256?.slice(0, 12) || '—'}</code></td><td>{skill.desiredEnabled ? t('Enabled') : t('Disabled')}</td><td>{skill.actualEnabled ? t('Enabled') : t('Disabled')}</td><td><Status value={drift ? 'drift' : skill.state} /></td><td>{skill.lastError || '—'}</td></tr> })}</tbody></table></div> : <Empty title={t('No effective Skills')} detail={t('This target currently has an empty Skill selection.')} />}</section>
    {confirmation && <Modal title={t('Confirm remote secret delivery')} close={() => setConfirmation(null)}><div className="secret-confirmation"><strong>{t('Profile {profile} will deliver secret references to {node}.', { profile: confirmation.profileName, node: confirmation.nodeName })}</strong><span>{t('Only these key names are shown; secret values remain encrypted.')}</span><ul>{confirmation.keys.map((key) => <li key={key}><code>{key}</code></li>)}</ul></div><div className="modal-actions"><Button variant="secondary" onClick={() => setConfirmation(null)}>{t('Cancel')}</Button><Button disabled={busy !== ''} onClick={() => queueProfile(confirmation.profileID, confirmation.profileName, true)}>{t('Confirm and activate')}</Button></div></Modal>}
  </div>
}

function stringDetail(details: Dict, key: string): string {
  return typeof details[key] === 'string' ? details[key] as string : ''
}

function arrayDetail(details: Dict, key: string): string[] {
  return Array.isArray(details[key]) ? (details[key] as unknown[]).map(String) : []
}
