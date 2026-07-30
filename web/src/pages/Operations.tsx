import { Ban, Eye, RefreshCw, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { api, type Operation } from '../api/client'
import { Button, Empty, ErrorNotice, IconButton, Loading, Modal, PageHeader, Status } from '../components/ui'
import { useData } from '../hooks/useData'
import { useI18n } from '../i18n'

export default function Operations() {
  const { t } = useI18n()
  const state = useData(() => api.list<Operation>('/operations'), [])
  const [notice, setNotice] = useState('')
  const [selected, setSelected] = useState('')
  const act = (request: Promise<unknown>, message: string) => request.then(() => { setNotice(message); state.reload() }).catch((reason: Error) => setNotice(reason.message))
  const items = state.data?.items ?? []
  return <>
    <PageHeader title={t('Operations')} detail={t('Apply, reconcile, import, restore, and relay history')} actions={<Button variant="secondary" onClick={state.reload}><RefreshCw size={16} />{t('Refresh')}</Button>} />
    {notice && <div className="inline-notice">{notice}</div>}
    {state.loading ? <Loading /> : state.error ? <ErrorNotice message={state.error} retry={state.reload} /> : items.length === 0 ? <Empty title={t('No operations')} /> : <div className="operation-list">{items.map((operation) => { const failed = operation.targets?.filter((target) => target.status === 'failed') ?? []; return <section key={operation.id}><header><div><strong>{operation.kind.replaceAll('_', ' ')}</strong><code>{operation.id.slice(0, 12)}</code></div><span>{new Date(operation.createdAt).toLocaleString()}</span><Status value={operation.status} /><div className="row-actions"><IconButton label={t('View details')} onClick={() => setSelected(operation.id)}><Eye size={16} /></IconButton>{['queued', 'running'].includes(operation.status) && <IconButton label={t('Cancel')} onClick={() => act(api.post(`/operations/${operation.id}/cancel`), t('Cancellation requested'))}><Ban size={16} /></IconButton>}{failed.length > 0 && <IconButton label={t('Retry failed targets')} onClick={() => act(api.post(`/operations/${operation.id}/retry-failed`), t('Retry queued'))}><RotateCcw size={16} /></IconButton>}</div></header>{operation.errorReason && <ErrorNotice message={operation.errorReason} />}{(operation.targets?.length ?? 0) > 0 && <div className="operation-targets">{operation.targets?.map((target) => <div key={target.id}><span><strong>{target.targetKey}</strong><small>{target.errorReason || `attempt ${target.attempt}`}</small></span><Status value={target.status} /></div>)}</div>}</section> })}</div>}
    {selected && <OperationDetail operationID={selected} close={() => setSelected('')} />}
  </>
}

function OperationDetail({ operationID, close }: { operationID: string; close: () => void }) {
  const { t } = useI18n()
  const state = useData(() => api.get<Operation>(`/operations/${operationID}`), [operationID])
  return <Modal title={`${t('Operation details')} · ${operationID.slice(0, 12)}`} close={close}>
    {state.loading ? <Loading /> : state.error || !state.data ? <ErrorNotice message={state.error} retry={state.reload} /> : <>
      <dl className="detail-list"><div><dt>{t('Kind')}</dt><dd>{state.data.kind.replaceAll('_', ' ')}</dd></div><div><dt>{t('State')}</dt><dd><Status value={state.data.status} /></dd></div><div><dt>{t('Created')}</dt><dd>{new Date(state.data.createdAt).toLocaleString()}</dd></div><div><dt>{t('Started')}</dt><dd>{state.data.startedAt ? new Date(state.data.startedAt).toLocaleString() : '—'}</dd></div><div><dt>{t('Finished')}</dt><dd>{state.data.finishedAt ? new Date(state.data.finishedAt).toLocaleString() : '—'}</dd></div></dl>
      <div className="operation-detail-targets">{state.data.targets?.map((target) => <section key={target.id}><header><span><strong>{target.targetKey}</strong><small>{t('Attempt')} {target.attempt}</small></span><Status value={target.status} /></header><dl className="detail-list"><div><dt>{t('Bridge operation')}</dt><dd><code>{target.bridgeOperationId || '—'}</code></dd></div><div><dt>{t('Salt JID')}</dt><dd><code>{target.saltJid || '—'}</code></dd></div><div><dt>{t('Started')}</dt><dd>{target.startedAt ? new Date(target.startedAt).toLocaleString() : '—'}</dd></div><div><dt>{t('Finished')}</dt><dd>{target.finishedAt ? new Date(target.finishedAt).toLocaleString() : '—'}</dd></div></dl>{target.errorReason && <ErrorNotice message={`${target.errorCode || 'operation_failed'}: ${target.errorReason}`} />}{target.result && <pre className="operation-result">{JSON.stringify(target.result, null, 2)}</pre>}</section>)}</div>
    </>}
  </Modal>
}
