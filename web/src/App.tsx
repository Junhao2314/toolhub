import { useEffect, useMemo, useState } from 'react'
import { Boxes, Bot, BriefcaseBusiness, ChevronDown, CircleUserRound, LogOut, Menu, Network, Settings, ShieldCheck, Store, Workflow, X } from 'lucide-react'
import { api, APIError, type Session } from './api/client'
import { IconButton, Loading } from './components/ui'
import Login from './pages/Login'
import Overview from './pages/Overview'
import Skills from './pages/Skills'
import Marketplace from './pages/Marketplace'
import Nodes from './pages/Nodes'
import Jobs from './pages/Jobs'
import MCP from './pages/MCP'
import Access from './pages/Access'
import SettingsPage from './pages/Settings'

const navigation = [
  { path: '/overview', label: 'Overview', icon: BriefcaseBusiness },
  { path: '/skills', label: 'Skills', icon: Boxes },
  { path: '/marketplace', label: 'Marketplace', icon: Store },
  { path: '/nodes', label: 'Nodes', icon: Network },
  { path: '/jobs', label: 'Jobs', icon: Workflow },
  { path: '/mcp', label: 'MCP', icon: Bot },
  { path: '/access', label: 'Users & Audit', icon: ShieldCheck, admin: true },
  { path: '/settings', label: 'Settings', icon: Settings, admin: true },
]

export default function App() {
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
  const visibleNav = useMemo(() => navigation.filter((item) => !item.admin || isAdmin), [isAdmin])
  const navigate = (next: string) => {
    history.pushState({}, '', next)
    setPath(next)
    setMobileNav(false)
  }

  if (checking) return <div className="boot"><div className="brand-mark"><Boxes /></div><Loading label="Opening ToolHub" /></div>
  if (!session) return <Login onLogin={setSession} />

  const page = path.startsWith('/skills') ? <Skills />
    : path.startsWith('/marketplace') ? <Marketplace />
    : path.startsWith('/nodes') ? <Nodes />
    : path.startsWith('/jobs') ? <Jobs />
    : path.startsWith('/mcp') ? <MCP />
    : path.startsWith('/access') && isAdmin ? <Access />
    : path.startsWith('/settings') && isAdmin ? <SettingsPage />
    : <Overview navigate={navigate} />

  return <div className="app-shell">
    <aside className={mobileNav ? 'sidebar open' : 'sidebar'}>
      <div className="sidebar-brand"><div className="brand-mark small"><Boxes /></div><span>ToolHub</span><IconButton label="Close navigation" onClick={() => setMobileNav(false)}><X size={18} /></IconButton></div>
      <nav>{visibleNav.map(({ path: itemPath, label, icon: Icon }) => <button key={itemPath} className={path.startsWith(itemPath) ? 'active' : ''} onClick={() => navigate(itemPath)}><Icon size={18} /><span>{label}</span></button>)}</nav>
      <div className="sidebar-foot"><span className="tailnet-dot" />Tailnet control plane</div>
    </aside>
    {mobileNav && <button className="nav-scrim" aria-label="Close navigation" onClick={() => setMobileNav(false)} />}
    <div className="workspace">
      <header className="topbar">
        <IconButton label="Open navigation" onClick={() => setMobileNav(true)}><Menu size={20} /></IconButton>
        <div className="topbar-context"><span>Operations</span><ChevronDown size={14} /></div>
        <div className="account"><CircleUserRound size={18} /><span><strong>{session.user.displayName}</strong><small>{session.user.roles.join(', ')}</small></span><IconButton label="Sign out" onClick={() => api.logout().finally(() => setSession(null))}><LogOut size={17} /></IconButton></div>
      </header>
      <main>{page}</main>
    </div>
  </div>
}
