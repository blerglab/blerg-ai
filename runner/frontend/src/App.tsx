import { useState, useEffect } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom'
import SessionList from './components/SessionList'
import UpdateBanner from './components/UpdateBanner'
import SignInBanner from './components/SignInBanner'
import { useSignInEpoch } from './signIn'
import SessionsHome from './components/SessionsHome'
import SessionDetail from './components/SessionDetail'
import LaunchSheet from './components/LaunchSheet'
import ClusterStatus from './components/ClusterStatus'
import Insights from './components/Insights'
import CronsPage from './components/CronsPage'
import Proposals, { ProposalsBadge } from './components/Proposals'
import { useProposalCountPoll } from './lib/proposals'
import BoardsList from './components/BoardsList'
import BoardView from './components/BoardView'
import TicketDetail from './components/TicketDetail'
import ArchiveView from './components/ArchiveView'
import ToastContainer from './components/ToastContainer'
import ThemeToggle from './components/ThemeToggle'
import { useCodeColoursSync } from './lib/codeColours'
import { useIsMobile } from './hooks/useIsMobile'
import SettingsDialog from './components/SettingsDialog'
import { setSettingsOpen, useSettingsOpen } from './lib/settingsDialog'
import { useLaunchSheetOpen } from './hooks/useLaunchSheetOpen'
import { prefetchLaunchData } from './lib/launchCache'
import { startActivityTracking } from './activity'
import { apiFetch } from './apiFetch'
import { coreOrigin } from './authClient'

// CORE_URL: the control-plane landing lives on core's own origin, which is
// not simply "this origin with the port dropped" — see coreOrigin() for the
// k8s (subdomain-stripping) vs. desktop (port-swapping) derivation. Shared
// verbatim with board/web and core/web so all three wordmarks agree.
const CORE_URL = coreOrigin()

// BrandHeader: the Blerg wordmark + component name, styled to match the
// control-plane landing and blerg-board's header — one designed system
// across every surface.
export function BrandHeader() {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: 10,
        padding: '9px 14px',
        borderBottom: '1px solid var(--stone)',
        background: 'var(--basalt)',
        flexShrink: 0,
      }}
    >
    <a
      href={CORE_URL}
      title="blerg control plane"
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        textDecoration: 'none',
      }}
    >
      <span
        aria-label="blerg"
        style={{
          display: 'inline-flex',
          alignItems: 'baseline',
          gap: 2,
          fontFamily: 'var(--mono)',
          fontWeight: 600,
          fontSize: 16,
          letterSpacing: '0.01em',
          color: 'var(--chalk)',
        }}
      >
        blerg
        <span
          aria-hidden="true"
          style={{ width: '0.42em', height: '0.64em', background: 'var(--blaze)', display: 'inline-block', borderRadius: 1, transform: 'translateY(0.04em)' }}
        />
      </span>
      <span style={{ width: 1, height: 14, background: 'var(--stone)' }} aria-hidden="true" />
      <span
        style={{
          fontFamily: 'var(--display)',
          fontSize: 12,
          letterSpacing: '0.14em',
          textTransform: 'uppercase',
          fontWeight: 600,
          color: 'var(--fog)',
        }}
      >
        Runner
      </span>
    </a>
      <ThemeToggle />
    </div>
  )
}

// AppHeader: hidden inside a session's immersive chat/terminal view (mobile
// and desktop both need the full viewport there) — same rule TabBar uses.
function AppHeader() {
  const location = useLocation()
  if (location.pathname.startsWith('/sessions/')) return null
  return <BrandHeader />
}

export function TabBar({ clusterConfigured = false }: { clusterConfigured?: boolean } = {}) {
  const navigate = useNavigate()
  const location = useLocation()

  // Hide the tab bar when inside a session — the back button handles navigation there,
  // and the fixed bar would otherwise overlap the reply input at the bottom.
  if (location.pathname.startsWith('/sessions/')) return null

  const isActive = (path: string) => {
    return location.pathname.startsWith(path)
  }

  return (
    <div
      style={{
        position: 'fixed',
        bottom: 0,
        left: 0,
        right: 0,
        background: 'var(--basalt)',
        borderTop: '1px solid var(--stone)',
        display: 'flex',
      }}
    >
      <button
        onClick={() => navigate('/sessions')}
        style={{
          flex: 1,
          padding: '12px',
          textAlign: 'center',
          color: isActive('/sessions') ? 'var(--chalk)' : 'var(--fog-dim)',
          fontSize: '14px',
          cursor: 'pointer',
          background: 'none',
          border: 'none',
          fontFamily: 'inherit',
          letterSpacing: '0.05em',
        }}
      >
        Sessions
      </button>
      <button
        onClick={() => navigate('/boards')}
        style={{
          flex: 1,
          padding: '12px',
          textAlign: 'center',
          color: isActive('/boards') ? 'var(--chalk)' : 'var(--fog-dim)',
          fontSize: '14px',
          cursor: 'pointer',
          background: 'none',
          border: 'none',
          fontFamily: 'inherit',
          letterSpacing: '0.05em',
        }}
      >
        Boards
      </button>
      <button
        onClick={() => navigate('/crons')}
        style={{
          flex: 1,
          padding: '12px',
          textAlign: 'center',
          color: isActive('/crons') ? 'var(--chalk)' : 'var(--fog-dim)',
          fontSize: '14px',
          cursor: 'pointer',
          background: 'none',
          border: 'none',
          fontFamily: 'inherit',
          letterSpacing: '0.05em',
        }}
      >
        Crons
      </button>
      <button
        onClick={() => navigate('/proposals')}
        style={{
          flex: 1,
          padding: '12px',
          textAlign: 'center',
          color: isActive('/proposals') ? 'var(--chalk)' : 'var(--fog-dim)',
          fontSize: '14px',
          cursor: 'pointer',
          background: 'none',
          border: 'none',
          fontFamily: 'inherit',
          letterSpacing: '0.05em',
          position: 'relative',
        }}
      >
        Proposals
        <ProposalsBadge style={{ position: 'absolute', top: 6, right: 4 }} />
      </button>
      <button
        onClick={() => navigate('/insights')}
        style={{
          flex: 1,
          padding: '12px',
          textAlign: 'center',
          color: isActive('/insights') ? 'var(--chalk)' : 'var(--fog-dim)',
          fontSize: '14px',
          cursor: 'pointer',
          background: 'none',
          border: 'none',
          fontFamily: 'inherit',
          letterSpacing: '0.05em',
        }}
      >
        Insights
      </button>
      <button
        onClick={() => setSettingsOpen(true)}
        data-testid="settings-tab"
        style={{
          flex: 1,
          padding: '12px',
          textAlign: 'center',
          color: 'var(--fog-dim)',
          fontSize: '14px',
          cursor: 'pointer',
          background: 'none',
          border: 'none',
          fontFamily: 'inherit',
          letterSpacing: '0.05em',
        }}
      >
        Settings
      </button>
      {clusterConfigured && (
        <button
          onClick={() => navigate('/cluster')}
          style={{
            flex: 1,
            padding: '12px',
            textAlign: 'center',
            color: isActive('/cluster') ? 'var(--chalk)' : 'var(--fog-dim)',
            fontSize: '14px',
            cursor: 'pointer',
            background: 'none',
            border: 'none',
            fontFamily: 'inherit',
            letterSpacing: '0.05em',
          }}
        >
          Cluster
        </button>
      )}
    </div>
  )
}

function Layout() {
  const [launchOpen, setLaunchOpen] = useLaunchSheetOpen()
  const isMobile = useIsMobile()
  // Keeps the Proposals nav badge fresh wherever the person is.
  useProposalCountPoll()
  // Cluster tab/route only make sense when cluster runtime is actually
  // configured (install/k8s) — on a desktop-only install /api/cluster/status
  // reports configured:false and the tab would just lead to a dead end.
  const [clusterConfigured, setClusterConfigured] = useState(false)
  // Asked again when the sign-in banner's quiet retry got a token (the first ask was refused).
  const signInEpoch = useSignInEpoch()

  useEffect(() => {
    apiFetch('/api/cluster/status')
      .then(r => r.json())
      .then(data => setClusterConfigured(Boolean(data?.configured)))
      .catch(() => setClusterConfigured(false))
  }, [signInEpoch])

  // Once the app has settled, warm what the New session sheet opens on, so
  // even the first "+ New" of this tab opens at once (lib/launchCache). Only
  // for a signed-in page; prefetchLaunchData checks.
  useEffect(() => {
    if (typeof window.requestIdleCallback === 'function') {
      const id = window.requestIdleCallback(() => prefetchLaunchData(), { timeout: 5000 })
      return () => window.cancelIdleCallback(id)
    }
    const t = setTimeout(prefetchLaunchData, 2000)
    return () => clearTimeout(t)
  }, [])

  const settingsOpen = useSettingsOpen()
  const settingsDialog = settingsOpen ? <SettingsDialog onClose={() => setSettingsOpen(false)} /> : null

  const routes = (
    <Routes>
      <Route path="/" element={<Navigate to="/sessions" replace />} />
      {/* On a phone /sessions IS the list. Beside the sidebar it would be a second copy of it, so the
          wide layout shows a prompt instead. */}
      <Route
        path="/sessions"
        element={isMobile
          ? <SessionList onNewSession={() => setLaunchOpen(true)} />
          : <SessionsHome onNewSession={() => setLaunchOpen(true)} />}
      />
      <Route path="/sessions/:id" element={<SessionDetail />} />
      <Route path="/boards" element={<BoardsList />} />
      <Route path="/boards/:id" element={<BoardView />} />
      <Route path="/boards/:id/ticket/:ticketId" element={<TicketDetail />} />
      <Route path="/boards/:id/archive" element={<ArchiveView />} />
      <Route path="/cluster" element={<ClusterStatus />} />
      <Route path="/crons" element={<CronsPage />} />
      <Route path="/proposals" element={<Proposals />} />
      <Route path="/insights" element={<Insights />} />
    </Routes>
  )

  if (isMobile) {
    return (
      <div
        style={{
          minHeight: '100vh',
          background: 'var(--basalt)',
          color: 'var(--chalk)',
          paddingBottom: '60px',
        }}
      >
        <SignInBanner />
        <UpdateBanner />
        <AppHeader />
        {routes}
        <TabBar clusterConfigured={clusterConfigured} />
        <LaunchSheet key={launchOpen ? 'open' : 'closed'} open={launchOpen} onClose={() => setLaunchOpen(false)} />
        {settingsDialog}
        <ToastContainer />
      </div>
    )
  }

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100vh',
        background: 'var(--basalt)',
        color: 'var(--chalk)',
        overflow: 'hidden',
      }}
    >
      <SignInBanner />
      <UpdateBanner />
      <AppHeader />
      <div style={{ display: 'flex', flexDirection: 'row', flex: 1, minHeight: 0 }}>
        <SessionList variant="sidebar" onNewSession={() => setLaunchOpen(true)} />
        <div style={{ flex: 1, minWidth: 0, overflow: 'auto' }}>
          {routes}
        </div>
      </div>
      <LaunchSheet key={launchOpen ? 'open' : 'closed'} open={launchOpen} onClose={() => setLaunchOpen(false)} />
      {settingsDialog}
      <ToastContainer />
    </div>
  )
}

function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = '='.repeat((4 - base64String.length % 4) % 4)
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
  const rawData = window.atob(base64)
  return Uint8Array.from([...rawData].map(c => c.charCodeAt(0)))
}

// enableNotifications is the ONLY place that ever calls
// Notification.requestPermission() — a user-initiated action from
// NotificationsButton's click handler, never from a mount effect. Browsers
// increasingly ignore (or actively penalize) permission prompts fired
// without a user gesture, and asking on every page load regardless of the
// visitor's prior answer is exactly that anti-pattern.
async function enableNotifications() {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) return
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') return
  try { localStorage.setItem('blerg_notifications_wanted', '1') } catch { /* storage unavailable: the opt-in just isn't remembered */ }
  const registration = await navigator.serviceWorker.register('/sw.js')
  const { publicKey } = await (await apiFetch('/api/push/vapid-public-key')).json()
  if (!publicKey) return
  const subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(publicKey).buffer as ArrayBuffer })
  await apiFetch('/api/push/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(subscription) })
}

// NotificationsButton reflects the CURRENT Notification.permission on mount
// (granted/denied/default) rather than assuming "off" — so a visitor who
// already granted or denied permission in a previous session isn't asked
// again, and a "denied" visitor sees no button to re-trigger a prompt the
// browser would silently refuse anyway.
export function NotificationsButton() {
  const [state, setState] = useState<'idle' | 'on' | 'off'>(() =>
    typeof Notification !== 'undefined' && Notification.permission === 'granted' ? 'on' : 'idle')
  if (state === 'on') return null
  if (typeof Notification !== 'undefined' && Notification.permission === 'denied') return null
  // A small bell beside "+ New" in the sessions header; gone once notifications are on.
  return (
    <button type="button" aria-label="Enable notifications" title="Enable notifications"
      onClick={() => enableNotifications().then(() => setState(Notification.permission === 'granted' ? 'on' : 'off')).catch(console.error)}
      style={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 30, height: 30, color: 'var(--fog)', background: 'none', border: '1px solid var(--stone)', borderRadius: 6, padding: 0, cursor: 'pointer' }}>
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" />
        <path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" />
      </svg>
    </button>
  )
}

function App() {
  useCodeColoursSync()
  useEffect(() => {
    startActivityTracking()
  }, [])

  // Silent re-subscribe only: if this visitor previously opted in (granted
  // permission AND asked to be remembered) but the browser's push
  // subscription didn't survive (e.g. cleared site data), quietly restore
  // it — never re-prompt for permission here.
  useEffect(() => {
    let wanted = false
    try { wanted = localStorage.getItem('blerg_notifications_wanted') === '1' } catch { /* storage unavailable: treat as not opted in */ }
    if (wanted && typeof Notification !== 'undefined' && Notification.permission === 'granted') {
      enableNotifications().catch(console.error)
    }
  }, [])

  return (
    <BrowserRouter>
      <Layout />
    </BrowserRouter>
  )
}

export default App
