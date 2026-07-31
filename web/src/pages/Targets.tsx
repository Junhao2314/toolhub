import { Activity, Bot, Download, KeyRound, Monitor, Pencil, Play, RefreshCw, RotateCcw, Server, Square, UserRoundCog, Wrench } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, type Backup, type LocalMCPImportPreflight, type LocalMCPServerPreview, type MCPServer, type Node, type Operation, type Skill, type Target, type TargetDetail } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

export default function Targets() {
  const { t } = useI18n()
  const state = useData(async () => {
    const [targets, skills, servers] = await Promise.all([api.list<Target>('/targets'), api.list<Skill>('/skills'), api.list<MCPServer>('/mcp/servers')])
    return { targets: targets.items, skills: skills.items, servers: servers.items }
  }, [])
  const [selected, setSelected] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    if (state.data?.targets.length && !state.data.targets.some((target) => target.id === selected)) setSelected(state.data.targets[0].id)
  }, [state.data, selected])
  const activeTargetID = state.data?.targets.some((target) => target.id === selected) ? selected : state.data?.targets[0]?.id ?? ''
  const refreshNodes = () => api.post('/nodes/refresh').then(() => { setNotice(t('Node refresh queued')); state.reload() }).catch((reason: Error) => setNotice(reason.message))
  return <>
    <PageHeader title={t('Targets')} detail={t('Runtime inventory and pinned desired snapshots')} actions={<Button variant="secondary" onClick={refreshNodes}><RefreshCw size={16} />{t('Refresh nodes')}</Button>} />
    {notice && <div className="inline-notice">{notice}</div>}
    {state.loading ? <Loading /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : state.data.targets.length === 0 ? <Empty title={t('No Targets')} /> : <div className="targets-layout">
      <aside className="target-index">
        <div className="target-index-header">
          <span>{t('Nodes & Targets')}</span>
          <span className="count-badge">{state.data.targets.length}</span>
        </div>
        {state.data.targets.map((target) => (
          <button className={activeTargetID === target.id ? 'active' : ''} key={target.id} onClick={() => setSelected(target.id)}>
            <div className="target-item-info">
              <div className="target-item-icon">
                {target.runtime === 'shared-relay' ? <Bot size={17} /> : target.nodeKind === 'salt' ? <Server size={17} /> : <Monitor size={17} />}
              </div>
              <div className="target-item-text">
                <strong>{target.runtime === 'shared-relay' ? t('Shared MCP relay') : target.runtime}</strong>
                <small>{target.nodeName}</small>
              </div>
            </div>
            <Status value={target.health} />
          </button>
        ))}
      </aside>
      {activeTargetID && <TargetInspector key={activeTargetID} targetID={activeTargetID} skills={state.data.skills} servers={state.data.servers} refreshTargets={state.reload} operation={(message) => setNotice(message)} />}
    </div>}
  </>
}

function TargetInspector({ targetID, skills, servers, refreshTargets, operation }: { targetID: string; skills: Skill[]; servers: MCPServer[]; refreshTargets: () => void; operation: (message: string) => void }) {
  const { t } = useI18n()
  const state = useData(async () => {
    const [detail, backups] = await Promise.all([api.get<TargetDetail>(`/targets/${targetID}`), api.list<Backup>(`/targets/${targetID}/backups`)])
    return { detail, backups: backups.items }
  }, [targetID])
  const [editor, setEditor] = useState(false)
  const [restoring, setRestoring] = useState<Backup | null>(null)
  const [mcpImport, setMCPImport] = useState(false)
  const [usernameEditor, setUsernameEditor] = useState(false)
  const queue = (request: Promise<Operation>, label: string) => request.then((op) => operation(`${label} · ${op.id.slice(0, 8)}`)).catch((reason: Error) => operation(reason.message))
  if (state.loading) return <Loading />
  if (state.error || !state.data) return <ErrorNotice message={state.error} retry={state.reload} />
  const { detail, backups } = state.data
  const target = detail.target
  const members = detail.inventory.members ?? []
  const managedSkills = new Set(detail.desired?.manifest.skills.map((item) => item.slug) ?? [])
  const managedMCP = new Set(detail.desired?.manifest.mcpServers.map((item) => item.name) ?? [])
  const isManaged = (member: (typeof members)[number]) => member.kind === 'anchor'
    || (member.kind === 'skill' && managedSkills.has(member.name))
    || (member.kind === 'mcp' && managedMCP.has(member.name))
  const allowsLocalIntake = target.nodeKind === 'local' && ['claude', 'codex'].includes(target.runtime)
  return <div className="target-inspector">
    <section className="target-hero">
      <div className="hero-main">
        <div className="hero-badge">
          {target.runtime === 'shared-relay' ? <Bot size={22} /> : target.nodeKind === 'salt' ? <Server size={22} /> : <Monitor size={22} />}
        </div>
        <div>
          <div className="hero-tags">
            <span className="runtime-label">{target.nodeKind} / {target.runtime}</span>
            <Status value={target.health} />
          </div>
          <h2>{target.targetKey}</h2>
          <p className="hero-sub">{t('Node')}: <strong>{target.nodeName}</strong> · {t('User')}: <code>{target.managedUsername}</code></p>
        </div>
      </div>
      <div className="summary-actions">
        {target.nodeKind === 'salt' && <Button variant="secondary" onClick={() => setUsernameEditor(true)}><UserRoundCog size={16} />{t('Edit username')}</Button>}
        <Button variant="secondary" onClick={() => queue(api.post(`/targets/${target.id}/scan`), t('Scan queued'))}><RefreshCw size={16} />{t('Scan')}</Button>
        {allowsLocalIntake && <Button variant="secondary" onClick={() => setMCPImport(true)}><KeyRound size={16} />{t('Import MCP')}</Button>}
        {target.writable && <Button variant="primary" disabled={!detail.targetRevision} onClick={() => setEditor(true)}><Pencil size={16} />{t('Edit target')}</Button>}
      </div>
    </section>
    {target.errorReason && <ErrorNotice message={`${target.errorCode}: ${target.errorReason}`} />}
    {target.runtime === 'shared-relay' && <section className="relay-strip">
      <div>
        <Activity size={20} />
        <span>
          <strong>{detail.inventory.relay?.endpoint || `http://127.0.0.1:${detail.desired?.manifest.relayPort ?? 6276}/mcp`}</strong>
          <small>{detail.inventory.relay?.state || 'unknown'}</small>
        </span>
        <Status value={detail.inventory.relay?.intentionalPaused ? 'paused' : detail.inventory.relay?.healthy ? 'healthy' : 'blocked'} />
      </div>
      <div className="relay-actions">
        <IconButton label={t('Start')} onClick={() => queue(api.post(`/targets/${target.id}/relay/start`), t('Relay start queued'))}><Play size={16} /></IconButton>
        <IconButton label={t('Stop')} onClick={() => queue(api.post(`/targets/${target.id}/relay/stop`), t('Relay stop queued'))}><Square size={16} /></IconButton>
        <IconButton label={t('Restart')} onClick={() => queue(api.post(`/targets/${target.id}/relay/restart`), t('Relay restart queued'))}><RefreshCw size={16} /></IconButton>
        <IconButton label={t('Health check')} onClick={() => queue(api.post(`/targets/${target.id}/relay/health`), t('Health check queued'))}><Activity size={16} /></IconButton>
      </div>
    </section>}
    <dl className="target-facts">
      <div className="fact-card"><dt>{t('Desired revision')}</dt><dd><code>{target.desiredRevision || '—'}</code></dd></div>
      <div className="fact-card"><dt>{t('Snapshot source')}</dt><dd>{detail.desired?.snapshot.sourceKind || '—'}</dd></div>
      <div className="fact-card"><dt>{t('Last scan')}</dt><dd>{target.lastScannedAt ? new Date(target.lastScannedAt).toLocaleString() : '—'}</dd></div>
      <div className="fact-card"><dt>{t('Last reconcile')}</dt><dd>{target.lastReconciledAt ? new Date(target.lastReconciledAt).toLocaleString() : '—'}</dd></div>
    </dl>
    <section className="target-band">
      <header><h3>{t('Inventory')}</h3><span>{members.length}</span></header>
      {members.length === 0 ? <Empty title={t('No inventory snapshot')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Member')}</th><th>{t('Kind')}</th><th>{t('Scope')}</th><th>{t('Hash')}</th><th>{t('State')}</th><th /></tr></thead><tbody>{members.map((member) => { const managed = isManaged(member); const importable = allowsLocalIntake && member.kind === 'skill' && !member.protected && !managed && !!member.contentHash && !!detail.targetRevision; return <tr key={member.id}><td><strong>{member.name}</strong></td><td><Status value={member.kind} /></td><td>{member.scope || '—'}</td><td><code>{member.contentHash?.slice(0, 12) || '—'}</code></td><td><Status value={member.protected ? 'protected' : managed ? 'managed' : 'unmanaged'} /></td><td><div className="row-actions">{importable && <Button variant="secondary" onClick={() => queue(api.post(`/targets/${target.id}/skill-import`, { name: member.name, expectedRevision: detail.targetRevision, contentHash: member.contentHash }), t('Skill import queued'))}><Download size={15} />{t('Import Skill')}</Button>}</div></td></tr> })}</tbody></table></div>}
    </section>
    <section className="target-band">
      <header><h3>{t('Backups')}</h3><span>{backups.length}</span></header>
      {backups.length === 0 ? <Empty title={t('No backups')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Created')}</th><th>{t('Revision')}</th><th>{t('Expires')}</th><th /></tr></thead><tbody>{backups.map((backup) => <tr key={backup.id}><td>{new Date(backup.createdAt).toLocaleString()}</td><td><code>{backup.targetRevision.slice(0, 12)}</code></td><td>{new Date(backup.expiresAt).toLocaleDateString()}</td><td><div className="row-actions"><Button variant="secondary" onClick={() => setRestoring(backup)}><RotateCcw size={15} />{t('Restore')}</Button></div></td></tr>)}</tbody></table></div>}
    </section>
    {editor && <TargetEditor detail={detail} skills={skills} servers={servers} close={() => setEditor(false)} queued={(op) => { setEditor(false); operation(`${t('Edit queued')} · ${op.id.slice(0, 8)}`) }} />}
    {restoring && <RestoreDialog target={target} revision={detail.targetRevision} backup={restoring} close={() => setRestoring(null)} queued={(op) => { setRestoring(null); operation(`${t('Restore queued')} · ${op.id.slice(0, 8)}`) }} />}
    {mcpImport && <LocalMCPImportDialog target={target} close={() => setMCPImport(false)} queued={(op) => { setMCPImport(false); operation(`${t('MCP import queued')} · ${op.id.slice(0, 8)}`) }} />}
    {usernameEditor && <NodeUsernameDialog target={target} close={() => setUsernameEditor(false)} saved={(configured) => { setUsernameEditor(false); state.reload(); refreshTargets(); operation(t(configured ? 'Node username override saved' : 'Node username override cleared')) }} />}
  </div>
}

function NodeUsernameDialog({ target, close, saved }: { target: Target; close: () => void; saved: (configured: boolean) => void }) {
  const { t } = useI18n()
  const state = useData(async () => {
    const response = await api.list<Node>('/nodes')
    const node = response.items.find((item) => item.id === target.nodeId && item.kind === 'salt')
    if (!node) throw new Error(t('Salt node is no longer available'))
    return node
  }, [target.nodeId])
  return <Modal title={`${t('Managed username')} · ${target.nodeName}`} close={close}>{state.loading ? <Loading /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : <NodeUsernameForm node={state.data} effectiveUsername={target.managedUsername} close={close} saved={saved} />}</Modal>
}

function NodeUsernameForm({ node, effectiveUsername, close, saved }: { node: Node; effectiveUsername: string; close: () => void; saved: (configured: boolean) => void }) {
  const { t } = useI18n()
  const initial = node.managedUsernameOverride ?? ''
  const [username, setUsername] = useState(initial)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    const managedUsername = username.trim()
    setSubmitting(true)
    setError('')
    api.patch<void>(`/nodes/${node.id}`, { managedUsername }).then(() => saved(managedUsername !== '')).catch((reason: Error) => { setError(reason.message); setSubmitting(false) })
  }
  return <form className="modal-form" onSubmit={submit}>{error && <ErrorNotice message={error} />}<Field label={t('Node username override')}><input value={username} placeholder={effectiveUsername} maxLength={32} pattern="[a-z_][a-z0-9_-]{0,31}" autoCapitalize="none" spellCheck={false} onChange={(event) => setUsername(event.target.value)} /></Field><div className="modal-actions"><Button type="button" variant="secondary" onClick={close}>{t('Cancel')}</Button><Button type="submit" disabled={submitting || username.trim() === initial}>{t('Save')}</Button></div></form>
}

function TargetEditor({ detail, skills, servers, close, queued }: { detail: TargetDetail; skills: Skill[]; servers: MCPServer[]; close: () => void; queued: (operation: Operation) => void }) {
  const { t } = useI18n()
  const target = detail.target
  const [skillIds, setSkillIds] = useState(new Set(detail.desired?.manifest.skills.map((item) => item.skillId) ?? []))
  const [serverIds, setServerIds] = useState(new Set(detail.desired?.manifest.mcpServers.map((item) => item.serverId) ?? []))
  const [error, setError] = useState('')
  const toggle = (source: Set<string>, id: string, setter: (next: Set<string>) => void) => { const next = new Set(source); next.has(id) ? next.delete(id) : next.add(id); setter(next) }
  const allowsSkills = target.runtime !== 'shared-relay'
  const allowsMCP = target.runtime === 'shared-relay' || target.nodeKind === 'salt'
  const submit = () => api.post<Operation>(`/targets/${target.id}/edit`, { expectedRevision: detail.targetRevision, skillIds: [...skillIds], mcpServerIds: [...serverIds] }).then(queued).catch((reason: Error) => setError(reason.message))
  return <Modal title={`${t('Edit target')} · ${target.targetKey}`} close={close}>{error && <ErrorNotice message={error} />}<div className="membership-grid">{allowsSkills && <MembershipList title={t('Skills')} items={skills.map((item) => ({ id: item.id, name: item.name, detail: item.slug }))} selected={skillIds} toggle={(id) => toggle(skillIds, id, setSkillIds)} />}{allowsMCP && <MembershipList title="MCP" items={servers.map((item) => ({ id: item.id, name: item.name, detail: item.transport }))} selected={serverIds} toggle={(id) => toggle(serverIds, id, setServerIds)} />}</div><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button onClick={submit}><Wrench size={16} />{t('Apply edit')}</Button></div></Modal>
}

function MembershipList({ title, items, selected, toggle }: { title: string; items: Array<{ id: string; name: string; detail: string }>; selected: Set<string>; toggle: (id: string) => void }) {
  return <section className="membership"><header><h3>{title}</h3><span>{selected.size}</span></header><div>{items.map((item) => <label key={item.id}><input type="checkbox" checked={selected.has(item.id)} onChange={() => toggle(item.id)} /><span><strong>{item.name}</strong><small>{item.detail}</small></span></label>)}</div></section>
}

function LocalMCPImportDialog({ target, close, queued }: { target: Target; close: () => void; queued: (operation: Operation) => void }) {
  const { t } = useI18n()
  const state = useData(() => api.post<LocalMCPImportPreflight>(`/targets/${target.id}/mcp-import/preflight`), [target.id])
  const [selected, setSelected] = useState<LocalMCPServerPreview | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const submit = () => {
    if (!selected || !confirmed) return
    setSubmitting(true)
    setError('')
    api.post<Operation>('/mcp/import', { confirmationToken: selected.confirmationToken }).then(queued).catch((reason: Error) => { setError(reason.message); setSubmitting(false) })
  }
  return <Modal title={`${t('Import MCP from runtime')} · ${target.targetKey}`} close={close}>
    {state.loading ? <Loading /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : state.data.items.length === 0 ? <Empty title={t('No importable MCP servers')} /> : <div className="mcp-import-list">{state.data.items.map((item) => <label key={`${item.name}:${item.contentHash}`}><input type="radio" name="local-mcp-import" aria-label={`${t('Select')} ${item.name}`} checked={selected?.confirmationToken === item.confirmationToken} onChange={() => setSelected(item)} /><span><strong>{item.name}</strong><small>{item.transport} · <code>{item.command ? [item.command, ...item.args].join(' ') : item.url || '—'}</code></small><small>{t('Environment secrets')}: {item.envKeys.join(', ') || '—'}</small><small>{t('Header secrets')}: {item.headerKeys.join(', ') || '—'}</small></span></label>)}</div>}
    {error && <ErrorNotice message={error} />}
    {state.data && state.data.items.length > 0 && <label className="capture-confirmation"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} /><span>{t('Read and encrypt the selected secret values once')}</span></label>}
    <div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button disabled={!selected || !confirmed || submitting} onClick={submit}><Download size={16} />{submitting ? t('Importing') : t('Import selected')}</Button></div>
  </Modal>
}

function RestoreDialog({ target, revision, backup, close, queued }: { target: Target; revision: string; backup: Backup; close: () => void; queued: (operation: Operation) => void }) {
  const { t } = useI18n()
  const [error, setError] = useState('')
  const submit = () => api.post<Operation>(`/targets/${target.id}/restore`, { backupId: backup.id, expectedRevision: revision }).then(queued).catch((reason: Error) => setError(reason.message))
  return <Modal title={`${t('Restore')} · ${target.targetKey}`} close={close}>{error && <ErrorNotice message={error} />}<dl className="detail-list"><div><dt>{t('Backup')}</dt><dd><code>{backup.id}</code></dd></div><div><dt>{t('Created')}</dt><dd>{new Date(backup.createdAt).toLocaleString()}</dd></div><div><dt>{t('Target revision')}</dt><dd><code>{backup.targetRevision}</code></dd></div></dl><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button variant="danger" onClick={submit}><RotateCcw size={16} />{t('Restore')}</Button></div></Modal>
}


