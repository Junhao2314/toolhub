import type { ButtonHTMLAttributes, ReactNode } from 'react'
import { AlertTriangle, LoaderCircle, X } from 'lucide-react'
import { useI18n } from '../i18n'

export function Button({ children, variant = 'primary', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'danger' | 'ghost' }) {
  return <button className={`button ${variant}`} {...props}>{children}</button>
}

export function IconButton({ label, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; children: ReactNode }) {
  return <button className="icon-button" title={label} aria-label={label} {...props}>{children}</button>
}

export function Status({ value }: { value: string }) {
  const { t } = useI18n()
  const normalized = value.toLowerCase().replaceAll('_', '-')
  return <span className={`status ${normalized}`}><i />{t(value.replaceAll('_', ' '))}</span>
}

export function Loading({ label }: { label?: string }) {
  const { t } = useI18n()
  return <div className="loading"><LoaderCircle size={18} className="spin" />{label ?? t('Loading')}</div>
}

export function Empty({ title, detail, action }: { title: string; detail?: string; action?: ReactNode }) {
	return <div className="empty"><div className="empty-mark" /><strong>{title}</strong>{detail && <span>{detail}</span>}{action}</div>
}

export function ErrorNotice({ message, retry }: { message: string; retry?: () => void }) {
  const { t } = useI18n()
  return <div className="error-notice"><AlertTriangle size={17} /><span>{message}</span>{retry && <Button variant="ghost" onClick={retry}>{t('Retry')}</Button>}</div>
}

export function Modal({ title, children, close }: { title: string; children: ReactNode; close: () => void }) {
  const { t } = useI18n()
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && close()}>
    <section className="modal" role="dialog" aria-modal="true" aria-label={title}>
      <header><h2>{title}</h2><IconButton label={t('Close')} onClick={close}><X size={18} /></IconButton></header>
      <div className="modal-body">{children}</div>
    </section>
  </div>
}

export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return <label className="field"><span>{label}</span>{children}{hint && <small>{hint}</small>}</label>
}

export function PageHeader({ title, detail, actions }: { title: string; detail?: string; actions?: ReactNode }) {
	return <header className="page-header"><div><h1>{title}</h1>{detail && <p>{detail}</p>}</div>{actions && <div className="page-actions">{actions}</div>}</header>
}

export function Segments({ options, value, onChange }: { options: string[]; value: string; onChange: (value: string) => void }) {
  const { t } = useI18n()
  return <div className="segments">{options.map((option) => <button key={option} className={value === option ? 'active' : ''} onClick={() => onChange(option)}>{t(option)}</button>)}</div>
}
