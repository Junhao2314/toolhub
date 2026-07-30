import { Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { api, type MCPServer } from '../api/client'
import { Button, Empty, ErrorNotice, Field, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

export default function MCP() {
  const { t } = useI18n()
  const state = useData(() => api.list<MCPServer>('/mcp/servers'), [])
  const [query, setQuery] = useState('')
  const [editing, setEditing] = useState<MCPServer | 'new' | null>(null)
  const [notice, setNotice] = useState('')
  const items = (state.data?.items ?? []).filter((server) => `${server.name} ${server.transport} ${server.description}`.toLowerCase().includes(query.toLowerCase()))
  const remove = (server: MCPServer) => {
    if (!confirm(`${t('Delete')} ${server.name}?`)) return
    api.delete(`/mcp/servers/${server.id}`).then(state.reload).catch((reason: Error) => setNotice(reason.message))
  }
  return <>
    <PageHeader title="MCP" detail={t('Server library and write-only secrets')} actions={<Button onClick={() => setEditing('new')}><Plus size={16} />{t('Add server')}</Button>} />
    {notice && <div className="inline-notice">{notice}</div>}
    <div className="toolbar"><label className="search"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('Search MCP servers')} /></label></div>
    {state.loading ? <Loading /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : items.length === 0 ? <Empty title={t('No MCP servers')} /> : <div className="table-scroll"><table><thead><tr><th>{t('Server')}</th><th>{t('Transport')}</th><th>{t('Endpoint')}</th><th>{t('Secrets')}</th><th>{t('Revision')}</th><th aria-label={t('Actions')} /></tr></thead><tbody>{items.map((server) => <tr key={server.id}><td><strong>{server.name}</strong><small>{server.description || '—'}</small></td><td><Status value={server.transport} /></td><td><code>{server.transport === 'stdio' ? server.command : server.url}</code></td><td>{[...server.envKeys, ...server.headerKeys].length ? [...server.envKeys, ...server.headerKeys].join(', ') : '—'}</td><td>{server.revision}</td><td className="row-actions"><IconButton label={t('Edit')} onClick={() => setEditing(server)}><Pencil size={16} /></IconButton><IconButton label={t('Delete')} onClick={() => remove(server)}><Trash2 size={16} /></IconButton></td></tr>)}</tbody></table></div>}
    {editing && <ServerEditor server={editing === 'new' ? undefined : editing} close={() => setEditing(null)} saved={() => { setEditing(null); state.reload() }} />}
  </>
}

function ServerEditor({ server, close, saved }: { server?: MCPServer; close: () => void; saved: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState(server?.name ?? '')
  const [description, setDescription] = useState(server?.description ?? '')
  const [transport, setTransport] = useState<MCPServer['transport']>(server?.transport ?? 'stdio')
  const [command, setCommand] = useState(server?.command ?? '')
  const [args, setArgs] = useState((server?.args ?? []).join('\n'))
  const [url, setURL] = useState(server?.url ?? '')
  const [env, setEnv] = useState<SecretRow[]>(() => secretRows(server?.envKeys ?? []))
  const [headers, setHeaders] = useState<SecretRow[]>(() => secretRows(server?.headerKeys ?? []))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = () => {
    const envValues = secretValues(env)
    const headerValues = secretValues(headers)
    if (!envValues || !headerValues) { setError(t('Secret keys must be unique')); return }
    const payload = { name, description, transport, command: transport === 'stdio' ? command : '', args: transport === 'stdio' ? args.split('\n').map((item) => item.trim()).filter(Boolean) : [], url: transport === 'stdio' ? '' : url, env: envValues, headers: headerValues }
    setBusy(true); setError('')
    const request = server ? api.put(`/mcp/servers/${server.id}`, payload) : api.post('/mcp/servers', payload)
    request.then(saved).catch((reason: Error) => setError(reason.message)).finally(() => setBusy(false))
  }
  return <Modal title={server ? `${t('Edit')} · ${server.name}` : t('Add MCP server')} close={close}>{error && <ErrorNotice message={error} />}<div className="form-grid"><Field label={t('Name')}><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t('Transport')}><select value={transport} onChange={(event) => setTransport(event.target.value as MCPServer['transport'])}><option value="stdio">stdio</option><option value="http">http</option><option value="sse">sse</option></select></Field></div><Field label={t('Description')}><input value={description} onChange={(event) => setDescription(event.target.value)} /></Field>{transport === 'stdio' ? <><Field label={t('Command')}><input value={command} onChange={(event) => setCommand(event.target.value)} /></Field><Field label={t('Arguments')}><textarea value={args} onChange={(event) => setArgs(event.target.value)} rows={3} /></Field></> : <Field label="URL"><input value={url} onChange={(event) => setURL(event.target.value)} /></Field>}<div className="form-grid"><SecretEditor title={t('Environment secrets')} rows={env} setRows={setEnv} /><SecretEditor title={t('Header secrets')} rows={headers} setRows={setHeaders} /></div><div className="modal-actions"><Button variant="secondary" onClick={close}>{t('Cancel')}</Button><Button disabled={busy || !name || (transport === 'stdio' ? !command : !url)} onClick={submit}>{t('Save')}</Button></div></Modal>
}

interface SecretRow { id: string; key: string; value: string; existing: boolean }

function secretRows(keys: string[]): SecretRow[] {
  return keys.map((key) => ({ id: crypto.randomUUID(), key, value: '', existing: true }))
}

function secretValues(rows: SecretRow[]): Record<string, string> | null {
  const result: Record<string, string> = {}
  for (const row of rows) {
    const key = row.key.trim()
    if (!key) continue
    if (Object.hasOwn(result, key)) return null
    result[key] = row.value
  }
  return result
}

function SecretEditor({ title, rows, setRows }: { title: string; rows: SecretRow[]; setRows: (rows: SecretRow[]) => void }) {
  const { t } = useI18n()
  const update = (id: string, patch: Partial<SecretRow>) => setRows(rows.map((row) => row.id === id ? { ...row, ...patch } : row))
  return <section className="secret-editor" aria-label={title}><header><h3>{title}</h3><IconButton label={`${t('Add')} ${title}`} onClick={() => setRows([...rows, { id: crypto.randomUUID(), key: '', value: '', existing: false }])}><Plus size={15} /></IconButton></header><div>{rows.map((row, index) => <div className="secret-row" key={row.id}><input aria-label={`${title} ${t('Key')} ${index + 1}`} placeholder={t('Key')} value={row.key} onChange={(event) => update(row.id, { key: event.target.value })} /><input aria-label={`${title} ${row.key || index + 1}`} type="password" autoComplete="new-password" placeholder={row.existing ? t('Unchanged') : t('Value')} value={row.value} onChange={(event) => update(row.id, { value: event.target.value })} /><IconButton label={`${t('Remove')} ${row.key || index + 1}`} onClick={() => setRows(rows.filter((candidate) => candidate.id !== row.id))}><Trash2 size={15} /></IconButton></div>)}</div></section>
}
