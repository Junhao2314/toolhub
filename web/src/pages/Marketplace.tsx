import { BrainCircuit, ExternalLink, Import, Search, ShieldCheck } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Field, Loading, Modal, PageHeader, Status } from '../components/ui'

interface MarketSkill extends Dict { id?: string; name?: string; description?: string; githubUrl?: string; author?: string; stars?: number; category?: string; risk?: string }

export default function Marketplace() {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<MarketSkill[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<MarketSkill | null>(null)
  const [recommend, setRecommend] = useState(false)
  const search = (event: FormEvent) => {
    event.preventDefault(); setLoading(true); setError('')
    api.get<Dict>(`/market/search?q=${encodeURIComponent(query)}&page=1&limit=24`).then((payload) => {
      const raw = (payload.items ?? payload.skills ?? payload.data ?? []) as unknown
      setResults(Array.isArray(raw) ? raw as MarketSkill[] : [])
    }).catch((reason: Error) => setError(reason.message)).finally(() => setLoading(false))
  }
  return <>
    <PageHeader title="Marketplace" detail="Search SkillsMP, inspect provenance, then queue a reviewed import." actions={<Button variant="secondary" onClick={() => setRecommend(true)}><BrainCircuit size={16} />AI recommendation</Button>} />
    <form className="market-search" onSubmit={search}><Search size={20} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search by workflow, runtime, or capability" /><Button type="submit" disabled={query.trim().length < 2}>Search</Button></form>
    {error && <ErrorNotice message={error} />}
    {loading ? <Loading label="Searching SkillsMP" /> : results.length === 0 ? <Empty title="Search the marketplace" detail="Results stay external until you explicitly queue an import." /> : <div className="market-grid">{results.map((skill, index) => <article key={skill.id ?? `${skill.name}-${index}`} className="market-item"><header><div className="package-icon"><ShieldCheck size={18} /></div><Status value={skill.risk ?? 'unreviewed'} /></header><h2>{skill.name ?? 'Unnamed skill'}</h2><p>{skill.description ?? 'No description supplied by the provider.'}</p><dl><div><dt>Author</dt><dd>{skill.author ?? 'Unknown'}</dd></div><div><dt>Stars</dt><dd>{skill.stars ?? '—'}</dd></div></dl><footer><Button variant="secondary" onClick={() => setSelected(skill)}><Import size={15} />Review import</Button>{skill.githubUrl && <a className="icon-button" href={skill.githubUrl} target="_blank" rel="noreferrer" title="Open source repository"><ExternalLink size={16} /></a>}</footer></article>)}</div>}
    {selected && <ImportMarket skill={selected} close={() => setSelected(null)} />}
    {recommend && <Recommendation close={() => setRecommend(false)} />}
  </>
}

function ImportMarket({ skill, close }: { skill: MarketSkill; close: () => void }) {
  const [subdirectory, setSubdirectory] = useState('')
  const [commit, setCommit] = useState('')
  const [message, setMessage] = useState('')
  const submit = () => api.post('/skills', { kind: 'skillsmp', name: skill.name, githubUrl: skill.githubUrl, subdirectory, commit }).then(() => setMessage('Import queued. It will enter the review queue without deployment targets.')).catch((error: Error) => setMessage(error.message))
  return <Modal title={`Review import · ${skill.name ?? 'Skill'}`} close={close}><div className="provenance-block"><strong>Source repository</strong><code>{skill.githubUrl ?? 'Missing repository URL'}</code><span>ToolHub fetches a fixed commit and records canonical SHA-256 provenance.</span></div>{message && <div className="inline-notice">{message}</div>}<Field label="Package subdirectory"><input value={subdirectory} onChange={(event) => setSubdirectory(event.target.value)} placeholder="Optional" /></Field><Field label="Commit or ref"><input value={commit} onChange={(event) => setCommit(event.target.value)} placeholder="HEAD" /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>Close</Button><Button disabled={!skill.githubUrl} onClick={submit}>Queue for review</Button></div></Modal>
}

function Recommendation({ close }: { close: () => void }) {
  const [requirement, setRequirement] = useState('')
  const [candidates, setCandidates] = useState<Array<{ name: string; reasons: string[]; risks: string[]; confidence: number }>>([])
  const [error, setError] = useState('')
  const submit = () => api.post<{ candidates: typeof candidates }>('/recommendations', { requirement, inventory: {}, tags: [] }).then((value) => setCandidates(value.candidates)).catch((reason: Error) => setError(reason.message))
  return <Modal title="AI skill recommendation" close={close}>{error && <ErrorNotice message={error} />}<Field label="Operational requirement"><textarea rows={4} value={requirement} onChange={(event) => setRequirement(event.target.value)} placeholder="Describe the workflow or capability gap." /></Field>{candidates.length > 0 && <div className="recommendations">{candidates.map((candidate) => <div key={candidate.name}><strong>{candidate.name}</strong><span>{Math.round(candidate.confidence * 100)}% confidence</span><p>{candidate.reasons.join(' · ')}</p>{candidate.risks.length > 0 && <small>Risk: {candidate.risks.join(' · ')}</small>}</div>)}</div>}<div className="modal-actions"><Button variant="secondary" onClick={close}>Close</Button><Button onClick={submit} disabled={requirement.length < 5}>Recommend</Button></div></Modal>
}
