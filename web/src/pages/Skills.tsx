import { FileUp, GitBranch, RefreshCw, Search } from 'lucide-react'
import { useRef, useState } from 'react'
import { api, type Operation, type Skill } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

export default function Skills() {
  const { t } = useI18n()
  const state = useData(() => api.list<Skill>('/skills'), [])
  const upload = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [importing, setImporting] = useState(false)
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const act = (work: Promise<unknown>, message: string) => {
    setBusy(true); setNotice('')
    work.then(() => { setNotice(message); state.reload() }).catch((reason: Error) => setNotice(reason.message)).finally(() => setBusy(false))
  }
  const items = (state.data?.items ?? []).filter((skill) => `${skill.name} ${skill.slug} ${skill.sourceKind}`.toLowerCase().includes(query.toLowerCase()))
  return <>
    <PageHeader title={t('Skills')} detail={t('Immutable Skill library')} actions={<>
      <input ref={upload} hidden type="file" accept=".zip,application/zip" onChange={(event) => { const file = event.target.files?.[0]; if (file) act(api.uploadSkill(file), t('Skill imported')) }} />
      <Button variant="secondary" disabled={busy} onClick={() => upload.current?.click()}><FileUp size={16} />{t('Upload ZIP')}</Button>
      <Button onClick={() => setImporting(true)}><GitBranch size={16} />{t('Import Git')}</Button>
    </>} />
    {notice && <div className="inline-notice">{notice}</div>}
    <div className="toolbar"><label className="search"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('Search skills')} /></label><Button variant="secondary" disabled={busy} onClick={() => act(api.post<Operation>('/updates/check'), t('Update check queued'))}><RefreshCw size={16} />{t('Check now')}</Button><IconButton label={t('Refresh')} onClick={state.reload}><RefreshCw size={17} /></IconButton></div>
    {state.loading ? <Loading label={t('Loading skills')} /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : items.length === 0 ? <Empty title={t('No Skills in Library')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Skill')}</th><th>{t('Tags')}</th><th>{t('Source')}</th><th>{t('Commit')}</th><th>{t('Artifact')}</th><th>{t('Updated')}</th></tr></thead><tbody>{items.map((skill) => <tr key={skill.id}><td><strong>{skill.name}</strong><small>{skill.slug}</small>{skill.description && <small>{skill.description}</small>}</td><td><TagToggle skill={skill} reload={state.reload} disabled={busy} /></td><td><Status value={skill.sourceKind} /></td><td><code>{skill.sourceCommit?.slice(0, 12) || '—'}</code></td><td><code>{skill.currentSha256.slice(0, 12)}</code><small>{skill.currentVersionId.slice(0, 8)}</small></td><td>{new Date(skill.updatedAt).toLocaleString()}</td></tr>)}</tbody></table></div>}
    {importing && <GitImport close={() => setImporting(false)} queued={() => { setImporting(false); setNotice(t('Import queued')) }} />}
  </>
}

function TagToggle({ skill, reload, disabled }: { skill: Skill; reload: () => void; disabled: boolean }) {
  const { t } = useI18n()
  const [busy, setBusy] = useState(false)
  const required = skill.tags.includes('required')
  const toggle = () => {
    setBusy(true)
    const tags = required ? skill.tags.filter((tag) => tag !== 'required') : [...skill.tags, 'required']
    api.updateSkillTags(skill.id, tags).then(reload).finally(() => setBusy(false))
  }
  return <Button variant="secondary" disabled={disabled || busy} onClick={toggle}>{required ? t('Required') : t('Optional')}</Button>
}

function GitImport({ close, queued }: { close: () => void; queued: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [url, setURL] = useState('')
  const [subdirectory, setSubdirectory] = useState('')
  const [commit, setCommit] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = () => {
    setBusy(true); setError('')
    api.post('/skills/import', { kind: 'git', name: name || url.split('/').pop() || 'Git Skill', url, subdirectory, commit }).then(queued).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false))
  }
  return <Modal title={t('Import Git Skill')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Name')}><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('HTTPS repository')}><input value={url} onChange={(event) => setURL(event.target.value)} placeholder="https://github.com/org/repository.git" /></Field><Field label={t('Subdirectory')}><input value={subdirectory} onChange={(event) => setSubdirectory(event.target.value)} /></Field><Field label={t('Commit or ref')}><input value={commit} onChange={(event) => setCommit(event.target.value)} /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button disabled={busy || !url} onClick={submit}>{t('Queue import')}</Button></div></Modal>
}
