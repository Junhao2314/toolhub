import { Clipboard, KeyRound, Network, Plus, RefreshCw, ScanSearch, Server, ShieldCheck, Terminal, WifiOff } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'

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
  isLocal: boolean
  hasSsh: boolean
}

export default function Nodes() {
  const state = useData(async () => {
    const nodes = await api.list<NodeRow>('/nodes')
    return { nodes: nodes.items, localNodeName: nodes.items.find((node) => node.isLocal)?.name ?? 'project-host' }
  }, [])
  const [add, setAdd] = useState(false)
  const [sshNode, setSSHNode] = useState<NodeRow | null>(null)
  const [error, setError] = useState('')
  const scan = (id: string) => api.post(`/nodes/${id}/scan`).then(state.reload).catch((reason: Error) => setError(reason.message))
  const localNode = state.data?.nodes.find((node) => node.isLocal)
  return <>
    <PageHeader title="Nodes" detail="Agent-first Tailnet connectivity, inventory, and pinned SSH fallback." actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />Refresh</Button><Button onClick={() => setAdd(true)}><Plus size={16} />{localNode?.status === 'pending' ? 'Enroll project host' : 'Enroll node'}</Button></>} />
    {error && <ErrorNotice message={error} />}
    {state.loading ? <Loading label="Loading nodes" /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : !state.data?.nodes.length ? <Empty title="No nodes enrolled" detail="Create a short-lived enrollment token for the first node." action={<Button onClick={() => setAdd(true)}>Enroll node</Button>} /> : <div className="node-list">{state.data.nodes.map((node) => <article key={node.id} className="node-row"><div className={`node-avatar ${node.status}`}><Server size={20} /></div><div className="node-primary"><div className="node-title"><strong>{node.name}</strong>{node.isLocal && <span className="local-badge"><ShieldCheck size={12} />Project host</span>}</div><span>{node.hostname || 'Awaiting Agent inventory'} · {node.platform}/{node.architecture}</span><div className="label-row">{Object.entries(node.labels ?? {}).map(([key, value]) => <small key={key}>{key}:{value}</small>)}</div></div><dl><div><dt>Tailnet</dt><dd>{node.tailscaleIp ?? 'Unknown'}</dd></div><div><dt>Runtimes</dt><dd>{node.runtimeKinds.length ? node.runtimeKinds.join(', ') : 'Not scanned'}</dd></div><div><dt>Fallback</dt><dd>{node.hasSsh ? 'Pinned SSH ready' : 'Not configured'}</dd></div></dl><Status value={node.status} /><div className="row-actions"><IconButton label="Configure pinned SSH fallback" onClick={() => setSSHNode(node)}><KeyRound size={17} /></IconButton><IconButton label="Read-only inventory scan" onClick={() => scan(node.id)}><ScanSearch size={17} /></IconButton></div></article>)}</div>}
    {add && <Enrollment localNode={localNode} localNodeName={state.data?.localNodeName ?? 'project-host'} close={() => setAdd(false)} complete={() => { setAdd(false); state.reload() }} />}
    {sshNode && <SSHConnection node={sshNode} close={() => setSSHNode(null)} saved={() => { setSSHNode(null); state.reload() }} />}
  </>
}

function Enrollment({ localNode, localNodeName, close, complete }: { localNode?: NodeRow; localNodeName: string; close: () => void; complete: () => void }) {
  const claimingLocal = localNode?.status === 'pending'
  const [name, setName] = useState(claimingLocal ? localNode.name : '')
  const [labels, setLabels] = useState(claimingLocal ? 'scope=local,group=canary' : 'env=test')
  const [result, setResult] = useState<{ token: string; expiresAt: string; agentCommand: string } | null>(null)
  const [error, setError] = useState('')
  const submit = () => {
    const labelMap = Object.fromEntries(labels.split(',').map((item) => item.trim().split('=')).filter((pair) => pair.length === 2))
    api.post<{ token: string; expiresAt: string; agentCommand: string }>('/nodes', { name, labels: labelMap }).then(setResult).catch((reason: Error) => setError(reason.message))
  }
  return <Modal title={claimingLocal ? `Enroll project host · ${localNodeName}` : 'Enroll node'} close={close}>{error && <ErrorNotice message={error} />}{result ? <div className="enrollment-result"><Terminal size={22} /><strong>Run on the target machine</strong><p>Expires {new Date(result.expiresAt).toLocaleString()}.</p><div className="command-block"><code>{result.agentCommand}</code><IconButton label="Copy enrollment command" onClick={() => navigator.clipboard.writeText(result.agentCommand)}><Clipboard size={15} /></IconButton></div><details><summary>Show one-time token</summary><code>{result.token}</code></details><div className="inline-notice"><WifiOff size={16} />The node remains pending until the Agent completes enrollment.</div></div> : <><Field label="Node name"><input value={name} onChange={(event) => setName(event.target.value)} placeholder="build-node-01" /></Field><Field label="Labels" hint="Comma-separated key=value pairs."><input value={labels} onChange={(event) => setLabels(event.target.value)} /></Field>{claimingLocal && <div className="inline-notice"><Network size={16} />This token claims the default project-host node and makes its discovered runtimes the canary defaults.</div>}</>}<div className="modal-actions"><Button variant="secondary" onClick={result ? complete : close}>{result ? 'Done' : 'Cancel'}</Button>{!result && <Button onClick={submit} disabled={!name}><Network size={15} />Create token</Button>}</div></Modal>
}

function SSHConnection({ node, close, saved }: { node: NodeRow; close: () => void; saved: () => void }) {
  const [address, setAddress] = useState('')
  const [knownHosts, setKnownHosts] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const submit = () => {
    setSaving(true); setError('')
    api.post(`/nodes/${node.id}/connections`, { kind: 'ssh', address, knownHosts, privateKey }).then(saved).catch((reason: Error) => setError(reason.message)).finally(() => setSaving(false))
  }
  return <Modal title={`SSH fallback · ${node.name}`} close={close}>{error && <ErrorNotice message={error} />}<div className="ssh-note"><ShieldCheck size={18} /><span><strong>Fixed-task fallback only</strong><small>ToolHub pins this host key, uploads a signed task with SFTP, and invokes only the Agent task runner.</small></span></div><Field label="Address" hint="Use user@host without SSH options."><input value={address} onChange={(event) => setAddress(event.target.value)} placeholder="ops@100.100.10.20" /></Field><Field label="Pinned known_hosts line" hint="Paste one complete host key line, not only its fingerprint."><textarea rows={3} value={knownHosts} onChange={(event) => setKnownHosts(event.target.value)} placeholder="100.100.10.20 ssh-ed25519 AAAA..." /></Field><Field label="Private key" hint="Encrypted with TOOLHUB_MASTER_KEY before PostgreSQL storage."><textarea className="secret-input" rows={7} value={privateKey} onChange={(event) => setPrivateKey(event.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={submit} disabled={saving || !address || !knownHosts || !privateKey}><KeyRound size={15} />{saving ? 'Encrypting...' : 'Save fallback'}</Button></div></Modal>
}
