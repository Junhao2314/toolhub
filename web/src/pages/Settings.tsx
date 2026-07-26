import { KeyRound, Plus, RefreshCw, Save, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { api, type Dict } from '../api/client'
import { Button, ErrorNotice, Field, Loading, Modal, PageHeader, Segments, Status } from '../components/ui'
import { useData } from '../hooks/useData'

interface SettingsData extends Dict { publicUrl: string; listenPort: number; timezone: string; localNodeName: string; inventoryIntervalHours: number; marketApiKeyConfigured: boolean; policies: { updatePolicy?: { schedule: string; timezone: string }; syncPolicy?: { schedule: string; timezone: string } } }
interface Provider { id: string; name: string; baseUrl: string; model: string; isDefault: boolean; enabled: boolean }

export default function SettingsPage() {
  const [tab, setTab] = useState('Policies')
  const [addProvider, setAddProvider] = useState(false)
  const [message, setMessage] = useState('')
  const [reconciling, setReconciling] = useState(false)
  const state = useData(async () => {
    const [settings, providers] = await Promise.all([api.get<SettingsData>('/settings'), api.list<Provider>('/settings/ai-providers')])
    return { settings, providers: providers.items }
  }, [])
  return <>
    <PageHeader title="Settings" detail="Schedules, encrypted provider credentials, and Tailnet exposure." actions={tab === 'AI Providers' ? <Button onClick={() => setAddProvider(true)}><Plus size={16} />Add provider</Button> : tab === 'Policies' ? <Button disabled={reconciling} onClick={() => { setReconciling(true); api.post('/reconcile', {}).then(() => setMessage('Skill and MCP reconciliation queued.')).catch((reason: Error) => setMessage(reason.message)).finally(() => setReconciling(false)) }}><RefreshCw size={16} />{reconciling ? 'Queuing...' : 'Reconcile now'}</Button> : undefined} />
    {message && <div className="inline-notice">{message}</div>}
    <div className="toolbar"><Segments options={['Policies', 'AI Providers', 'Security']} value={tab} onChange={setTab} /></div>
    {state.loading ? <Loading label="Loading settings" /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : tab === 'Policies' ? <Policies value={state.data.settings} saved={() => { setMessage('Schedules updated. The worker reloads policies within five minutes.'); state.reload() }} /> : tab === 'AI Providers' ? <Providers items={state.data.providers} /> : <Security value={state.data.settings} />}
    {addProvider && <ProviderModal close={() => setAddProvider(false)} saved={() => { setAddProvider(false); state.reload() }} />}
  </>
}

function Policies({ value, saved }: { value: SettingsData; saved: () => void }) {
  const [updateSchedule, setUpdate] = useState(value.policies?.updatePolicy?.schedule ?? '0 2 * * *')
  const [syncSchedule, setSync] = useState(value.policies?.syncPolicy?.schedule ?? '30 3 * * *')
  const [timezone, setTimezone] = useState(value.timezone)
  const [error, setError] = useState('')
  const submit = () => api.patch('/settings', { updateSchedule, syncSchedule, timezone }).then(saved).catch((reason: Error) => setError(reason.message))
  return <section className="settings-form"><header><h2>Global schedules</h2><p>Inventory runs on connection and every {value.inventoryIntervalHours} hours. Source, skill, and node-group overrides take precedence over these defaults.</p></header>{error && <ErrorNotice message={error} />}<div className="form-grid"><Field label="Update check"><input value={updateSchedule} onChange={(event) => setUpdate(event.target.value)} /><small>Default 02:00. Discovery only; no automatic approval.</small></Field><Field label="Skill + MCP reconcile"><input value={syncSchedule} onChange={(event) => setSync(event.target.value)} /><small>Default 03:30. Enqueues both sync pipelines.</small></Field><Field label="Timezone"><input value={timezone} onChange={(event) => setTimezone(event.target.value)} /></Field></div><Button onClick={submit}><Save size={16} />Save policies</Button></section>
}

function Providers({ items }: { items: Provider[] }) {
  return <div className="provider-list">{items.map((item) => <article key={item.id}><div className="provider-icon"><KeyRound size={19} /></div><div><strong>{item.name}</strong><span>{item.baseUrl}</span><code>{item.model}</code></div>{item.isDefault && <Status value="default" />}<Status value={item.enabled ? 'enabled' : 'disabled'} /></article>)}</div>
}

function Security({ value }: { value: SettingsData }) {
  return <section className="security-settings"><div><ShieldCheck size={22} /><span><strong>Project host · {value.localNodeName}</strong><p>This reserved local node is the default onboarding and single-node canary target after Agent inventory is available.</p></span><Status value="default" /></div><div><ShieldCheck size={22} /><span><strong>Tailnet-only listener</strong><p>Container host binding is fixed to 127.0.0.1:{value.listenPort}. Tailscale Serve should terminate HTTPS and WSS.</p></span><Status value={value.publicUrl ? 'configured' : 'needs-config'} /></div><div><KeyRound size={22} /><span><strong>Encrypted credentials</strong><p>AI keys, SSH keys, Agent task keys, and MCP environment values use authenticated encryption.</p></span><Status value="active" /></div><div><ShieldCheck size={22} /><span><strong>SkillsMP key</strong><p>Optional provider key for higher Marketplace quotas.</p></span><Status value={value.marketApiKeyConfigured ? 'configured' : 'anonymous'} /></div></section>
}

function ProviderModal({ close, saved }: { close: () => void; saved: () => void }) {
  const [name, setName] = useState('')
  const [baseUrl, setBaseURL] = useState('https://api.openai.com/v1')
  const [model, setModel] = useState('gpt-4.1-mini')
  const [apiKey, setAPIKey] = useState('')
  const [error, setError] = useState('')
  const submit = () => api.post('/settings/ai-providers', { name, baseUrl, model, apiKey, isDefault: true }).then(saved).catch((reason: Error) => setError(reason.message))
  return <Modal title="Add AI provider" close={close}>{error && <ErrorNotice message={error} />}<Field label="Name"><input value={name} onChange={(event) => setName(event.target.value)} placeholder="OpenAI" /></Field><Field label="OpenAI-compatible endpoint"><input value={baseUrl} onChange={(event) => setBaseURL(event.target.value)} /></Field><Field label="Model"><input value={model} onChange={(event) => setModel(event.target.value)} /></Field><Field label="API key"><input type="password" value={apiKey} onChange={(event) => setAPIKey(event.target.value)} /></Field><div className="modal-actions"><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={submit} disabled={!name || !baseUrl || !model || !apiKey}>Save encrypted provider</Button></div></Modal>
}
