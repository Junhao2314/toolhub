import { Clipboard, KeyRound, Network, Plus, RefreshCw, ScanSearch, Server, WifiOff } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'

interface NodeRow { id: string; name: string; hostname: string; platform: string; architecture: string; tailscaleIp?: string; status: string; labels: Record<string, string>; connectionPreference: string; runtimeCount: number; lastSeenAt?: string }

export default function Nodes() {
  const state = useData(() => api.list<NodeRow>('/nodes'), [])
  const [add, setAdd] = useState(false)
  const [error, setError] = useState('')
  const scan = (id: string) => api.post(`/nodes/${id}/scan`).then(state.reload).catch((reason: Error) => setError(reason.message))
  return <>
    <PageHeader title="Nodes" detail="Agent-first Tailnet connectivity, inventory, and pinned SSH fallback." actions={<><Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />Refresh</Button><Button onClick={() => setAdd(true)}><Plus size={16} />Enroll node</Button></>} />
    {error && <ErrorNotice message={error} />}
    {state.loading ? <Loading label="Loading nodes" /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : !state.data?.items.length ? <Empty title="No nodes enrolled" detail="Create a short-lived enrollment token for the first node." action={<Button onClick={() => setAdd(true)}>Enroll node</Button>} /> : <div className="node-list">{state.data.items.map((node) => <article key={node.id} className="node-row"><div className={`node-avatar ${node.status}`}><Server size={20} /></div><div className="node-primary"><strong>{node.name}</strong><span>{node.hostname || 'Awaiting inventory'} · {node.platform}/{node.architecture}</span><div className="label-row">{Object.entries(node.labels ?? {}).map(([key, value]) => <small key={key}>{key}:{value}</small>)}</div></div><dl><div><dt>Tailnet</dt><dd>{node.tailscaleIp ?? 'Unknown'}</dd></div><div><dt>Runtimes</dt><dd>{node.runtimeCount}</dd></div><div><dt>Connection</dt><dd>{node.connectionPreference}</dd></div></dl><Status value={node.status} /><div className="row-actions"><IconButton label="Read-only inventory scan" onClick={() => scan(node.id)}><ScanSearch size={17} /></IconButton></div></article>)}</div>}
    {add && <Enrollment close={() => setAdd(false)} complete={() => { setAdd(false); state.reload() }} />}
  </>
}

function Enrollment({ close, complete }: { close: () => void; complete: () => void }) {
  const [name, setName] = useState('')
  const [labels, setLabels] = useState('env=test')
  const [result, setResult] = useState<{ token: string; expiresAt: string; agentCommand: string } | null>(null)
  const [error, setError] = useState('')
  const submit = () => {
    const labelMap = Object.fromEntries(labels.split(',').map((item) => item.trim().split('=')).filter((pair) => pair.length === 2))
    api.post<{ token: string; expiresAt: string; agentCommand: string }>('/nodes', { name, labels: labelMap }).then(setResult).catch((reason: Error) => setError(reason.message))
  }
  return <Modal title="Enroll node" close={close}>{error && <ErrorNotice message={error} />}{result ? <div className="enrollment-result"><KeyRound size={22} /><strong>One-time token created</strong><p>Expires {new Date(result.expiresAt).toLocaleString()}.</p><code>{result.token}</code><Button variant="secondary" onClick={() => navigator.clipboard.writeText(result.token)}><Clipboard size={15} />Copy token</Button><div className="inline-notice"><WifiOff size={16} />The node remains pending until the Agent completes enrollment.</div></div> : <><Field label="Node name"><input value={name} onChange={(event) => setName(event.target.value)} placeholder="build-node-01" /></Field><Field label="Labels" hint="Comma-separated key=value pairs."><input value={labels} onChange={(event) => setLabels(event.target.value)} /></Field></>}<div className="modal-actions"><Button variant="secondary" onClick={result ? complete : close}>{result ? 'Done' : 'Cancel'}</Button>{!result && <Button onClick={submit} disabled={!name}><Network size={15} />Create token</Button>}</div></Modal>
}
