import { Ban, RefreshCw, RotateCw } from 'lucide-react'
import { useState } from 'react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, IconButton, Loading, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'

interface Job extends Dict { id: string; kind: string; status: string; dryRun: boolean; attempts: number; maxAttempts: number; payload: Dict; result: Dict; createdAt: string; startedAt?: string; finishedAt?: string }

export default function Jobs() {
  const state = useData(() => api.list<Job>('/jobs'), [])
  const [filter, setFilter] = useState('All')
  const [error, setError] = useState('')
  const cancel = (id: string) => api.post(`/jobs/${id}/cancel`).then(state.reload).catch((reason: Error) => setError(reason.message))
  const jobs = (state.data?.items ?? []).filter((job) => filter === 'All' || job.status === filter.toLowerCase())
  return <>
    <PageHeader title="Jobs" detail="Scheduled and manual update, sync, inventory, MCP, and rollback work." actions={<Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />Refresh</Button>} />
    {error && <ErrorNotice message={error} />}
    <div className="toolbar"><Segments options={['All', 'Pending', 'Running', 'Failed', 'Succeeded']} value={filter} onChange={setFilter} /></div>
    {state.loading ? <Loading label="Loading job queue" /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : jobs.length === 0 ? <Empty title="No jobs in this view" detail="Scheduled checks and manual operations appear here." /> : <div className="table-scroll"><table><thead><tr><th>Job</th><th>Mode</th><th>Created</th><th>Attempts</th><th>Status</th><th /></tr></thead><tbody>{jobs.map((job) => <tr key={job.id}><td><strong>{job.kind.replaceAll('_', ' ')}</strong><small>{job.id.slice(0, 12)}</small></td><td>{job.dryRun ? 'Dry run' : 'Apply'}</td><td>{new Date(job.createdAt).toLocaleString()}</td><td>{job.attempts}/{job.maxAttempts}</td><td><Status value={job.status} /></td><td>{['pending', 'running'].includes(job.status) && <IconButton label="Cancel job" onClick={() => cancel(job.id)}><Ban size={16} /></IconButton>}</td></tr>)}</tbody></table></div>}
  </>
}

const _icons = [RotateCw]
