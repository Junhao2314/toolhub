import { RefreshCw, Save } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, type Operation, type Settings, type Skill } from '../api/client'
import { Button, ErrorNotice, Field, Loading, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

export default function SettingsPage() {
  const { t } = useI18n()
  const state = useData(() => api.get<Settings>('/settings'), [])
  const skillsState = useData(() => api.list<Skill>('/skills'), [])
  const [form, setForm] = useState<Settings | null>(null)
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (state.data) {
      setForm({
        ...state.data,
        kimiFrontendBundle: state.data.kimiFrontendBundle ?? [],
        piBundle: state.data.piBundle ?? [],
      })
    }
  }, [state.data])
  const save = () => { if (!form) return; setBusy(true); setNotice(''); api.put<Settings>('/settings', form).then((value) => { setForm(value); setNotice(t('Settings saved')) }).catch((reason: Error) => setNotice(reason.message)).finally(() => setBusy(false)) }
  const check = () => { setBusy(true); api.post<Operation>('/updates/check').then((operation) => setNotice(`${t('Update check queued')} · ${operation.id.slice(0, 8)}`)).catch((reason: Error) => setNotice(reason.message)).finally(() => setBusy(false)) }
  if (state.loading || skillsState.loading || !form) return <Loading />
  if (state.error) return <ErrorNotice message={state.error} retry={state.reload} />
  if (skillsState.error) return <ErrorNotice message={skillsState.error} retry={skillsState.reload} />
  const skills = skillsState.data?.items ?? []
  const toggleBundleSkill = (bundle: 'kimiFrontendBundle' | 'piBundle', slug: string) => {
    if (!form) return
    const selected = new Set(form[bundle])
    if (selected.has(slug)) {
      if (bundle === 'kimiFrontendBundle' && slug === 'ui-ux-pro-max-cn') return
      selected.delete(slug)
    } else {
      selected.add(slug)
    }
    setForm({ ...form, [bundle]: skills.filter((skill) => selected.has(skill.slug)).map((skill) => skill.slug) })
  }
  return <>
    <PageHeader title={t('Settings')} detail={t('Global runtime and update policy')} actions={<Button variant="secondary" disabled={busy} onClick={check}><RefreshCw size={16} />{t('Check now')}</Button>} />
    {notice && <div className="inline-notice">{notice}</div>}
    <section className="settings-section"><header><h2>{t('Managed runtime')}</h2><Status value={form.relayIntentionalPaused ? 'paused' : 'active'} /></header><div className="form-grid"><Field label={t('Managed username')}><input value={form.managedUsername} onChange={(event) => setForm({ ...form, managedUsername: event.target.value })} /></Field><Field label={t('Relay port')}><input type="number" min={1} max={65535} value={form.relayPort} onChange={(event) => setForm({ ...form, relayPort: Number(event.target.value) })} /></Field></div></section>
    <section className="settings-section"><header><h2>{t('Library updates')}</h2></header><div className="form-grid"><Field label={t('Cron schedule')}><input value={form.updateCron} onChange={(event) => setForm({ ...form, updateCron: event.target.value })} /></Field><Field label={t('Timezone')}><input value={form.timezone} onChange={(event) => setForm({ ...form, timezone: event.target.value })} /></Field></div></section>
    <section className="settings-section"><header><h2>{t('Subagent routing bundles')}</h2></header><p className="section-help">{t('These bundles control curated external CLI Skills. They do not Apply a runtime Profile.')}</p><div className="bundle-grid">
      <BundleEditor title={t('Kimi frontend bundle')} source={t('Hermes configured Skill root')} requiredSlug="ui-ux-pro-max-cn" selected={form.kimiFrontendBundle} skills={skills} onToggle={(slug) => toggleBundleSkill('kimiFrontendBundle', slug)} t={t} />
      <BundleEditor title={t('Pi agent bundle')} source={t('Pi configured Skill root')} selected={form.piBundle} skills={skills} onToggle={(slug) => toggleBundleSkill('piBundle', slug)} t={t} />
    </div></section>
    <div className="page-save"><Button disabled={busy} onClick={save}><Save size={16} />{t('Save settings')}</Button></div>
  </>
}

function BundleEditor({ title, source, requiredSlug, selected, skills, onToggle, t }: { title: string; source: string; requiredSlug?: string; selected: string[]; skills: Skill[]; onToggle: (slug: string) => void; t: (key: string) => string }) {
  return <div className="bundle-card"><header><div><h3>{title}</h3><small>{t('Source root')}: <code>{source}</code></small></div><Status value={`${selected.length} ${t('selected')}`} /></header><div className="bundle-list">
    {skills.length === 0 ? <p>{t('No Skills in Library')}</p> : skills.map((skill) => <label key={skill.id} className="selection-row"><input type="checkbox" checked={selected.includes(skill.slug)} disabled={skill.slug === requiredSlug} onChange={() => onToggle(skill.slug)} /><span><strong>{skill.name}</strong><small>{skill.slug}</small></span></label>)}
  </div>{requiredSlug && <small className="bundle-note">{t('ui-ux-pro-max-cn is mandatory for Kimi frontend dispatches.')}</small>}</div>
}
