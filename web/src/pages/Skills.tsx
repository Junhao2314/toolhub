import { Archive, Check, Download, FileUp, GitBranch, RefreshCw, RotateCcw, Search, ShieldAlert } from 'lucide-react'
import { useRef, useState } from 'react'
import { api } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'

interface Skill { id: string; name: string; slug: string; description: string; reviewStatus: string; riskLevel?: string; sourceKind?: string; sourceCommit?: string; sha256?: string; deploymentCount: number; protected: boolean }
interface Deployment { id: string; skillId: string; skillName: string; nodeName: string; runtime: string; state: string; desiredEnabled: boolean }

export default function Skills() {
  const [view, setView] = useState('Library')
  const [query, setQuery] = useState('')
  const [modal, setModal] = useState<'git' | 'targets' | null>(null)
  const [selected, setSelected] = useState<Skill | null>(null)
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  const upload = useRef<HTMLInputElement>(null)
  const state = useData(async () => {
    const [skills, deployments] = await Promise.all([api.list<Skill>('/skills'), api.list<Deployment>('/deployments')])
    return { skills: skills.items, deployments: deployments.items }
  }, [])
  const act = async (key: string, task: () => Promise<unknown>) => {
    setBusy(key); setNotice('')
    try { await task(); state.reload() } catch (error) { setNotice((error as Error).message) } finally { setBusy('') }
  }
  const filtered = (state.data?.skills ?? []).filter((skill) => `${skill.name} ${skill.description} ${skill.slug}`.toLowerCase().includes(query.toLowerCase()) && (view !== 'Review' || skill.reviewStatus === 'pending'))
  return <>
    <PageHeader title="Skills" detail="Immutable packages, review decisions, targets, and actual state." actions={<><input ref={upload} hidden type="file" accept=".zip,application/zip" onChange={(event) => { const file = event.target.files?.[0]; if (file) act('upload', () => api.uploadSkill(file)) }} /><Button variant="secondary" onClick={() => upload.current?.click()} disabled={busy === 'upload'}><FileUp size={16} />Upload</Button><Button onClick={() => setModal('git')}><GitBranch size={16} />Import</Button></>} />
    {notice && <ErrorNotice message={notice} />}
    <div className="toolbar"><Segments options={['Library', 'Review', 'Deployments']} value={view} onChange={setView} /><label className="search"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search skills" /></label><IconButton label="Refresh" onClick={state.reload}><RefreshCw size={17} /></IconButton></div>
    {state.loading ? <Loading label="Loading skills" /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : view === 'Deployments' ? <DeploymentTable deployments={state.data?.deployments ?? []} rollback={(id) => act(id, () => api.post(`/deployments/${id}/rollback`))} /> : filtered.length === 0 ? <Empty title={view === 'Review' ? 'Review queue is empty' : 'No skills in the library'} detail={view === 'Review' ? 'New imports requiring approval appear here.' : 'Import a Git package, SkillsMP entry, or ZIP.'} /> : <div className="table-scroll"><table><thead><tr><th>Skill</th><th>Source</th><th>Risk</th><th>Review</th><th>Targets</th><th aria-label="Actions" /></tr></thead><tbody>{filtered.map((skill) => <tr key={skill.id}><td><strong>{skill.name}</strong><small>{skill.description || skill.slug}</small></td><td>{skill.sourceKind ?? 'upload'}<small>{skill.sourceCommit?.slice(0, 9) || skill.sha256?.slice(0, 9)}</small></td><td><Status value={skill.riskLevel ?? 'unscored'} /></td><td><Status value={skill.reviewStatus} /></td><td>{skill.deploymentCount}</td><td className="row-actions">{skill.reviewStatus === 'pending' && <Button variant="secondary" disabled={busy === skill.id} onClick={() => act(skill.id, () => api.post(`/skills/${skill.id}/review`, { decision: 'approved' }))}><Check size={15} />Approve</Button>}<IconButton label="Set deployment targets" disabled={skill.reviewStatus !== 'approved'} onClick={() => { setSelected(skill); setModal('targets') }}><Download size={16} /></IconButton><IconButton label="Archive skill" disabled={skill.protected} onClick={() => act(skill.id, () => api.delete(`/skills/${skill.id}`))}><Archive size={16} /></IconButton></td></tr>)}</tbody></table></div>}
    {modal === 'git' && <ImportModal close={() => setModal(null)} imported={() => { setModal(null); state.reload() }} />}
    {modal === 'targets' && selected && <TargetModal skill={selected} close={() => setModal(null)} saved={() => { setModal(null); state.reload() }} />}
  </>
}

function DeploymentTable({ deployments, rollback }: { deployments: Deployment[]; rollback: (id: string) => void }) {
  if (!deployments.length) return <Empty title="No desired deployments" detail="Approve a skill and assign node/runtime targets." />
  return <div className="table-scroll"><table><thead><tr><th>Skill</th><th>Node</th><th>Runtime</th><th>Enabled</th><th>State</th><th /></tr></thead><tbody>{deployments.map((item) => <tr key={item.id}><td><strong>{item.skillName}</strong></td><td>{item.nodeName}</td><td>{item.runtime}</td><td>{item.desiredEnabled ? 'Yes' : 'No'}</td><td><Status value={item.state} /></td><td><IconButton label="Rollback deployment" onClick={() => rollback(item.id)}><RotateCcw size={16} /></IconButton></td></tr>)}</tbody></table></div>
}

function ImportModal({ close, imported }: { close: () => void; imported: () => void }) {
  const [url, setURL] = useState('')
  const [subdirectory, setSubdirectory] = useState('')
  const [commit, setCommit] = useState('')
  const [error, setError] = useState('')
  const submit = () => api.post('/skills', { kind: 'git', name: url.split('/').pop(), url, subdirectory, commit }).then(imported).catch((reason: Error) => setError(reason.message))
  return <Modal title="Import Git skill" close={close}>{error && <ErrorNotice message={error} />}<Field label="Repository URL"><input value={url} onChange={(event) => setURL(event.target.value)} placeholder="https://github.com/org/repository" /></Field><Field label="Subdirectory"><input value={subdirectory} onChange={(event) => setSubdirectory(event.target.value)} placeholder="skills/example" /></Field><Field label="Commit or ref" hint="ToolHub resolves and stores the exact commit."><input value={commit} onChange={(event) => setCommit(event.target.value)} placeholder="main or commit SHA" /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={submit} disabled={!url}>Queue import</Button></div></Modal>
}

function TargetModal({ skill, close, saved }: { skill: Skill; close: () => void; saved: () => void }) {
  const nodes = useData(() => api.list<{ id: string; name: string; status: string }>('/nodes'), [])
  const [targets, setTargets] = useState<Record<string, boolean>>({})
  const [error, setError] = useState('')
  const toggle = (key: string) => setTargets((current) => ({ ...current, [key]: !current[key] }))
  const submit = () => {
    const matrix = Object.entries(targets).filter(([, enabled]) => enabled).map(([key]) => { const [nodeId, runtime] = key.split(':'); return { nodeId, runtime, enabled: true } })
    api.post(`/skills/${skill.id}/deployments`, { targets: matrix, sync: true, dryRun: false }).then(saved).catch((reason: Error) => setError(reason.message))
  }
  return <Modal title={`Targets · ${skill.name}`} close={close}>{error && <ErrorNotice message={error} />}{nodes.loading ? <Loading /> : <div className="target-matrix"><div className="matrix-head"><span>Node</span><span>Codex</span><span>Claude</span><span>Hermes</span></div>{nodes.data?.items.map((node) => <div key={node.id}><span><strong>{node.name}</strong><Status value={node.status} /></span>{['codex', 'claude', 'hermes'].map((runtime) => <label key={runtime}><input type="checkbox" checked={targets[`${node.id}:${runtime}`] ?? false} onChange={() => toggle(`${node.id}:${runtime}`)} /><i /></label>)}</div>)}</div>}<div className="modal-actions"><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={submit}>Save and sync</Button></div></Modal>
}

const _unused = [ShieldAlert]
