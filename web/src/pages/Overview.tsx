import { AlertTriangle, ArrowRight, Boxes, Bot, MonitorCog, RefreshCw } from 'lucide-react'
import { api, type Operation, type Target } from '../api/client'
import { Button, Empty, ErrorNotice, Loading, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface OverviewData { targets: number; unhealthyTargets: number; skills: number; activeOperations: number; mcpServers: number; openAlerts: number; auditEvents24h: number }

export default function Overview({ navigate }: { navigate: (path: string) => void }) {
  const { t } = useI18n()
  const state = useData(async () => {
    const [overview, targets, operations] = await Promise.all([api.get<OverviewData>('/overview'), api.list<Target>('/targets'), api.list<Operation>('/operations')])
    return { overview, targets: targets.items, operations: operations.items }
  }, [])
  if (state.loading) return <Loading label={t('Loading fleet status')} />
  if (state.error || !state.data) return <ErrorNotice message={state.error || t('Overview unavailable')} retry={state.reload} />
  const { overview, targets, operations } = state.data
  const attention = targets.filter((target) => target.health !== 'healthy').slice(0, 8)
  return <>
    <PageHeader title={t('Overview')} detail={t('Desired state and target health')} actions={<Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button>} />
    <section className="metrics-band">
      <Metric icon={<MonitorCog />} label={t('Targets')} value={overview.targets} />
      <Metric icon={<AlertTriangle />} label={t('Needs attention')} value={overview.unhealthyTargets} tone={overview.unhealthyTargets ? 'bad' : 'good'} />
      <Metric icon={<Boxes />} label={t('Skills')} value={overview.skills} />
      <Metric icon={<Bot />} label={t('MCP servers')} value={overview.mcpServers} />
    </section>
    <div className="overview-grid">
      <section className="panel wide"><div className="panel-heading"><h2>{t('Target health')}</h2><Button variant="ghost" onClick={() => navigate('/targets')}>{t('Targets')}<ArrowRight size={15} /></Button></div>{attention.length === 0 ? <Empty title={t('All desired targets are healthy')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Target')}</th><th>{t('Runtime')}</th><th>{t('Desired revision')}</th><th>{t('State')}</th><th>{t('Reason')}</th></tr></thead><tbody>{attention.map((target) => <tr key={target.id}><td><strong>{target.nodeName}</strong><small>{target.targetKey}</small></td><td>{target.runtime}</td><td>{target.desiredRevision || '—'}</td><td><Status value={target.health} /></td><td>{target.errorReason || '—'}</td></tr>)}</tbody></table></div>}</section>
      <section className="panel"><div className="panel-heading"><h2>{t('Recent operations')}</h2><Button variant="ghost" onClick={() => navigate('/operations')}>{t('History')}<ArrowRight size={15} /></Button></div>{operations.length === 0 ? <Empty title={t('No operations')} /> : <div className="compact-list">{operations.slice(0, 8).map((operation) => <div key={operation.id}><span><strong>{operation.kind.replaceAll('_', ' ')}</strong><small>{new Date(operation.createdAt).toLocaleString()}</small></span><Status value={operation.status} /></div>)}</div>}</section>
    </div>
  </>
}

function Metric({ icon, label, value, tone = '' }: { icon: React.ReactNode; label: string; value: number; tone?: string }) {
  return <div className={`metric ${tone}`}><span>{icon}</span><div><strong>{value}</strong><small>{label}</small></div></div>
}
