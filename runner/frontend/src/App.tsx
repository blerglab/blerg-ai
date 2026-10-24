import { useProposalExpiryWarnings } from './hooks/useProposalExpiryWarnings'
import { useProposalInbox } from './hooks/useProposalInbox'
import { enableNotifications } from './lib/push'
import { useState, useEffect, useCallback } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom'
import SessionList from './components/SessionList'
import CommandPaletteProvider, { PaletteButton } from './components/CommandPalette'
import RunnerCommands from './components/RunnerCommands'
import UpdateBanner from './components/UpdateBanner'
import SignInBanner from './components/SignInBanner'
import { useSignInEpoch } from './signIn'
import SessionsHome from './components/SessionsHome'
import SessionDetail from './components/SessionDetail'
import DesktopScopeBoundary from './components/DesktopScopeBoundary'
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
      <div className="icon-btn-group"><PaletteButton /><ThemeToggle /></div>
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
  const location = useLocation()
  const [launchOpen, setLaunchOpen] = useLaunchSheetOpen()
  const openLaunch = useCallback(() => setLaunchOpen(true), [setLaunchOpen])
  const isMobile = useIsMobile()
  // Keeps the Proposals nav badge fresh wherever the person is.
  useProposalCountPoll()
  // The app's one Notifications bell (in the sessions header, beside + New) reads this inbox, and
  // the warning that a proposal expires soon comes from here, on every page.
  const proposalInbox = useProposalInbox()
  useProposalExpiryWarnings(proposalInbox)
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
          ? <SessionList onNewSession={() => setLaunchOpen(true)} proposalInbox={proposalInbox} />
          : <SessionsHome onNewSession={() => setLaunchOpen(true)} />}
      />
      <Route path="/sessions/:id" element={<DesktopScopeBoundary><SessionDetail /></DesktopScopeBoundary>} />
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
        <RunnerCommands key={location.pathname} onNewSession={openLaunch} />
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
      <RunnerCommands key={location.pathname} onNewSession={openLaunch} />
      <SignInBanner />
      <UpdateBanner />
      <AppHeader />
      <div style={{ display: 'flex', flexDirection: 'row', flex: 1, minHeight: 0 }}>
        <SessionList variant="sidebar" onNewSession={() => setLaunchOpen(true)} proposalInbox={proposalInbox} />
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
      <CommandPaletteProvider><Layout /></CommandPaletteProvider>
    </BrowserRouter>
  )
}

export default App
