import { useEffect, useMemo, useState } from 'react'
import { Boxes, Bot, BriefcaseBusiness, ChevronDown, CircleUserRound, KeyRound, Layers, LogOut, Menu, MonitorCog, Network, Settings, ShieldCheck, Store, Workflow, X } from 'lucide-react'
import { api, APIError, type Session } from './api/client'
import { IconButton, Loading } from './components/ui'
import { LanguageToggle, useI18n } from './i18n'
import Login from './pages/Login'
import Overview from './pages/Overview'
import Skills from './pages/Skills'
import Marketplace from './pages/Marketplace'
import Nodes from './pages/Nodes'
import Jobs from './pages/Jobs'
import MCP from './pages/MCP'
import Access from './pages/Access'
import SettingsPage from './pages/Settings'
import Account from './pages/Account'
import Profiles from './pages/Profiles'
import Targets from './pages/Targets'

const navigation = [
  { path: '/overview', label: 'Overview', icon: BriefcaseBusiness },
  { path: '/skills', label: 'Skills', icon: Boxes },
  { path: '/marketplace', label: 'Marketplace', icon: Store },
  { path: '/nodes', label: 'Nodes', icon: Network },
  { path: '/jobs', label: 'Jobs', icon: Workflow },
  { path: '/mcp', label: 'MCP', icon: Bot },
  { path: '/profiles', label: 'Profiles', icon: Layers },
  { path: '/targets', label: 'Runtime View', icon: MonitorCog },
  { path: '/account', label: 'Account', icon: CircleUserRound },
  { path: '/access', label: 'Users & Audit', icon: ShieldCheck, admin: true },
  { path: '/settings', label: 'Settings', icon: Settings, admin: true },
]

export default function App() {
  const { t } = useI18n()
  const [session, setSession] = useState<Session | null>(null)
  const [checking, setChecking] = useState(true)
  const [path, setPath] = useState(location.pathname === '/' ? '/overview' : location.pathname)
  const [mobileNav, setMobileNav] = useState(false)

  useEffect(() => {
    api.bootstrap().then(setSession).catch((error) => {
      if (!(error instanceof APIError) || error.status !== 401) console.error(error)
    }).finally(() => setChecking(false))
  }, [])
  useEffect(() => {
    const onPopState = () => setPath(location.pathname)
    addEventListener('popstate', onPopState)
    return () => removeEventListener('popstate', onPopState)
  }, [])

  const isAdmin = session?.user.roles.includes('admin') ?? false
  const canOperate = isAdmin || (session?.user.roles.includes('operator') ?? false)
  const visibleNav = useMemo(() => navigation.filter((item) => !item.admin || isAdmin), [isAdmin])
  const navigate = (next: string) => {
    history.pushState({}, '', next)
    setPath(next)
    setMobileNav(false)
  }
  const signedOut = () => {
    api.forgetSession()
    setSession(null)
    history.replaceState({}, '', '/')
    setPath('/overview')
  }

  if (checking) return <div className="boot"><div className="brand-mark"><Boxes /></div><Loading label={t('Opening ToolHub')} /></div>
  if (!session) return <Login onLogin={setSession} />

  const page = path.startsWith('/skills') ? <Skills canAdopt={isAdmin} />
    : path.startsWith('/marketplace') ? <Marketplace />
    : path.startsWith('/nodes') ? <Nodes />
    : path.startsWith('/jobs') ? <Jobs />
    : path.startsWith('/mcp') ? <MCP />
    : path.startsWith('/profiles') ? <Profiles canOperate={canOperate} />
    : path.startsWith('/targets') ? <Targets canOperate={canOperate} />
    : path.startsWith('/account') ? <Account user={session.user} signedOut={signedOut} />
    : path.startsWith('/access') && isAdmin ? <Access currentUserID={session.user.id} sessionInvalidated={signedOut} />
    : path.startsWith('/settings') && isAdmin ? <SettingsPage />
    : <Overview navigate={navigate} />

  return <div className="app-shell">
    <aside className={mobileNav ? 'sidebar open' : 'sidebar'}>
      <div className="sidebar-brand"><div className="brand-mark small"><Boxes /></div><span>ToolHub</span><IconButton label={t('Close navigation')} onClick={() => setMobileNav(false)}><X size={18} /></IconButton></div>
      <nav>{visibleNav.map(({ path: itemPath, label, icon: Icon }) => <button key={itemPath} className={path.startsWith(itemPath) ? 'active' : ''} onClick={() => navigate(itemPath)}><Icon size={18} /><span>{t(label)}</span></button>)}</nav>
      <div className="sidebar-foot"><span className="tailnet-dot" />{t('Tailnet control plane')}</div>
    </aside>
    {mobileNav && <button className="nav-scrim" aria-label={t('Close navigation')} onClick={() => setMobileNav(false)} />}
    <div className="workspace">
      <header className="topbar">
        <IconButton label={t('Open navigation')} onClick={() => setMobileNav(true)}><Menu size={20} /></IconButton>
        <div className="topbar-context"><span>{t('Operations')}</span><ChevronDown size={14} /></div>
        <LanguageToggle />
        <div className="account"><CircleUserRound size={18} /><span><strong>{session.user.displayName}</strong><small>@{session.user.username} · {session.user.roles.join(', ')}</small></span><IconButton label={t('Sign out')} onClick={() => api.logout().finally(signedOut)}><LogOut size={17} /></IconButton></div>
      </header>
      {session.user.passwordChangeRecommended && <button className="credential-reminder" onClick={() => navigate('/account')}><KeyRound size={17} /><span>{t('This account is using a temporary password. Change it from Account.')}</span></button>}
      <main>{page}</main>
    </div>
  </div>
}
