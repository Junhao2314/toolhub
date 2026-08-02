import { Download, ExternalLink, Import, Search, Star, User } from 'lucide-react'
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, type Dict } from '../api/client'
import { Button, Empty, ErrorNotice, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useI18n } from '../i18n'

interface MarketSkill extends Dict { source: 'skillsmp' | 'xiaping'; id: string; name: string; description?: string; author?: string; stars?: number; downloads?: number; version?: string; status?: string; githubUrl?: string; sourceUrl?: string }

export default function Marketplace() {
  const { t } = useI18n()
  const [source, setSource] = useState('all')
  const [query, setQuery] = useState('')
  const [items, setItems] = useState<MarketSkill[]>([])
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<MarketSkill | null>(null)

  const performSearch = useCallback((src: string, q: string) => {
    setLoading(true)
    setError('')
    api.get<{ items: MarketSkill[]; errors?: Record<string, string> }>(`/market/search?source=${src}&q=${encodeURIComponent(q)}&page=1&limit=24`)
      .then((result) => { setItems(result.items); setErrors(result.errors ?? {}) })
      .catch((reason: Error) => setError(reason.message))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    performSearch(source, query)
  }, [source, performSearch])

  const search = (event: FormEvent) => {
    event.preventDefault()
    performSearch(source, query)
  }

  return <>
    <PageHeader title={t('Marketplace')} detail="SkillsMP / Xiaping" />
    <form className="market-search" onSubmit={search}>
      <select aria-label={t('Source')} value={source} onChange={(event) => setSource(event.target.value)}>
        <option value="all">{t('All sources')}</option>
        <option value="skillsmp">SkillsMP</option>
        <option value="xiaping">Xiaping</option>
      </select>
      <div className="search-bar">
        <Search size={16} />
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('Search marketplace')} />
      </div>
      <Button disabled={query.trim().length === 1 || loading}>{t('Search')}</Button>
    </form>
    {error && <ErrorNotice message={error} />}
    {Object.keys(errors).length > 0 && <ErrorNotice message={Object.entries(errors).map(([name, value]) => `${name}: ${value}`).join(' / ')} />}
    {loading ? <Loading label={t('Searching')} /> : items.length === 0 ? <Empty title={t('No marketplace results')} /> : <div className="market-grid">
      {items.map((skill) => <article className="market-item" key={`${skill.source}:${skill.id}`}>
        <header>
          <Status value={skill.source} />
          {skill.version && <span className="version-badge">v{skill.version}</span>}
          {skill.status && <Status value={skill.status} />}
        </header>
        <div className="market-item-body">
          <h2>{skill.name}</h2>
          <p>{skill.description || '—'}</p>
        </div>
        <div className="market-stats">
          <div className="stat-pill" title={t('Author')}>
            <User size={13} />
            <span>{skill.author || '—'}</span>
          </div>
          <div className="stat-pill" title={skill.source === 'xiaping' ? t('Downloads') : t('Stars')}>
            {skill.source === 'xiaping' ? <Download size={13} /> : <Star size={13} />}
            <span>{skill.source === 'xiaping' ? (skill.downloads ?? 0) : (skill.stars ?? 0)}</span>
          </div>
        </div>
        <footer>
          {(skill.sourceUrl || skill.githubUrl) && <a className="icon-button" href={skill.sourceUrl || skill.githubUrl} target="_blank" rel="noreferrer" title={t('Open source')}><ExternalLink size={16} /></a>}
          <Button variant="secondary" onClick={() => setSelected(skill)}><Import size={15} />{t('Import')}</Button>
        </footer>
      </article>)}
    </div>}
    {selected && <MarketImport item={selected} close={() => setSelected(null)} />}
  </>
}

function MarketImport({ item, close }: { item: MarketSkill; close: () => void }) {
  const { t } = useI18n()
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const payload = item.source === 'xiaping'
    ? { kind: 'xiaping', name: item.name, url: item.sourceUrl || item.githubUrl, metadata: { skillId: item.id } }
    : { kind: 'skillsmp', name: item.name, url: item.githubUrl }
  const submit = () => { setBusy(true); api.post('/skills/import', payload).then(() => setMessage(t('Import queued'))).catch((reason: Error) => setMessage(reason.message)).finally(() => setBusy(false)) }
  return <Modal title={`${t('Import')} · ${item.name}`} close={close}>{message && <div className="inline-notice">{message}</div>}<dl className="detail-list"><div><dt>{t('Source')}</dt><dd>{item.source}</dd></div><div><dt>{t('Version')}</dt><dd>{item.version || 'latest'}</dd></div><div><dt>{t('URL')}</dt><dd><code>{String(payload.url || '—')}</code></dd></div></dl><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Close')}</Button><Button disabled={busy || !payload.url || Boolean(message)} onClick={submit}>{t('Queue import')}</Button></div></Modal>
}

