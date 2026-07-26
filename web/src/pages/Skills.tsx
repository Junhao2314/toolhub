import { Archive, Check, Download, FileUp, GitBranch, RefreshCw, RotateCcw, Search, ShieldAlert, WandSparkles } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface Skill { id: string; name: string; slug: string; description: string; reviewStatus: string; riskLevel?: string; sourceKind?: string; sourceCommit?: string; sha256?: string; deploymentCount: number; protected: boolean }
interface Deployment { id: string; skillId: string; skillName: string; nodeName: string; runtime: string; state: string; desiredEnabled: boolean }
interface Discovery { id: string; kind: string; nodeName: string; runtime: string; name: string; path: string; sha256: string; managed: boolean; protected: boolean; missing: boolean; drift: boolean; status: string; lastError: string; linkCoverage?: Record<string, string> }

export default function Skills({ canAdopt }: { canAdopt: boolean }) {
  const { t } = useI18n()
  const [view, setView] = useState('Library')
  const [query, setQuery] = useState('')
  const [modal, setModal] = useState<'git' | 'targets' | null>(null)
  const [selected, setSelected] = useState<Skill | null>(null)
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  const upload = useRef<HTMLInputElement>(null)
  const state = useData(async () => {
    const [skills, deployments, discoveries] = await Promise.all([api.list<Skill>('/skills'), api.list<Deployment>('/deployments'), api.list<Discovery>('/discoveries')])
    return { skills: skills.items, deployments: deployments.items, discoveries: discoveries.items.filter((item) => item.kind === 'skill') }
  }, [])
  const act = async (key: string, task: () => Promise<unknown>) => {
    setBusy(key); setNotice('')
    try { await task(); state.reload() } catch (error) { setNotice((error as Error).message) } finally { setBusy('') }
  }
  const filtered = (state.data?.skills ?? []).filter((skill) => `${skill.name} ${skill.description} ${skill.slug}`.toLowerCase().includes(query.toLowerCase()) && (view !== 'Review' || skill.reviewStatus === 'pending'))
  return <>
    <PageHeader title={t('Skills')} detail={t('Immutable packages, review decisions, targets, and actual state.')} actions={<><input ref={upload} hidden type="file" accept=".zip,application/zip" onChange={(event) => { const file = event.target.files?.[0]; if (file) act('upload', () => api.uploadSkill(file)) }} /><Button variant="secondary" onClick={() => upload.current?.click()} disabled={busy === 'upload'}><FileUp size={16} />{t('Upload')}</Button><Button onClick={() => setModal('git')}><GitBranch size={16} />{t('Import')}</Button></>} />
    {notice && <ErrorNotice message={notice} />}
    <div className="toolbar"><Segments options={['Library', 'Discovered', 'Review', 'Deployments']} value={view} onChange={setView} /><label className="search"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('Search skills')} /></label><IconButton label={t('Refresh')} onClick={state.reload}><RefreshCw size={17} /></IconButton></div>
    {state.loading ? <Loading label={t('Loading skills')} /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : view === 'Deployments' ? <DeploymentTable deployments={state.data?.deployments ?? []} rollback={(id) => act(id, () => api.post(`/deployments/${id}/rollback`))} /> : view === 'Discovered' ? <DiscoveredTable items={state.data?.discoveries ?? []} busy={busy} canAdopt={canAdopt} adopt={(id) => act(id, () => api.post(`/discoveries/${id}/adopt-skill`))} /> : filtered.length === 0 ? <Empty title={view === 'Review' ? t('Review queue is empty') : t('No skills in the library')} detail={view === 'Review' ? t('New imports requiring approval appear here.') : t('Import a Git package, SkillsMP entry, or ZIP.')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Skill')}</th><th>{t('Source')}</th><th>{t('Risk')}</th><th>{t('Review')}</th><th>{t('Targets')}</th><th aria-label="Actions" /></tr></thead><tbody>{filtered.map((skill) => <tr key={skill.id}><td><strong>{skill.name}</strong><small>{skill.description || skill.slug}</small></td><td>{skill.sourceKind ?? 'upload'}<small>{skill.sourceCommit?.slice(0, 9) || skill.sha256?.slice(0, 9)}</small></td><td><Status value={skill.riskLevel ?? 'unscored'} /></td><td><Status value={skill.reviewStatus} /></td><td>{skill.deploymentCount}</td><td className="row-actions">{skill.reviewStatus === 'pending' && <Button variant="secondary" disabled={busy === skill.id} onClick={() => act(skill.id, () => api.post(`/skills/${skill.id}/review`, { decision: 'approved' }))}><Check size={15} />{t('Approve')}</Button>}<IconButton label={t('Set deployment targets')} disabled={skill.reviewStatus !== 'approved'} onClick={() => { setSelected(skill); setModal('targets') }}><Download size={16} /></IconButton><IconButton label={t('Archive skill')} disabled={skill.protected} onClick={() => act(skill.id, () => api.delete(`/skills/${skill.id}`))}><Archive size={16} /></IconButton></td></tr>)}</tbody></table></div>}
    {modal === 'git' && <ImportModal close={() => setModal(null)} imported={() => { setModal(null); state.reload() }} />}
    {modal === 'targets' && selected && <TargetModal skill={selected} close={() => setModal(null)} saved={() => { setModal(null); state.reload() }} />}
  </>
}

function DiscoveredTable({ items, busy, canAdopt, adopt }: { items: Discovery[]; busy: string; canAdopt: boolean; adopt: (id: string) => void }) {
  const { t } = useI18n()
  if (!items.length) return <Empty title={t('No discovered Skills')} detail={t('Unmanaged runtime Skills appear after the next inventory scan.')} />
  return <div className="table-scroll"><table><thead><tr><th>{t('Skill')}</th><th>{t('Node / runtime')}</th><th>{t('Hash')}</th><th>{t('State')}</th><th /></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td><strong>{item.name}</strong><small>{item.path}</small>{item.runtime === 'shared' && <small>{t('Consumer links: {coverage}', { coverage: Object.entries(item.linkCoverage ?? {}).map(([runtime, state]) => `${runtime}:${state}`).join(', ') || t('not reported') })}</small>}</td><td>{item.nodeName}<small>{item.runtime === 'shared' ? t('Canonical shared source') : item.runtime}</small></td><td><code>{item.sha256?.slice(0, 12) || t('unavailable')}</code></td><td><Status value={item.missing ? 'missing' : item.drift ? 'drift' : item.status} />{item.lastError && <small>{item.lastError}</small>}</td><td className="row-actions">{canAdopt && <Button variant="secondary" disabled={busy === item.id || item.managed || item.protected || item.missing || !item.sha256} onClick={() => adopt(item.id)}><WandSparkles size={15} />{t('Adopt')}</Button>}</td></tr>)}</tbody></table></div>
}

function DeploymentTable({ deployments, rollback }: { deployments: Deployment[]; rollback: (id: string) => void }) {
  const { t } = useI18n()
  if (!deployments.length) return <Empty title={t('No desired deployments')} detail={t('Approve a skill and assign node/runtime targets.')} />
  return <div className="table-scroll"><table><thead><tr><th>{t('Skill')}</th><th>{t('Node')}</th><th>{t('Runtime')}</th><th>{t('Enabled')}</th><th>{t('State')}</th><th /></tr></thead><tbody>{deployments.map((item) => <tr key={item.id}><td><strong>{item.skillName}</strong></td><td>{item.nodeName}</td><td>{item.runtime}</td><td>{item.desiredEnabled ? t('Yes') : t('No')}</td><td><Status value={item.state} /></td><td><IconButton label={t('Rollback deployment')} onClick={() => rollback(item.id)}><RotateCcw size={16} /></IconButton></td></tr>)}</tbody></table></div>
}

function ImportModal({ close, imported }: { close: () => void; imported: () => void }) {
  const { t } = useI18n()
  const [url, setURL] = useState('')
  const [subdirectory, setSubdirectory] = useState('')
  const [commit, setCommit] = useState('')
  const [error, setError] = useState('')
  const submit = () => api.post('/skills', { kind: 'git', name: url.split('/').pop(), url, subdirectory, commit }).then(imported).catch((reason: Error) => setError(reason.message))
  return <Modal title={t('Import Git skill')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Repository URL')}><input value={url} onChange={(event) => setURL(event.target.value)} placeholder="https://github.com/org/repository" /></Field><Field label={t('Subdirectory')}><input value={subdirectory} onChange={(event) => setSubdirectory(event.target.value)} placeholder="skills/example" /></Field><Field label={t('Commit or ref')} hint={t('ToolHub resolves and stores the exact commit.')}><input value={commit} onChange={(event) => setCommit(event.target.value)} placeholder={t('main or commit SHA')} /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={!url}>{t('Queue import')}</Button></div></Modal>
}

function TargetModal({ skill, close, saved }: { skill: Skill; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const nodes = useData(() => api.list<{ id: string; name: string; status: string; isLocal: boolean; runtimeKinds: string[] }>('/nodes'), [])
  const [targets, setTargets] = useState<Record<string, boolean>>({})
  const [error, setError] = useState('')
  const defaultsApplied = useRef(false)
  useEffect(() => {
    if (defaultsApplied.current || !nodes.data) return
    defaultsApplied.current = true
    const local = nodes.data.items.find((node) => node.isLocal)
    if (!local) return
    const defaults = local.runtimeKinds.filter((runtime) => runtime !== 'shared')
    setTargets(Object.fromEntries(defaults.map((runtime) => [`${local.id}:${runtime}`, true])))
  }, [nodes.data])
  const toggle = (key: string) => setTargets((current) => ({ ...current, [key]: !current[key] }))
  const submit = () => {
    const matrix = Object.entries(targets).filter(([, enabled]) => enabled).map(([key]) => { const [nodeId, runtime] = key.split(':'); return { nodeId, runtime, enabled: true } })
    api.post(`/skills/${skill.id}/deployments`, { targets: matrix, sync: true, dryRun: false }).then(saved).catch((reason: Error) => setError(reason.message))
  }
  const runtimes = ['codex', 'claude', 'hermes', 'grok', 'openclaw']
  return <Modal title={`${t('Targets')} · ${skill.name}`} close={close}>{error && <ErrorNotice message={error} />}{nodes.loading ? <Loading /> : <><div className="inline-notice"><ShieldAlert size={16} />{t('Skills are materialized per runtime; the legacy Shared deployment target is retired.')}</div><div className="target-matrix wide"><div className="matrix-head"><span>{t('Node')}</span>{runtimes.map((runtime) => <span key={runtime}>{runtime}</span>)}</div>{nodes.data?.items.map((node) => <div key={node.id} className={node.isLocal ? 'local-target' : ''}><span><strong>{node.name}</strong>{node.isLocal && <small>{t('Project host')}</small>}<Status value={node.status} /></span>{runtimes.map((runtime) => { const available = node.runtimeKinds.includes(runtime); return <label key={runtime} title={available ? runtime : t('{runtime} not available', { runtime })}><input type="checkbox" disabled={!available} checked={targets[`${node.id}:${runtime}`] ?? false} onChange={() => toggle(`${node.id}:${runtime}`)} /><i /></label> })}</div>)}</div></>}<div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit} disabled={!Object.values(targets).some(Boolean)}>{t('Save and sync')}</Button></div></Modal>
}
