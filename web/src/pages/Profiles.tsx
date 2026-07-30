import { Eye, Pencil, Play, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { api, type MCPServer, type Operation, type Profile, type Skill, type Target } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

interface DiffItem { kind: string; memberId?: string; name: string; reason?: string }
interface PreflightItem { targetId: string; confirmationToken: string; expiresAt: string; result: { targetRevision: string; manifestHash: string; diff: { add: DiffItem[]; replace: DiffItem[]; delete: DiffItem[]; excluded: DiffItem[] } } }

export default function Profiles() {
  const { t } = useI18n()
  const state = useData(async () => {
    const [profiles, skills, servers, targets] = await Promise.all([api.list<Profile>('/profiles'), api.list<Skill>('/skills'), api.list<MCPServer>('/mcp/servers'), api.list<Target>('/targets')])
    return { profiles: profiles.items, skills: skills.items, servers: servers.items, targets: targets.items }
  }, [])
  const [editing, setEditing] = useState<Profile | 'new' | null>(null)
  const [applying, setApplying] = useState<Profile | null>(null)
  const [notice, setNotice] = useState('')
  if (state.loading) return <Loading />
  if (state.error || !state.data) return <ErrorNotice message={state.error} retry={state.reload} />
  const remove = (profile: Profile) => {
    if (!confirm(`${t('Delete')} ${profile.name}?`)) return
    api.delete(`/profiles/${profile.id}`).then(state.reload).catch((reason: Error) => setNotice(reason.message))
  }
  return <>
    <PageHeader title={t('Profiles')} detail={t('Unified Skill and MCP membership')} actions={<Button onClick={() => setEditing('new')}><Plus size={16} />{t('New Profile')}</Button>} />
    {notice && <div className="inline-notice">{notice}</div>}
    {state.data.profiles.length === 0 ? <Empty title={t('No Profiles')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Profile')}</th><th>{t('Revision')}</th><th>{t('Skills')}</th><th>{t('MCP servers')}</th><th>{t('Updated')}</th><th aria-label={t('Actions')} /></tr></thead><tbody>{state.data.profiles.map((profile) => <tr key={profile.id}><td><strong>{profile.name}</strong><small>{profile.description || '—'}</small></td><td>{profile.revision}</td><td>{profile.skillIds.length}</td><td>{profile.mcpServerIds.length}</td><td>{new Date(profile.updatedAt).toLocaleString()}</td><td className="row-actions"><IconButton label={t('Edit')} onClick={() => setEditing(profile)}><Pencil size={16} /></IconButton><IconButton label={t('Preflight and Apply')} onClick={() => setApplying(profile)}><Play size={16} /></IconButton><IconButton label={t('Delete')} onClick={() => remove(profile)}><Trash2 size={16} /></IconButton></td></tr>)}</tbody></table></div>}
    {editing && <ProfileEditor profile={editing === 'new' ? undefined : editing} skills={state.data.skills} servers={state.data.servers} close={() => setEditing(null)} saved={() => { setEditing(null); state.reload() }} />}
    {applying && <ProfileApply profile={applying} targets={state.data.targets} close={() => setApplying(null)} queued={(operation) => { setApplying(null); setNotice(`${t('Apply queued')} · ${operation.id.slice(0, 8)}`) }} />}
  </>
}

function ProfileEditor({ profile, skills, servers, close, saved }: { profile?: Profile; skills: Skill[]; servers: MCPServer[]; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState(profile?.name ?? '')
  const [description, setDescription] = useState(profile?.description ?? '')
  const [skillIds, setSkillIds] = useState(new Set(profile?.skillIds ?? []))
  const [serverIds, setServerIds] = useState(new Set(profile?.mcpServerIds ?? []))
  const [error, setError] = useState('')
  const toggle = (source: Set<string>, id: string, setter: (value: Set<string>) => void) => { const next = new Set(source); next.has(id) ? next.delete(id) : next.add(id); setter(next) }
  const submit = () => {
    const payload = { name, description, revision: profile?.revision ?? 0, skillIds: [...skillIds], mcpServerIds: [...serverIds] }
    const request = profile ? api.put(`/profiles/${profile.id}`, payload) : api.post('/profiles', payload)
    request.then(saved).catch((reason: Error) => setError(reason.message))
  }
  return <Modal title={profile ? `${t('Edit')} · ${profile.name}` : t('New Profile')} close={close}>{error && <ErrorNotice message={error} />}<Field label={t('Name')}><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('Description')}><input value={description} onChange={(event) => setDescription(event.target.value)} /></Field><div className="membership-grid"><Membership title={t('Skills')} items={skills.map((skill) => ({ id: skill.id, name: skill.name, detail: skill.slug }))} selected={skillIds} toggle={(id) => toggle(skillIds, id, setSkillIds)} /><Membership title={t('MCP servers')} items={servers.map((server) => ({ id: server.id, name: server.name, detail: server.transport }))} selected={serverIds} toggle={(id) => toggle(serverIds, id, setServerIds)} /></div><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button disabled={!name} onClick={submit}>{t('Save')}</Button></div></Modal>
}

function Membership({ title, items, selected, toggle }: { title: string; items: Array<{ id: string; name: string; detail: string }>; selected: Set<string>; toggle: (id: string) => void }) {
  return <section className="membership"><header><h3>{title}</h3><span>{selected.size}</span></header>{items.length === 0 ? <Empty title="None" /> : <div>{items.map((item) => <label key={item.id}><input type="checkbox" checked={selected.has(item.id)} onChange={() => toggle(item.id)} /><span><strong>{item.name}</strong><small>{item.detail}</small></span></label>)}</div>}</section>
}

function ProfileApply({ profile, targets, close, queued }: { profile: Profile; targets: Target[]; close: () => void; queued: (operation: Operation) => void }) {
  const { t } = useI18n()
  const available = targets.filter((target) => target.writable)
  const [selected, setSelected] = useState(new Set<string>())
  const [items, setItems] = useState<PreflightItem[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const toggle = (id: string) => { const next = new Set(selected); next.has(id) ? next.delete(id) : next.add(id); setSelected(next) }
  const preflight = () => { setBusy(true); setError(''); api.post<{ items: PreflightItem[] }>(`/profiles/${profile.id}/preflight`, { targetIds: [...selected] }).then((value) => setItems(value.items)).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false)) }
  const apply = () => { if (!items) return; setBusy(true); api.post<Operation>(`/profiles/${profile.id}/apply`, { confirmationTokens: items.map((item) => item.confirmationToken) }).then(queued).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false)) }
  return <Modal title={`${t('Apply Profile')} · ${profile.name}`} close={close}>{error && <ErrorNotice message={error} />}{items ? <div className="preflight-list">{items.map((item) => { const target = targets.find((candidate) => candidate.id === item.targetId); const diff = item.result.diff; return <section key={item.targetId}><header><span><strong>{target?.targetKey}</strong><small>{t('Expires')} {new Date(item.expiresAt).toLocaleTimeString()}</small></span><Status value="ready" /></header><div className="diff-summary"><span className="add">+{diff.add.length}</span><span className="replace">~{diff.replace.length}</span><span className="delete">-{diff.delete.length}</span><span>{diff.excluded.length} {t('excluded')}</span></div>{[...diff.add, ...diff.replace, ...diff.delete, ...diff.excluded].length > 0 && <ul>{diff.add.map((entry) => <li key={`a:${entry.kind}:${entry.name}`}><b>+</b>{entry.kind} / {entry.name}</li>)}{diff.replace.map((entry) => <li key={`r:${entry.kind}:${entry.name}`}><b>~</b>{entry.kind} / {entry.name}</li>)}{diff.delete.map((entry) => <li key={`d:${entry.kind}:${entry.name}`}><b>-</b>{entry.kind} / {entry.name}</li>)}{diff.excluded.map((entry) => <li key={`e:${entry.kind}:${entry.name}`}><b>×</b>{entry.kind} / {entry.name} ({entry.reason})</li>)}</ul>}</section> })}</div> : <div className="target-checklist">{available.map((target) => <label key={target.id}><input type="checkbox" checked={selected.has(target.id)} onChange={() => toggle(target.id)} /><span><strong>{target.targetKey}</strong><small>{target.nodeName} / {target.runtime}</small></span><Status value={target.health} /></label>)}</div>}<div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button>{items ? <Button disabled={busy} onClick={apply}><Play size={16} />{t('Confirm Apply')}</Button> : <Button disabled={busy || selected.size === 0} onClick={preflight}><Eye size={16} />{t('Run preflight')}</Button>}</div></Modal>
}
