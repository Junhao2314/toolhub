import { AlertTriangle, ArrowRight, Boxes, Network, RefreshCw, Workflow } from 'lucide-react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Loading, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'

interface OverviewData extends Dict {
  nodes: number
  onlineNodes: number
  skills: number
  needsAttention: number
  activeJobs: number
  availableUpdates: number
}

interface NodeRow { id: string; name: string; platform: string; status: string; runtimeCount: number; lastSeenAt?: string }
interface Deployment { id: string; skillName: string; nodeName: string; runtime: string; state: string; lastError: string }
interface Job { id: string; kind: string; status: string; createdAt: string }

export default function Overview({ navigate }: { navigate: (path: string) => void }) {
  const state = useData(async () => {
    const [overview, nodes, deployments, jobs] = await Promise.all([
      api.get<OverviewData>('/overview'), api.list<NodeRow>('/nodes'), api.list<Deployment>('/deployments'), api.list<Job>('/jobs'),
    ])
    return { overview, nodes: nodes.items, deployments: deployments.items, jobs: jobs.items }
  }, [])
  if (state.loading) return <Loading label="Loading fleet status" />
  if (state.error || !state.data) return <ErrorNotice message={state.error || 'Overview unavailable'} retry={state.reload} />
  const { overview, nodes, deployments, jobs } = state.data
  const attention = deployments.filter((item) => ['drift', 'conflict', 'failed', 'rolling_back'].includes(item.state)).slice(0, 6)
  return <>
    <PageHeader title="Overview" detail="Fleet posture, desired state, and work queue." actions={<Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />Refresh</Button>} />
    <section className="metrics-band">
      <Metric icon={<Network />} label="Nodes online" value={`${overview.onlineNodes}/${overview.nodes}`} tone={overview.onlineNodes === overview.nodes ? 'good' : 'warn'} />
      <Metric icon={<Boxes />} label="Library skills" value={overview.skills} />
      <Metric icon={<AlertTriangle />} label="Needs attention" value={overview.needsAttention} tone={overview.needsAttention ? 'bad' : 'good'} />
      <Metric icon={<Workflow />} label="Active jobs" value={overview.activeJobs} />
    </section>
    <div className="overview-grid">
      <section className="panel wide">
        <div className="panel-heading"><div><h2>Fleet</h2><p>Recent agent and runtime inventory.</p></div><Button variant="ghost" onClick={() => navigate('/nodes')}>All nodes<ArrowRight size={15} /></Button></div>
        {nodes.length === 0 ? <Empty title="No enrolled nodes" detail="Create an enrollment token from Nodes." /> : <div className="table-scroll"><table><thead><tr><th>Node</th><th>Platform</th><th>Runtimes</th><th>Last seen</th><th>Status</th></tr></thead><tbody>{nodes.slice(0, 8).map((node) => <tr key={node.id}><td><strong>{node.name}</strong></td><td>{node.platform}</td><td>{node.runtimeCount}</td><td>{relative(node.lastSeenAt)}</td><td><Status value={node.status} /></td></tr>)}</tbody></table></div>}
      </section>
      <section className="panel">
        <div className="panel-heading"><div><h2>Attention queue</h2><p>Drift, conflict, failure, and rollback.</p></div></div>
        {attention.length === 0 ? <Empty title="Desired state is clean" detail="No deployment requires intervention." /> : <div className="attention-list">{attention.map((item) => <button key={item.id} onClick={() => navigate('/skills')}><span><strong>{item.skillName}</strong><small>{item.nodeName} · {item.runtime}</small></span><Status value={item.state} /></button>)}</div>}
      </section>
      <section className="panel">
        <div className="panel-heading"><div><h2>Recent jobs</h2><p>Latest worker activity.</p></div><Button variant="ghost" onClick={() => navigate('/jobs')}>Queue<ArrowRight size={15} /></Button></div>
        {jobs.length === 0 ? <Empty title="Queue is empty" detail="Manual and scheduled jobs will appear here." /> : <div className="compact-list">{jobs.slice(0, 7).map((job) => <div key={job.id}><span><strong>{job.kind.replaceAll('_', ' ')}</strong><small>{relative(job.createdAt)}</small></span><Status value={job.status} /></div>)}</div>}
      </section>
    </div>
  </>
}

function Metric({ icon, label, value, tone = '' }: { icon: React.ReactNode; label: string; value: string | number; tone?: string }) {
  return <div className={`metric ${tone}`}><span>{icon}</span><div><strong>{value}</strong><small>{label}</small></div></div>
}

function relative(value?: string) {
  if (!value) return 'Never'
  const seconds = Math.max(0, (Date.now() - new Date(value).getTime()) / 1000)
  if (seconds < 60) return `${Math.floor(seconds)}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}
