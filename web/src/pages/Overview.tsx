import { AlertTriangle, ArrowRight, Boxes, Network, RefreshCw, Workflow } from 'lucide-react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Loading, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

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
  const { t } = useI18n()
  const state = useData(async () => {
    const [overview, nodes, deployments, jobs] = await Promise.all([
      api.get<OverviewData>('/overview'), api.list<NodeRow>('/nodes'), api.list<Deployment>('/deployments'), api.list<Job>('/jobs'),
    ])
    return { overview, nodes: nodes.items, deployments: deployments.items, jobs: jobs.items }
  }, [])
  const relative = (value?: string) => {
    if (!value) return t('Never')
    const seconds = Math.max(0, (Date.now() - new Date(value).getTime()) / 1000)
    if (seconds < 60) return t('{n}s ago', { n: Math.floor(seconds) })
    if (seconds < 3600) return t('{n}m ago', { n: Math.floor(seconds / 60) })
    if (seconds < 86400) return t('{n}h ago', { n: Math.floor(seconds / 3600) })
    return t('{n}d ago', { n: Math.floor(seconds / 86400) })
  }
  if (state.loading) return <Loading label={t('Loading fleet status')} />
  if (state.error || !state.data) return <ErrorNotice message={state.error || t('Overview unavailable')} retry={state.reload} />
  const { overview, nodes, deployments, jobs } = state.data
  const attention = deployments.filter((item) => ['drift', 'conflict', 'failed', 'rolling_back'].includes(item.state)).slice(0, 6)
  return <>
    <PageHeader title={t('Overview')} detail={t('Fleet posture, desired state, and work queue.')} actions={<Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button>} />
    <section className="metrics-band">
      <Metric icon={<Network />} label={t('Nodes online')} value={`${overview.onlineNodes}/${overview.nodes}`} tone={overview.onlineNodes === overview.nodes ? 'good' : 'warn'} />
      <Metric icon={<Boxes />} label={t('Library skills')} value={overview.skills} />
      <Metric icon={<AlertTriangle />} label={t('Needs attention')} value={overview.needsAttention} tone={overview.needsAttention ? 'bad' : 'good'} />
      <Metric icon={<Workflow />} label={t('Active jobs')} value={overview.activeJobs} />
    </section>
    <div className="overview-grid">
      <section className="panel wide">
        <div className="panel-heading"><div><h2>{t('Fleet')}</h2><p>{t('Recent agent and runtime inventory.')}</p></div><Button variant="ghost" onClick={() => navigate('/nodes')}>{t('All nodes')}<ArrowRight size={15} /></Button></div>
        {nodes.length === 0 ? <Empty title={t('No enrolled nodes')} detail={t('Create an enrollment token from Nodes.')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Node')}</th><th>{t('Platform')}</th><th>{t('Runtimes')}</th><th>{t('Last seen')}</th><th>{t('Status')}</th></tr></thead><tbody>{nodes.slice(0, 8).map((node) => <tr key={node.id}><td><strong>{node.name}</strong></td><td>{node.platform}</td><td>{node.runtimeCount}</td><td>{relative(node.lastSeenAt)}</td><td><Status value={node.status} /></td></tr>)}</tbody></table></div>}
      </section>
      <section className="panel">
        <div className="panel-heading"><div><h2>{t('Attention queue')}</h2><p>{t('Drift, conflict, failure, and rollback.')}</p></div></div>
        {attention.length === 0 ? <Empty title={t('Desired state is clean')} detail={t('No deployment requires intervention.')} /> : <div className="attention-list">{attention.map((item) => <button key={item.id} onClick={() => navigate('/skills')}><span><strong>{item.skillName}</strong><small>{item.nodeName} · {item.runtime}</small></span><Status value={item.state} /></button>)}</div>}
      </section>
      <section className="panel">
        <div className="panel-heading"><div><h2>{t('Recent jobs')}</h2><p>{t('Latest worker activity.')}</p></div><Button variant="ghost" onClick={() => navigate('/jobs')}>{t('Queue')}<ArrowRight size={15} /></Button></div>
        {jobs.length === 0 ? <Empty title={t('Queue is empty')} detail={t('Manual and scheduled jobs will appear here.')} /> : <div className="compact-list">{jobs.slice(0, 7).map((job) => <div key={job.id}><span><strong>{job.kind.replaceAll('_', ' ')}</strong><small>{relative(job.createdAt)}</small></span><Status value={job.status} /></div>)}</div>}
      </section>
    </div>
  </>
}

function Metric({ icon, label, value, tone = '' }: { icon: React.ReactNode; label: string; value: string | number; tone?: string }) {
  return <div className={`metric ${tone}`}><span>{icon}</span><div><strong>{value}</strong><small>{label}</small></div></div>
}
