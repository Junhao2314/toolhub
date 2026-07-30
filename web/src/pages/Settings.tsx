import { RefreshCw, Save } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, type Operation, type Settings } from '../api/client'
import { Button, ErrorNotice, Field, Loading, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

export default function SettingsPage() {
  const { t } = useI18n()
  const state = useData(() => api.get<Settings>('/settings'), [])
  const [form, setForm] = useState<Settings | null>(null)
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (state.data) setForm(state.data) }, [state.data])
  const save = () => { if (!form) return; setBusy(true); setNotice(''); api.put<Settings>('/settings', form).then((value) => { setForm(value); setNotice(t('Settings saved')) }).catch((reason: Error) => setNotice(reason.message)).finally(() => setBusy(false)) }
  const check = () => { setBusy(true); api.post<Operation>('/updates/check').then((operation) => setNotice(`${t('Update check queued')} · ${operation.id.slice(0, 8)}`)).catch((reason: Error) => setNotice(reason.message)).finally(() => setBusy(false)) }
  if (state.loading || !form) return <Loading />
  if (state.error) return <ErrorNotice message={state.error} retry={state.reload} />
  return <>
    <PageHeader title={t('Settings')} detail={t('Global runtime and update policy')} actions={<Button variant="secondary" disabled={busy} onClick={check}><RefreshCw size={16} />{t('Check now')}</Button>} />
    {notice && <div className="inline-notice">{notice}</div>}
    <section className="settings-section"><header><h2>{t('Managed runtime')}</h2><Status value={form.relayIntentionalPaused ? 'paused' : 'active'} /></header><div className="form-grid"><Field label={t('Managed username')}><input value={form.managedUsername} onChange={(event) => setForm({ ...form, managedUsername: event.target.value })} /></Field><Field label={t('Relay port')}><input type="number" min={1} max={65535} value={form.relayPort} onChange={(event) => setForm({ ...form, relayPort: Number(event.target.value) })} /></Field></div></section>
    <section className="settings-section"><header><h2>{t('Library updates')}</h2></header><div className="form-grid"><Field label={t('Cron schedule')}><input value={form.updateCron} onChange={(event) => setForm({ ...form, updateCron: event.target.value })} /></Field><Field label={t('Timezone')}><input value={form.timezone} onChange={(event) => setForm({ ...form, timezone: event.target.value })} /></Field></div></section>
    <div className="page-save"><Button disabled={busy} onClick={save}><Save size={16} />{t('Save settings')}</Button></div>
  </>
}
