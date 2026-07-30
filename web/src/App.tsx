import { useEffect, useState } from 'react'
import { Boxes, Bot, BriefcaseBusiness, CircleUserRound, ClipboardList, KeyRound, Layers, LogOut, Menu, MonitorCog, Settings, Store, X } from 'lucide-react'
import { api, APIError, type Session } from './api/client'
import { IconButton, Loading } from './components/ui'
import { LanguageToggle, useI18n } from './i18n'
import Account from './pages/Account'
import Login from './pages/Login'
import Marketplace from './pages/Marketplace'
import MCP from './pages/MCP'
import Operations from './pages/Operations'
import Overview from './pages/Overview'
import Profiles from './pages/Profiles'
import SettingsPage from './pages/Settings'
import Skills from './pages/Skills'
import Targets from './pages/Targets'

const navigation = [
  { path: '/overview', label: 'Overview', icon: BriefcaseBusiness },
  { path: '/skills', label: 'Skills', icon: Boxes },
  { path: '/marketplace', label: 'Marketplace', icon: Store },
  { path: '/mcp', label: 'MCP', icon: Bot },
  { path: '/profiles', label: 'Profiles', icon: Layers },
  { path: '/targets', label: 'Targets', icon: MonitorCog },
  { path: '/operations', label: 'Operations', icon: ClipboardList },
  { path: '/settings', label: 'Settings', icon: Settings },
  { path: '/account', label: 'Account', icon: CircleUserRound },
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

  const page = path.startsWith('/skills') ? <Skills />
    : path.startsWith('/marketplace') ? <Marketplace />
    : path.startsWith('/mcp') ? <MCP />
    : path.startsWith('/profiles') ? <Profiles />
    : path.startsWith('/targets') ? <Targets />
    : path.startsWith('/operations') ? <Operations />
    : path.startsWith('/settings') ? <SettingsPage />
    : path.startsWith('/account') ? <Account user={session.user} signedOut={signedOut} />
    : <Overview navigate={navigate} />

  return <div className="app-shell">
    <aside className={mobileNav ? 'sidebar open' : 'sidebar'}>
      <div className="sidebar-brand"><div className="brand-mark small"><Boxes /></div><span>ToolHub</span><IconButton label={t('Close navigation')} onClick={() => setMobileNav(false)}><X size={18} /></IconButton></div>
      <nav>{navigation.map(({ path: itemPath, label, icon: Icon }) => <button key={itemPath} className={path.startsWith(itemPath) ? 'active' : ''} onClick={() => navigate(itemPath)}><Icon size={18} /><span>{t(label)}</span></button>)}</nav>
      <div className="sidebar-foot"><span className="bridge-dot" />Bridge control plane</div>
    </aside>
    {mobileNav && <button className="nav-scrim" aria-label={t('Close navigation')} onClick={() => setMobileNav(false)} />}
    <div className="workspace">
      <header className="topbar">
        <IconButton label={t('Open navigation')} onClick={() => setMobileNav(true)}><Menu size={20} /></IconButton>
        <div className="topbar-product">ToolHub <span>/</span> {t(navigation.find((item) => path.startsWith(item.path))?.label ?? 'Overview')}</div>
        <LanguageToggle />
        <div className="account"><CircleUserRound size={18} /><strong>{session.user.username}</strong><IconButton label={t('Sign out')} onClick={() => api.logout().finally(signedOut)}><LogOut size={17} /></IconButton></div>
      </header>
      {session.user.passwordChangeRecommended && <button className="credential-reminder" onClick={() => navigate('/account')}><KeyRound size={17} /><span>{t('This account is using a temporary password. Change it from Account.')}</span></button>}
      <main>{page}</main>
    </div>
  </div>
}
