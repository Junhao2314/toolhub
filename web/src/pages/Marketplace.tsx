import { BrainCircuit, ExternalLink, Import, Search, ShieldCheck } from 'lucide-react'
import { useRef, useState, type FormEvent } from 'react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Field, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useI18n } from '../i18n'

type SourceID = 'all' | 'skillsmp' | 'xiaping'

interface MarketSkill extends Dict {
  source?: string
  id?: string
  name?: string
  description?: string
  author?: string
  stars?: number
  downloads?: number
  reviews?: number
  version?: string
  status?: string
  githubUrl?: string
  sourceUrl?: string
  categories?: string[]
  tags?: string[]
}

const SOURCE_LABELS: Record<string, string> = { skillsmp: 'SkillsMP', xiaping: '虾评 Xiaping' }

export default function Marketplace() {
  const { t } = useI18n()
  const [source, setSource] = useState<SourceID>('all')
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<MarketSkill[]>([])
  const [sourceErrors, setSourceErrors] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<MarketSkill | null>(null)
  const [recommend, setRecommend] = useState(false)
  const requestSequence = useRef(0)
  const runSearch = (value: SourceID) => {
    const requestID = ++requestSequence.current
    setLoading(true); setError(''); setSourceErrors({})
    api.get<{ items?: MarketSkill[]; errors?: Record<string, string> }>(`/market/search?source=${value}&q=${encodeURIComponent(query)}&page=1&limit=24`)
      .then((payload) => { if (requestID === requestSequence.current) { setResults(payload.items ?? []); setSourceErrors(payload.errors ?? {}) } })
      .catch((reason: Error) => { if (requestID === requestSequence.current) { setError(reason.message); setResults([]) } })
      .finally(() => { if (requestID === requestSequence.current) setLoading(false) })
  }
  const search = (event: FormEvent) => { event.preventDefault(); runSearch(source) }
  const selectSource = (value: SourceID) => { setSource(value); if (query.trim().length >= 2) runSearch(value) }
  const failedSources = Object.entries(sourceErrors)
  return <>
    <PageHeader title={t('Marketplace')} detail={t('Search SkillsMP and Xiaping, inspect provenance, then queue a reviewed import.')} actions={<Button variant="secondary" onClick={() => setRecommend(true)}><BrainCircuit size={16} />{t('AI recommendation')}</Button>} />
    <div className="market-tabs">{(['all', 'skillsmp', 'xiaping'] as SourceID[]).map((value) => <button key={value} type="button" aria-pressed={source === value} className={source === value ? 'active' : ''} onClick={() => selectSource(value)}>{value === 'all' ? t('All sources') : SOURCE_LABELS[value]}</button>)}</div>
    <form className="market-search" onSubmit={search}><Search size={20} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('Search by workflow, runtime, or capability')} /><Button type="submit" disabled={query.trim().length < 2}>{t('Search')}</Button></form>
    {error && <ErrorNotice message={error} />}
    {!error && failedSources.length > 0 && <ErrorNotice message={t('Some sources failed: {details}', { details: failedSources.map(([name, reason]) => `${SOURCE_LABELS[name] ?? name}: ${reason}`).join(' · ') })} />}
    {loading ? <Loading label={t('Searching the marketplace')} /> : results.length === 0 ? <Empty title={t('Search the marketplace')} detail={t('Results stay external until you explicitly queue an import.')} /> : <div className="market-grid">{results.map((skill, index) => <article key={`${skill.source}-${skill.id ?? index}`} className="market-item"><header><div className="package-icon"><ShieldCheck size={18} /></div><Status value={skill.status || 'unreviewed'} /></header><h2>{skill.name ?? t('Unnamed skill')}</h2><p>{skill.description ?? t('No description supplied by the provider.')}</p><dl><div><dt>{t('Source')}</dt><dd>{SOURCE_LABELS[skill.source ?? ''] ?? skill.source ?? '—'}</dd></div><div><dt>{t('Author')}</dt><dd>{skill.author ?? t('Unknown')}</dd></div>{skill.source === 'xiaping' ? <><div><dt>{t('Downloads')}</dt><dd>{skill.downloads ?? '—'}</dd></div><div><dt>{t('Reviews')}</dt><dd>{skill.reviews ?? '—'}</dd></div></> : <div><dt>{t('Stars')}</dt><dd>{skill.stars ?? '—'}</dd></div>}</dl><footer><Button variant="secondary" onClick={() => setSelected(skill)}><Import size={15} />{t('Review import')}</Button>{(skill.githubUrl || skill.sourceUrl) && <a className="icon-button" href={skill.githubUrl ?? skill.sourceUrl} target="_blank" rel="noreferrer" title={skill.source === 'xiaping' ? t('Open marketplace listing') : t('Open source repository')}><ExternalLink size={16} /></a>}</footer></article>)}</div>}
    {selected && (selected.source === 'xiaping' ? <ImportXiaping skill={selected} close={() => setSelected(null)} /> : <ImportMarket skill={selected} close={() => setSelected(null)} />)}
    {recommend && <Recommendation close={() => setRecommend(false)} />}
  </>
}

function ImportMarket({ skill, close }: { skill: MarketSkill; close: () => void }) {
  const { t } = useI18n()
  const [subdirectory, setSubdirectory] = useState('')
  const [commit, setCommit] = useState('')
  const [message, setMessage] = useState('')
  const submit = () => api.post('/skills', { kind: 'skillsmp', name: skill.name, githubUrl: skill.githubUrl, subdirectory, commit }).then(() => setMessage(t('Import queued. It will enter the review queue without deployment targets.'))).catch((error: Error) => setMessage(error.message))
  return <Modal title={`${t('Review import')} · ${skill.name ?? t('Skill')}`} close={close}><div className="provenance-block"><strong>{t('Source repository')}</strong><code>{skill.githubUrl ?? t('Missing repository URL')}</code><span>{t('ToolHub fetches a fixed commit and records canonical SHA-256 provenance.')}</span></div>{message && <div className="inline-notice">{message}</div>}<Field label={t('Package subdirectory')}><input value={subdirectory} onChange={(event) => setSubdirectory(event.target.value)} placeholder={t('Optional')} /></Field><Field label={t('Commit or ref')}><input value={commit} onChange={(event) => setCommit(event.target.value)} placeholder="HEAD" /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Close')}</Button><Button disabled={!skill.githubUrl} onClick={submit}>{t('Queue for review')}</Button></div></Modal>
}

function ImportXiaping({ skill, close }: { skill: MarketSkill; close: () => void }) {
  const { t } = useI18n()
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [queued, setQueued] = useState(false)
  const submit = () => {
    if (busy || queued || !skill.id) return
    setBusy(true); setMessage('')
    api.post('/skills', { kind: 'xiaping', externalId: skill.id })
      .then(() => { setQueued(true); setMessage(t('Import queued. It will enter the review queue without deployment targets.')) })
      .catch((error: Error) => setMessage(error.message))
      .finally(() => setBusy(false))
  }
  return <Modal title={`${t('Review import')} · ${skill.name ?? t('Skill')}`} close={close}><div className="provenance-block"><strong>{t('Xiaping skill page')}</strong><code>{skill.sourceUrl ?? skill.id}</code><span>{t('ToolHub downloads the platform ZIP with the configured XIAPING_API_KEY, scans it, and queues a reviewed import. The provider reports any coin charge; ToolHub never automatically retries a charged download.')}</span></div>{message && <div className="inline-notice">{message}</div>}<div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Close')}</Button><Button disabled={!skill.id || busy || queued} onClick={submit}>{busy ? t('Queuing...') : t('Queue for review')}</Button></div></Modal>
}

function Recommendation({ close }: { close: () => void }) {
  const { t } = useI18n()
  const [requirement, setRequirement] = useState('')
  const [candidates, setCandidates] = useState<Array<{ name: string; reasons: string[]; risks: string[]; confidence: number }>>([])
  const [error, setError] = useState('')
  const submit = () => api.post<{ candidates: typeof candidates }>('/recommendations', { requirement, inventory: {}, tags: [] }).then((value) => setCandidates(value.candidates)).catch((reason: Error) => setError(reason.message))
  return <Modal title={t('AI skill recommendation')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Operational requirement')}><textarea rows={4} value={requirement} onChange={(event) => setRequirement(event.target.value)} placeholder={t('Describe the workflow or capability gap.')} /></Field>{candidates.length > 0 && <div className="recommendations">{candidates.map((candidate) => <div key={candidate.name}><strong>{candidate.name}</strong><span>{t('{n}% confidence', { n: Math.round(candidate.confidence * 100) })}</span><p>{candidate.reasons.join(' · ')}</p>{candidate.risks.length > 0 && <small>{t('Risk: {risks}', { risks: candidate.risks.join(' · ') })}</small>}</div>)}</div>}<div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Close')}</Button><Button onClick={submit} disabled={requirement.length < 5}>{t('Recommend')}</Button></div></Modal>
}
