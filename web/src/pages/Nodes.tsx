import { Clipboard, FileJson, KeyRound, Network, Plus, RefreshCw, ScanSearch, Server, ShieldCheck, Terminal, TestTube2, WifiOff } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface NodeRow {
  id: string
  name: string
  hostname: string
  platform: string
  architecture: string
  tailscaleIp?: string
  status: string
  labels: Record<string, string>
  connectionPreference: string
  runtimeCount: number
  runtimeKinds: string[]
  lastSeenAt?: string
  scannedAt?: string
  autoManagedMcpCount: number
  pendingSkillCount: number
  discoveryAttentionCount: number
  isLocal: boolean
  hasSsh: boolean
  sharedSourceCount: number
  hasManagedSharedSource: boolean
}

interface SharedSourceRow {
  id: string
  nodeId: string
  name: string
  mode: string
  autoSync: boolean
  skillsRoot: string
  mcpManifestPath: string
  status: string
  lastScanAt?: string
  lastSyncAt?: string
  lastError?: string
}

export default function Nodes({ canSync }: { canSync: boolean }) {
  const { t } = useI18n()
  const state = useData(async () => {
    const [nodes, sharedSources] = await Promise.all([api.list<NodeRow>('/nodes'), api.list<SharedSourceRow>('/shared-sources')])
    return { nodes: nodes.items, sharedSources: sharedSources.items, localNodeName: nodes.items.find((node) => node.isLocal)?.name ?? 'project-host' }
  }, [])
  const [add, setAdd] = useState(false)
  const [sshNode, setSSHNode] = useState<NodeRow | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const scan = (id: string) => api.post(`/nodes/${id}/scan`).then(state.reload).catch((reason: Error) => setError(reason.message))
  const syncShared = (source: SharedSourceRow, dryRun: boolean) => {
    setBusy(`${source.id}:${dryRun ? 'dry' : 'sync'}`); setError('')
    api.post(`/shared-sources/${source.id}/sync`, { scopes: ['skills', 'mcp'], dryRun }).then(state.reload).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(''))
  }
  const localNode = state.data?.nodes.find((node) => node.isLocal)
  return <>
    <PageHeader title={t('Nodes')} detail={t('Agent-first Tailnet connectivity, inventory, and pinned SSH fallback.')} actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button><Button onClick={() => setAdd(true)}><Plus size={16} />{localNode?.status === 'pending' ? t('Enroll project host') : t('Enroll node')}</Button></>} />
    {error && <ErrorNotice message={error} />}
    {state.loading ? <Loading label={t('Loading nodes')} /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : !state.data?.nodes.length ? <Empty title={t('No nodes enrolled')} detail={t('Create a short-lived enrollment token for the first node.')} action={<Button onClick={() => setAdd(true)}>{t('Enroll node')}</Button>} /> : <div className="node-list">{state.data.nodes.map((node) => <NodeCard key={node.id} node={node} sources={state.data?.sharedSources.filter((source) => source.nodeId === node.id) ?? []} canSync={canSync} busy={busy} scan={scan} syncShared={syncShared} configureSSH={() => setSSHNode(node)} />)}</div>}
    {add && <Enrollment localNode={localNode} localNodeName={state.data?.localNodeName ?? 'project-host'} close={() => setAdd(false)} complete={() => { setAdd(false); state.reload() }} />}
    {sshNode && <SSHConnection node={sshNode} close={() => setSSHNode(null)} saved={() => { setSSHNode(null); state.reload() }} />}
  </>
}

function NodeCard({ node, sources, canSync, busy, scan, syncShared, configureSSH }: { node: NodeRow; sources: SharedSourceRow[]; canSync: boolean; busy: string; scan: (id: string) => void; syncShared: (source: SharedSourceRow, dryRun: boolean) => void; configureSSH: () => void }) {
  const { t } = useI18n()
  return <article className="node-row"><div className={`node-avatar ${node.status}`}><Server size={20} /></div><div className="node-primary"><div className="node-title"><strong>{node.name}</strong>{node.isLocal && <span className="local-badge"><ShieldCheck size={12} />{t('Project host')}</span>}</div><span>{node.hostname || t('Awaiting Agent inventory')} · {node.platform}/{node.architecture}</span><small>{t('Last scan: {time}', { time: node.scannedAt ? new Date(node.scannedAt).toLocaleString() : t('Never') })}</small><div className="label-row">{Object.entries(node.labels ?? {}).map(([key, value]) => <small key={key}>{key}:{value}</small>)}</div>{sources.map((source) => <div className="shared-source-summary" key={source.id}><FileJson size={15} /><span><strong>{source.name}</strong><small>{source.skillsRoot}</small><small>{source.mcpManifestPath}</small>{source.lastError && <small className="warning-text">{source.lastError}</small>}</span><span className="shared-source-meta"><Status value={source.status} /><small>{source.mode} · {source.autoSync ? t('auto-sync on') : t('auto-sync off')}</small>{source.lastSyncAt && <small>{t('Last sync: {time}', { time: new Date(source.lastSyncAt).toLocaleString() })}</small>}</span>{canSync && <div className="row-actions"><IconButton label={t('Dry run shared sync')} disabled={busy !== ''} onClick={() => syncShared(source, true)}><TestTube2 size={16} /></IconButton><IconButton label={t('Sync shared source')} disabled={busy !== '' || source.mode !== 'managed'} onClick={() => syncShared(source, false)}><RefreshCw size={16} /></IconButton></div>}</div>)}</div><dl><div><dt>{t('Runtimes')}</dt><dd>{node.runtimeKinds.length ? node.runtimeKinds.join(', ') : t('Not scanned')}</dd></div><div><dt>{t('Shared sources')}</dt><dd>{node.sharedSourceCount ?? 0}</dd></div><div><dt>{t('Skills to adopt')}</dt><dd>{node.pendingSkillCount ?? 0}</dd></div><div><dt>{t('Drift / missing')}</dt><dd>{node.discoveryAttentionCount ?? 0}</dd></div></dl><Status value={node.discoveryAttentionCount ? 'drift' : node.status} /><div className="row-actions"><IconButton label={t('Configure pinned SSH fallback')} onClick={configureSSH}><KeyRound size={17} /></IconButton><IconButton label={t('Read-only inventory scan')} onClick={() => scan(node.id)}><ScanSearch size={17} /></IconButton></div></article>
}

function Enrollment({ localNode, localNodeName, close, complete }: { localNode?: NodeRow; localNodeName: string; close: () => void; complete: () => void }) {
  const { t } = useI18n()
  const claimingLocal = localNode?.status === 'pending'
  const [name, setName] = useState(claimingLocal ? localNode.name : '')
  const [labels, setLabels] = useState(claimingLocal ? 'scope=local,group=canary' : 'env=test')
  const [result, setResult] = useState<{ token: string; expiresAt: string; agentCommand: string } | null>(null)
  const [error, setError] = useState('')
  const submit = () => {
    const labelMap = Object.fromEntries(labels.split(',').map((item) => item.trim().split('=')).filter((pair) => pair.length === 2))
    api.post<{ token: string; expiresAt: string; agentCommand: string }>('/nodes', { name, labels: labelMap }).then(setResult).catch((reason: Error) => setError(reason.message))
  }
  return <Modal title={claimingLocal ? `${t('Enroll project host')} · ${localNodeName}` : t('Enroll node')} close={close}>{error && <ErrorNotice message={error} />}{result ? <div className="enrollment-result"><Terminal size={22} /><strong>{t('Run on the target machine')}</strong><p>{t('Expires {time}.', { time: new Date(result.expiresAt).toLocaleString() })}</p><div className="command-block"><code>{result.agentCommand}</code><IconButton label={t('Copy enrollment command')} onClick={() => navigator.clipboard.writeText(result.agentCommand)}><Clipboard size={15} /></IconButton></div><details><summary>{t('Show one-time token')}</summary><code>{result.token}</code></details><div className="inline-notice"><WifiOff size={16} />{t('The node remains pending until the Agent completes enrollment.')}</div></div> : <><Field label={t('Node name')}><input value={name} onChange={(event) => setName(event.target.value)} placeholder="build-node-01" /></Field><Field label={t('Labels')} hint={t('Comma-separated key=value pairs.')}><input value={labels} onChange={(event) => setLabels(event.target.value)} /></Field>{claimingLocal && <div className="inline-notice"><Network size={16} />{t('This token claims the default project-host node and makes its discovered runtimes the canary defaults.')}</div>}</>}<div className="modal-actions"><Button variant="secondary" onClick={result ? complete : close}>{result ? t('Done') : t('Cancel')}</Button>{!result && <Button onClick={submit} disabled={!name}><Network size={15} />{t('Create token')}</Button>}</div></Modal>
}

function SSHConnection({ node, close, saved }: { node: NodeRow; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [address, setAddress] = useState('')
  const [knownHosts, setKnownHosts] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const submit = () => {
    setSaving(true); setError('')
    api.post(`/nodes/${node.id}/connections`, { kind: 'ssh', address, knownHosts, privateKey }).then(saved).catch((reason: Error) => setError(reason.message)).finally(() => setSaving(false))
  }
  return <Modal title={`${t('SSH fallback')} · ${node.name}`} close={close}>{error && <ErrorNotice message={error} />}<div className="ssh-note"><ShieldCheck size={18} /><span><strong>{t('Fixed-task fallback only')}</strong><small>{t('ToolHub pins this host key, uploads a signed task with SFTP, and invokes only the Agent task runner.')}</small></span></div><Field label={t('Address')} hint={t('Use user@host without SSH options.')}><input value={address} onChange={(event) => setAddress(event.target.value)} placeholder="ops@100.100.10.20" /></Field><Field label={t('Pinned known_hosts line')} hint={t('Paste one complete host key line, not only its fingerprint.')}><textarea rows={3} value={knownHosts} onChange={(event) => setKnownHosts(event.target.value)} placeholder="100.100.10.20 ssh-ed25519 AAAA..." /></Field><Field label={t('Private key')} hint={t('Encrypted with TOOLHUB_MASTER_KEY before PostgreSQL storage.')}><textarea className="secret-input" rows={7} value={privateKey} onChange={(event) => setPrivateKey(event.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={saving || !address || !knownHosts || !privateKey}><KeyRound size={15} />{saving ? t('Encrypting...') : t('Save fallback')}</Button></div></Modal>
}
