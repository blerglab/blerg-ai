import ActionError from './ActionError'
import RuntimeIcon from './RuntimeIcon'
import { useSessionFocus } from '../hooks/useSessionFocus'
import { OPEN_SESSION_FILE, revealSessionFile, type SessionFileRequest } from '../lib/openSessionFile'
import { ViewerHostContext, HostedViewers, useChatReportStore, useTranscriptStore, type ViewerHostValue } from '@blerglab/chat'
import { useSessionTabs } from '../hooks/useSessionTabs'
import SessionGroups, { type StripEnds } from './SessionGroups'
import type { TabGroup } from '../lib/sessionGroups'
import { createPortal } from 'react-dom'
import SessionTabBar from './SessionTabBar'
import { blergTransport } from '../lib/chatTransport'
import { usePaneDimensions, useViewportWidth } from '../hooks/usePaneLayout'
import SessionFailureNotice from './SessionFailureNotice'
import { PaneContext, usePaneControl } from '../panes/context'
import DesktopEnrollNotice from './DesktopEnrollNotice'
import { useShellCapability } from '../hooks/useShellCapability'
import { useProposalInbox } from '../hooks/useProposalInbox'
import { useProposalsWaiting } from '../hooks/useProposalsWaiting'
import { ConfirmPopover, endSessionText } from '@blerglab/chat'
import type { ChecklistItem } from '@blerglab/chat'
import { useParams, useNavigate } from 'react-router-dom'
import { isAutomated, startedByLabel } from '../lib/startedBy'
import { Activity, type ReactNode, useCallback, useContext, useRef, useState, useEffect, useMemo } from 'react'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useIsMobile } from '../hooks/useIsMobile'
import { usePendingToolChanges } from '../hooks/usePendingToolChanges'
import { useSessionLive } from '../hooks/useSessionLive'
import { send } from '../ws'
import { arrowSeq, KEY_SEQ, type ArrowDir } from '../terminalKeys'
import { sessionViewportBox } from '../terminalRenderer'
import TerminalDOMView, { type TerminalHandle } from './TerminalDOMView'
import SessionBoardButton from './SessionBoardButton'
import ShellWorkspace, { type ShellWorkspaceHandle } from './ShellWorkspace'
import { tokenSubject } from '../claims'
import { getAccessToken } from '../authClient'
import AgentChatView from './AgentChatView'
import { ParentDiscardAction, ChildDiscardAction } from './ChildDiscard'
import { MessageThread } from './MessageThread'
import { apiFetch } from '../apiFetch'
import { StartProgress, placeholderAttempt, describeSessionEnd, useBackgroundTasks } from '@blerglab/chat'
import CapabilitiesPanel from './CapabilitiesPanel'
import SessionInsights from './SessionInsights'
import { displayState } from '../lib/displayState'
import SessionRail from './SessionRail'
import ExplorerDock from './ExplorerDock'
import { EXPLORER_CHAT_MIN, useExplorerLayout } from '../hooks/useExplorerLayout'
import { PaletteButton } from './CommandPalette'
import { useSessionTabCommands } from '../hooks/useSessionTabCommands'
import { usePaletteCommands } from '../commands/context'
import { InsightsIcon, PanelToggleIcon, SubagentFace, StarIcon, EndSessionIcon, StopTurnIcon } from './IconButton'
import { readRailPref, runningWork, runningWorkLabel, useCompact, useThinking, writeRailPref } from '../lib/sessionRail'
import { repoLabel } from '../lib/noRepo'
import { sessionShown } from '../lib/sessionRetention'


// At work now: discarding it stops that work, and its confirmation says so.
const WORKING_STATES = new Set(['starting', 'running', 'waiting', 'background'])

// PendingStart is the start panel shown before the server has announced the
// session; its clock starts when it first shows.
function PendingStart() {
  const [since] = useState(() => Date.now())
  return <StartProgress attempt={placeholderAttempt(since)} />
}

const EMPTY_CHECKLIST_ITEMS: readonly ChecklistItem[] = []

export default function SessionDetail() {
  const { id: sessionId } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const isMobile = useIsMobile()
  const restorePanes = useContext(PaneContext)?.restore

  const session = useSessionStore(s => s.sessions.find(sess => sess.id === sessionId))
  // The page keeps what this session's views read (transcript, Files, file contents, shells) for
  // the sessions viewed most recently, so coming back draws them at once (lib/sessionRetention).
  const shownId = session?.id
  useEffect(() => shownId ? sessionShown(tokenSubject(getAccessToken()), shownId) : undefined, [shownId])
  const explorer = useExplorerLayout(!!session, sessionId)
  const { columnsRef: explorerColumnsRef } = explorer
  const shellCapability = useShellCapability(sessionId, session?.status)
  const shellAvailable = shellCapability === true
  const daemon = useSessionStore(s => s.daemons.find(d => d.id === session?.daemon_id))
  // A child's header names its parent's agent: "started by Codex agent from <parent>".
  const parentSession = useSessionStore(s => (session?.parent_session_id ? s.sessions.find(p => p.id === session.parent_session_id) : undefined))
  const pendingSessionIds = useSessionStore(s => s.pendingSessionIds)
  const failedSessions = useSessionStore(s => s.failedSessions)
  const clearFailedSession = useSessionStore(s => s.clearFailedSession)

  const actionErrorAnchor = useRef<HTMLElement | null>(null)
  const [actionError, setActionError] = useState<{ title: string; message: string; retry(): void } | null>(null)

  // Opening a session marks it as read on the server — clears the unread indicator.
  useEffect(() => {
    if (sessionId) send({ type: 'mark_session_read', session_id: sessionId })
  }, [sessionId])

  const termRef = useRef<TerminalHandle | null>(null)
  const rootRef = useRef<HTMLDivElement | null>(null)

  // Pin the mobile session container to the VISUAL viewport while the soft
  // keyboard is open. Android keeps the layout viewport full-height when the
  // keyboard opens, so with plain `inset:0` the key bar and the terminal's
  // bottom rows sit behind the keyboard. Tracking vv.height/offsetTop keeps
  // the whole flex column — header, terminal, key bar — above the keyboard,
  // and restoring inset:0 on close cannot go stale (no derived px height).
  // Styles are set imperatively to avoid re-rendering the terminal on every
  // viewport event. This effect runs AFTER TerminalDOMView's own vv listener
  // registers (child effects run first), so the terminal's kbRef guard is in
  // place before the container resize triggers its ResizeObserver.
  useEffect(() => {
    if (!isMobile) return
    const vv = window.visualViewport
    if (!vv) return
    const apply = () => {
      // Read the ref per event: the pending/error views render without it, so
      // the root may not exist yet when this effect first runs.
      const root = rootRef.current
      if (!root) return
      const box = sessionViewportBox(true, window.innerHeight, vv.height, vv.offsetTop)
      if (box) {
        root.style.top = `${box.top}px`
        root.style.height = `${box.height}px`
        root.style.bottom = 'auto'
      } else {
        root.style.top = '0'
        root.style.height = ''
        root.style.bottom = '0'
      }
    }
    vv.addEventListener('resize', apply)
    vv.addEventListener('scroll', apply)
    apply()
    return () => {
      vv.removeEventListener('resize', apply)
      vv.removeEventListener('scroll', apply)
    }
  }, [isMobile])

  const shellRef = useRef<ShellWorkspaceHandle>(null)
  useSessionFocus(rootRef, sessionId, !!session, isMobile, shellRef)
  const [activeTab, setActiveTab] = useState<'terminal' | 'chat'>('terminal')
  const allMessages = useMessageStore(s => s.messages)
  const sessionMessages = useMemo(
    () => allMessages.filter(m => m.session_id === sessionId),
    [allMessages, sessionId],
  )
  const [editing, setEditing] = useState(false)
  const [editValue, setEditValue] = useState('')
  const [saving, setSaving] = useState(false)
  // End session asks first, in a small confirmation anchored to the ✕ (ConfirmPopover).
  const [confirmEnd, setConfirmEnd] = useState(false)
  const endRef = useRef<HTMLButtonElement>(null)
  // An End on its way: the ✕ waits, so it cannot be sent twice (a second DELETE would answer 404 and
  // toast a false "may still be running").
  const [ending, setEnding] = useState(false)
  // After a failed End the focus goes back to the ✕ (enabled again), to try again.
  const refocusEnd = useRef(false)
  useEffect(() => {
    if (ending || !refocusEnd.current) return
    refocusEnd.current = false
    endRef.current?.focus()
  }, [ending])
  // Stop (interrupt the current turn) sits in the header beside End session, far from Send. It shows
  // while the session's chat says the agent works and calls the chat's interrupt. A Stop on its way
  // waits (disabled) until the turn ends, the session changes, or 10 s pass.
  const chatBusy = useChatReportStore(st => st.reports[sessionId ?? '']?.busy === true)
  const chatInterrupt = useChatReportStore(st => st.reports[sessionId ?? '']?.interrupt)
  const stopRef = useRef<HTMLButtonElement>(null)
  const [stopping, setStopping] = useState<string | null>(null)
  if (stopping !== null && (stopping !== sessionId || !chatBusy)) setStopping(null)
  // A terminal session's Skills & plugins dialog (an agent session's are the panel's Capabilities tab).
  const [showCaps, setShowCaps] = useState(false)
  const [showInsights, setShowInsights] = useState(false)
  const tabs = useSessionTabs(tokenSubject(getAccessToken()), sessionId ?? '', isMobile)
  useSessionTabCommands(tabs)
  const { takeFocusIntent, targetFor } = tabs
  const chatVisible = tabs.visible.includes('chat')
  const renderTranscript = useCallback((content: ReactNode) => <Activity mode={chatVisible ? 'visible' : 'hidden'}><div className="session-transcript-pane">{content}</div></Activity>, [chatVisible])
  // The board button reads the session's boards again when a proposal is decided (it may have given
  // a board) and when the session's state changes (a launch board is given as the session starts).
  const decidedProposals = useTranscriptStore(s => (s.sessions[sessionId ?? '']?.events ?? []).reduce((n, e) => n + (e.kind === 'proposal_decided' ? 1 : 0), 0))
  const boardsKey = `${session?.status ?? ''}|${decidedProposals}`
  const [capabilitiesRequest, setCapabilitiesRequest] = useState(0)
  const priorFileScope = useRef(sessionId)
  const priorFile = useRef(tabs.active)
  const priorGroup = useRef(tabs.focused)
  const priorTabs = useRef(tabs.tabs)
  useEffect(() => {
    if (priorFileScope.current !== sessionId) {
      priorFileScope.current = sessionId; priorFile.current = tabs.active; priorGroup.current = tabs.focused; priorTabs.current = tabs.tabs
      return
    }
    const changed = priorFile.current !== tabs.active || priorGroup.current !== tabs.focused
    const focus = changed || priorTabs.current !== tabs.tabs ? takeFocusIntent() : null
    const selectedTab = [...rootRef.current?.querySelectorAll<HTMLButtonElement>('[role="tab"][aria-selected="true"]') ?? []].find(button => button.dataset.tabId === tabs.active) ?? (isMobile ? rootRef.current?.querySelector<HTMLButtonElement>('.session-back-chat') : undefined)
    if (focus === 'tab' || focus === 'close' && tabs.active !== 'chat') selectedTab?.focus()
    else if (changed && tabs.active === 'chat' && !isMobile) (rootRef.current?.querySelector<HTMLTextAreaElement>('.agent-composer textarea') ?? selectedTab)?.focus()
    let cancelFocus = () => {}
    if (changed && tabs.active !== 'chat' && focus === 'content') {
      const target = targetFor(tabs.active)
      if (target) {
        const focusView = () => {
          const view = target.querySelector<HTMLElement>('.hosted-view:not([hidden]) [role="region"]')
          if (!view) return false
          view.focus({ preventScroll: true }); return true
        }
        if (!focusView()) {
          const observer = new MutationObserver(() => { if (focusView()) cancelFocus() })
          cancelFocus = () => { observer.disconnect(); document.removeEventListener('pointerdown', cancelFocus, true); document.removeEventListener('keydown', cancelFocus, true) }
          observer.observe(target, {childList:true,subtree:true})
          document.addEventListener('pointerdown', cancelFocus, true); document.addEventListener('keydown', cancelFocus, true)
        }
      }
    }
    priorFile.current = tabs.active
    priorGroup.current = tabs.focused
    priorTabs.current = tabs.tabs
    if (changed && isMobile && tabs.active !== 'chat' && explorer.open) explorer.close()
    return () => cancelFocus()
  }, [sessionId, tabs.active, tabs.focused, tabs.tabs, targetFor, takeFocusIntent, isMobile, explorer])

  // ─── The side panel (agent sessions) ──────────────────────────────────────
  // What needs the person and what runs, read once here: the header badge counts it, the panel
  // lists it. On a wide screen the panel is a column whose open state is the person's choice, kept
  // per browser. Until they make one it opens by itself when something runs or needs them (and
  // stays open for this session). Once they close it, it opens by itself again only when something
  // new needs them (an ask or a tool change that came after they closed it, or after the session
  // was opened), once. On a narrow screen it is a drawer, opened only from the toggle.
  const agentSession = !!session && session.kind !== 'tmux'
  const railId = useMemo(() => `session-rail-${sessionId ?? ''}`, [sessionId])
  const viewport = useViewportWidth()
  const panelDimensions = usePaneDimensions('session-panel', { min: 280, max: Math.max(280, viewport * .4), default: 340 })
  const [sessionRoom, setSessionRoom] = useState<number | null>(null)
  useEffect(() => {
    const root = rootRef.current
    if (!root) return
    const measure = () => { if (root.clientWidth > 0) setSessionRoom(root.clientWidth) }
    const observer = new ResizeObserver(measure)
    observer.observe(root); measure()
    return () => observer.disconnect()
  }, [session?.id])
  // Use available session space: list + explorer + panel can exceed a tablet viewport.
  // A drawer retains the panel preference while keeping a 320 px workbench on screen.
  const narrow = isMobile || sessionRoom !== null && sessionRoom < EXPLORER_CHAT_MIN + (explorer.open ? explorer.width : 0) + panelDimensions.width
  const [railPref, setRailPref] = useState<boolean | null>(readRailPref)
  const [autoOpenFor, setAutoOpenFor] = useState<string | null>(null)
  const [reopenFor, setReopenFor] = useState<string | null>(null)
  // Closed by choice: what needed the person then (asks up to `since`, server time; `tools` tool
  // changes) is not new.
  const [quiet, setQuiet] = useState<{ id: string; since: number; tools: number | null } | null>(null)
  const [drawerFor, setDrawerFor] = useState<string | null>(null)
  const [checklistTarget, setChecklistTarget] = useState<{session: string; id: string; n: number} | null>(null)
  if (checklistTarget && checklistTarget.session !== sessionId) setChecklistTarget(null)
  const openChecklistItem = useCallback((id: string) => {
    if (!sessionId) return
    restorePanes?.()
    writeRailPref(true)
    setRailPref(true)
    setReopenFor(null)
    if (narrow) setDrawerFor(sessionId)
    setChecklistTarget(previous => ({session: sessionId, id, n: (previous?.n ?? 0) + 1}))
  }, [sessionId, narrow, restorePanes])
  const [approvalTarget, setApprovalTarget] = useState<{session:string; n:number} | null>(null)
  const openChildApproval = useCallback(() => {
    if (!sessionId) return
    restorePanes?.()
    writeRailPref(true)
    setRailPref(true)
    setReopenFor(null)
    if (narrow) setDrawerFor(sessionId)
    setApprovalTarget(previous => ({session:sessionId, n:(previous?.n ?? 0)+1}))
  }, [sessionId, narrow, restorePanes])
  // Taken once the modal opens: the number stays (the next request is a new one), the session clears.
  const childApprovalShown = useCallback(() => setApprovalTarget(previous => previous && previous.session ? {session:'', n:previous.n} : previous), [])
  // Read again when the person opens the panel or its Capabilities tab comes on screen.
  const [toolsNonce, setToolsNonce] = useState(0)
  const toolsLook = useCallback(() => setToolsNonce(n => n + 1), [])
  // The chat's settings (Settings, in the left bar); kept per browser.
  const [showThinking] = useThinking()
  const [compactTools] = useCompact()
  // Read from the server's whole log (useSessionLive), again when the session's status changes or
  // the person opens the panel or its Capabilities tab; from the chat's loaded events on an older server.
  // The session's asks the message store holds as closed: one closed elsewhere is read again.
  const closedAsks = useMemo(() => sessionMessages.filter(m => m.kind === 'ask' && m.status !== 'open').map(m => m.id).join(','), [sessionMessages])
  const sessionLive = useSessionLive(sessionId ?? '', agentSession, `${session?.status ?? ''}|${toolsNonce}|${closedAsks}`)
  const proposalInbox = useProposalInbox()
  const sessionProposals = useMemo(() => proposalInbox.pending.filter(p => p.session_id === sessionId), [proposalInbox.pending, sessionId])
  // The list shows a proposal past its deadline as expired; Needs you does not count it.
  const sessionProposalsWaiting = useProposalsWaiting(sessionProposals, proposalInbox.clockOffset ?? 0)
  const liveState = sessionLive.live
  // The tool changes come with the live state; an older server's page reads them itself.
  const toolChangesOwn = usePendingToolChanges(sessionId ?? '', agentSession && sessionLive.source === 'client', toolsNonce)
  const toolChangesRead = sessionLive.source === 'client' ? toolChangesOwn : sessionLive.toolChanges
  const toolChanges = toolChangesRead ?? 0
  // With no saved choice the panel is open; a close is remembered, and something new reopens it once.
  const panelOpen = railPref === null ? true : railPref || reopenFor === sessionId
  const drawerOpen = drawerFor === sessionId
  const rawRailOpen = agentSession && (narrow ? drawerOpen : panelOpen)
  // A file the History tab opens: handed to the chat, whose viewer opens it. n grows with every
  // request, so the same file opens again after its viewer was closed.
  const [fileAsk, setFileAsk] = useState<{ id: string; n: number; commit?: string } | null>(null)
  const openArtifact = useCallback((id: string, commit?: string) => setFileAsk(a => ({ id, n: (a?.n ?? 0) + 1, ...(commit ? { commit } : {}) })), [])
  // The panel opened by itself for a question: a request (the session's id) that it show Live, the
  // tab the person chose staying theirs. Cleared once the panel has acted on it, and on a switch of
  // session, so coming back does not force Live again.
  const [liveAsk, setLiveAsk] = useState<string | null>(null)
  const showLive = (id: string) => setLiveAsk(id)
  const liveShown = useCallback(() => setLiveAsk(null), [])
  if (liveAsk !== null && liveAsk !== sessionId) setLiveAsk(null)
  // A background subagent is listed (and counted) once, as a subagent.
  const needsYou = liveState.openAsks.length + toolChanges + sessionProposalsWaiting.length + (sessionLive.delegationRequests?.length ?? 0) + (sessionLive.reviewRequests?.length ?? 0)
  // The panel toggle's two counts, by the selector Running now lists with (runningWork), so they
  // always match it: the work inside the session (subagents, background commands and tasks) and
  // its child sessions at work. Neither counts Main, so a turn starting never opens the panel.
  const railBg = useBackgroundTasks(sessionId ?? '')
  const work = runningWork({ subagents: liveState.subagents, serverTasks: sessionLive.tasks, bg: railBg,
    children: (sessionLive.children ?? []).filter(c => !c.archived_at), archived: !!session?.archived_at })
  const runningNow = work.inside + work.children
  if (agentSession && sessionId) {
    if (railPref === null && needsYou + runningNow > 0 && autoOpenFor !== sessionId) {
      setAutoOpenFor(sessionId)
      if (needsYou > 0) showLive(sessionId)
    }
    if (railPref === false && quiet?.id !== sessionId && liveState.heldUntil !== null) {
      setQuiet({ id: sessionId, since: liveState.heldUntil, tools: toolChangesRead })
    } else if (quiet?.id === sessionId && quiet.tools === null && toolChangesRead !== null) {
      setQuiet({ ...quiet, tools: toolChangesRead })
    }
    // The promise is a new question. The tool change count is read only when the session, the panel
    // or its Capabilities tab opens (nothing pushes a new proposal to the page), so its half rarely fires.
    const somethingNew = railPref === false && quiet?.id === sessionId && (
      liveState.openAsks.some(a => a.askedAt !== null && a.askedAt > quiet.since) ||
      (quiet.tools !== null && toolChanges > quiet.tools))
    if (somethingNew && reopenFor !== sessionId) {
      setReopenFor(sessionId)
      showLive(sessionId)
    }
  }
  const choose = (open: boolean) => {
    writeRailPref(open)
    setRailPref(open)
    setReopenFor(null)
    if (open) setToolsNonce(n => n + 1)
    else if (sessionId) setQuiet({ id: sessionId, since: Date.now() - liveState.clockOffset, tools: toolChangesRead })
  }
  const panelControl = usePaneControl('session-panel', 'Toggle session panel', { destructive: false }, rawRailOpen, open => {
    if (narrow) { setDrawerFor(open ? sessionId ?? null : null); if (open) setToolsNonce(n => n + 1) }
    else choose(open)
  }, !!agentSession, sessionId)
  const railOpen = panelControl.open
  const toggleRail = panelControl.toggle
  const closeRail = () => panelControl.setOpen(false)

  function sendKey(seq: string) {
    if (session?.archived_at) return
    if (!sessionId) return
    send({ type: 'send_input', session_id: sessionId, data: btoa(seq) })
  }

  function sendArrow(dir: ArrowDir) {
    if (session?.archived_at) return
    const app = !!termRef.current?.applicationCursorKeysMode
    sendKey(arrowSeq(dir, app))
  }

  // Blurs the focused element so Android doesn't open the soft keyboard when
  // tapping a control button (the hidden textarea stays focused between taps but
  // a touch-with-focused-textarea can trigger the keyboard without this).
  function blurKeyboard() {
    (document.activeElement as HTMLElement | null)?.blur()
  }

  const revealPanel = useCallback(() => {
    restorePanes?.()
    writeRailPref(true)
    setRailPref(true)
    if (narrow && sessionId) setDrawerFor(sessionId)
  }, [narrow, sessionId, restorePanes])

  const { host: tabHost, open: openTab } = tabs
  const { close: closeExplorer } = explorer
  const viewerHost = useMemo<ViewerHostValue>(() => ({ ...tabHost, autoFocus: false, open: (view, keep) => {
    openTab(view, keep)
    if (view.kind === 'workspace' && sessionId) revealSessionFile({ session: sessionId, account: tokenSubject(getAccessToken()), path: view.path })
    if (isMobile) { closeExplorer(); setDrawerFor(null) }
  } }), [tabHost, openTab, isMobile, closeExplorer, sessionId])
  useEffect(() => {
    const open = (event: Event) => {
      const request = (event as CustomEvent<SessionFileRequest>).detail
      if (!session || request?.session !== session.id || request.account !== tokenSubject(getAccessToken())) return
      event.preventDefault()
      viewerHost.open({ kind: 'workspace', path: request.path })
    }
    window.addEventListener(OPEN_SESSION_FILE, open)
    return () => window.removeEventListener(OPEN_SESSION_FILE, open)
  }, [session, viewerHost])
  const canPause = !!session && !session.archived_at && session.runtime === 'cluster' && ['running', 'idle', 'waiting'].includes(session.status)
  const pause = useCallback(async () => {
    actionErrorAnchor.current = rootRef.current?.querySelector<HTMLElement>('[data-testid=pause-session]') ?? endRef.current
    if (!sessionId) return
    try {
      const res = await apiFetch(`/api/sessions/${sessionId}/pause`, { method: 'POST' })
      if (!res.ok) setActionError({ title: 'Pause failed', message: `Server returned ${res.status}. The session is still running.`, retry: () => { void pause() } })
    } catch { setActionError({ title: 'Pause failed', message: 'Network error. The session is still running.', retry: () => { void pause() } }) }
  }, [sessionId])
  const paletteCommands = useMemo(() => session ? [
    { id: `star-session:${session.id}`, destructive: false, title: session.starred ? 'Unstar session' : 'Star session', run: () => send({ type: 'set_session_star', session_id: session.id, starred: !session.starred }) },
    { id: `insights-session:${session.id}`, destructive: false, title: 'Session insights', here: 7, run: () => setShowInsights(true) },
    { id: `capabilities-session:${session.id}`, destructive: false, title: 'Skills & plugins', run: () => { if (session.kind !== 'tmux') { revealPanel(); setCapabilitiesRequest(n => n + 1) } else setShowCaps(true) } },
    ...(canPause ? [{ id: `pause-session:${session.id}`, destructive: true, title: 'Pause session', run: () => { void pause() } }] : []),
    ...(!ending && !session.archived_at && ['running', 'idle', 'waiting', 'starting', 'disconnected'].includes(session.status) ? [{ id: `end-session:${session.id}`, title: 'End session…', keywords: 'End this session', destructive: true, run: () => { endRef.current?.focus(); setConfirmEnd(true) } }] : []),
  ] : [], [session, canPause, pause, revealPanel, ending])
  usePaletteCommands(paletteCommands)

  if (!session) {
    const isFailed = sessionId !== undefined && sessionId in failedSessions
    const isPending = sessionId !== undefined && sessionId in pendingSessionIds

    if (isFailed) {
      const message = failedSessions[sessionId!] || 'Session failed to start.'
      return (
        <div style={{ padding: '24px', color: 'var(--chalk)' }}>
          <div style={{ color: 'var(--danger)', marginBottom: 16 }}>{message}</div>
          <button
            onClick={() => { clearFailedSession(sessionId!); navigate('/sessions') }}
            style={{ background: 'none', border: 'none', color: 'var(--blaze)', cursor: 'pointer', fontSize: 16, fontFamily: 'inherit' }}
          >
            Back to home
          </button>
        </div>
      )
    }

    if (isPending) {
      // The start request is in flight and the server has not announced the
      // session yet (it does so the moment the row exists) — show the same
      // panel the session will continue in, not a bare "Starting…".
      return (
        <div style={{ padding: '16px', color: 'var(--chalk)' }}>
          {/* Keyed by id: each pending start gets its own clock. */}
          <PendingStart key={sessionId} />
        </div>
      )
    }

    return (
      <div style={{ padding: '24px', color: 'var(--chalk)' }}>
        <button
          onClick={() => navigate('/sessions')}
          style={{ background: 'none', border: 'none', color: 'var(--blaze)', cursor: 'pointer', fontSize: 16, marginBottom: 16, fontFamily: 'inherit' }}
        >
          ← Back
        </button>
        <div>Session not found</div>
      </div>
    )
  }

  // disconnected (cluster pod gone) still shows the kill button — the server
  // deletes the Job and marks the session stopped.
  const isActive = !session.archived_at && (session.status === 'running' || session.status === 'idle' || session.status === 'waiting' || session.status === 'starting' || session.status === 'disconnected')
  // Terminal (tmux) sessions were removed in 0.5.0: only an existing one, named so explicitly, gets
  // the terminal layout; a session whose kind is missing or unknown is an agent session.
  const isAgent = session.kind !== 'tmux'
  // The Messages tab exists for terminal sessions only (see the tab toggle below).
  const showMessages = !isAgent && activeTab === 'chat'
  const endLine = describeSessionEnd(session)

  const tabBar = (group?: TabGroup, ends?: StripEnds) => (<SessionTabBar panel={isAgent ? (<button
              type="button"
              className="icon-btn"
              ref={panelDimensions.triggerRef}
              data-testid="rail-toggle"
              aria-expanded={railOpen}
              aria-controls={railId}
              aria-label={railLabel(work.inside, work.children)}
              title={railOpen ? 'Hide the session panel' : 'Show the session panel: what needs you, what is running, this session'}
              onClick={toggleRail}
            >
              <PanelToggleIcon />
              {work.inside > 0 && <span className="icon-btn-count" data-testid="rail-badge-inside" data-glyph="🤖" aria-hidden="true"><SubagentFace className="icon-btn-count-glyph" />{work.inside}</span>}
              {work.children > 0 && <span className="icon-btn-count" data-testid="rail-badge-children" data-glyph="↳" aria-hidden="true">↳{work.children}</span>}
            </button>) : undefined} group={group} grouped={!!ends} start={ends?.start ?? true} end={ends?.end ?? true} tabs={tabs} explorer={explorer} shell={isAgent && shellAvailable && !session.archived_at ? () => shellRef.current?.open() : undefined} shellOpen={false} board={<SessionBoardButton sessionId={session.id} refreshKey={boardsKey} />} />)

  // A live cluster session can be paused: its pod is freed, the session stays and a message resumes it.
  // The state the header shows: idle to the engine with background work running (by the server's
  // count or this page's live state) reads as working in the background.
  // The live state counts only when read from the server's whole log: its running subagents and the
  // tasks not done. The panel's Status row reads the same number.
  const liveRunning = sessionLive.source === 'server'
    ? liveState.subagents.length + (sessionLive.tasks ?? []).filter(t => !t.done).length
    : null
  const shownState = displayState(session, liveRunning)
  const working = WORKING_STATES.has(shownState)
  function stopTurn() {
    if (!sessionId || !chatInterrupt || stopping === sessionId) return
    const id = sessionId
    setStopping(id)
    window.setTimeout(() => setStopping(cur => (cur === id ? null : cur)), 10_000)
    chatInterrupt().catch(() => {
      setStopping(cur => (cur === id ? null : cur))
      actionErrorAnchor.current = stopRef.current
      setActionError({ title: 'Stopping the turn failed', message: 'Not connected. The agent is still working.', retry: () => stopTurn() })
    })
  }
  async function endSession() {
    if (!sessionId || ending) return
    actionErrorAnchor.current = endRef.current
    setConfirmEnd(false)
    setEnding(true)
    let ended = false
    try {
      const res = await apiFetch(`/api/sessions/${sessionId}`, { method: 'DELETE' })
      if (res.ok) {
        ended = true
        navigate('/sessions')
      } else {
        setActionError({ title: 'Ending the session failed', message: `Server returned ${res.status}. The session may still be running.`, retry: () => { void endSession() } })
      }
    } catch {
      setActionError({ title: 'Ending the session failed', message: 'Network error. The session may still be running.', retry: () => { void endSession() } })
    } finally {
      if (!ended) {
        refocusEnd.current = true
        setEnding(false)
      }
    }
  }


  return (
    <ViewerHostContext.Provider value={viewerHost}>
    {actionError && <ActionError anchor={actionErrorAnchor} title={actionError.title} message={actionError.message} onRetry={() => { setActionError(null); actionError.retry() }} onDismiss={() => setActionError(null)} />}
    <div ref={rootRef} data-session-root style={{ display: 'flex', flexDirection: 'column', minHeight: 0, minWidth: 0, overflow: 'hidden', ...(isMobile ? { position: 'fixed', inset: 0 } : { height: '100%' }), background: 'var(--basalt)', color: 'var(--chalk)' }}>
      {/* Header */}
      <div style={{ padding: isMobile ? '12px 8px' : '12px 16px', borderBottom: '1px solid var(--stone)', display: 'flex', alignItems: 'center', gap: isMobile ? 8 : 12, background: 'var(--basalt)' }}>
        {isMobile && (
          <button
            aria-label={tabs.active === 'chat' ? 'Back to sessions' : 'Back to Chat'}
            onClick={() => tabs.active === 'chat' ? navigate('/sessions') : tabs.back()}
            style={{ background: 'none', border: 'none', color: 'var(--blaze)', cursor: 'pointer', fontSize: 18, lineHeight: 1, padding: 0, fontFamily: 'inherit' }}
          >
            ←
          </button>
        )}
        <RuntimeIcon runtime={session.runtime} machine={daemon?.name ?? session.daemon_id} />
        <div style={{ flex: 1, minWidth: 0, display: 'flex', alignItems: 'baseline', gap: 10 }}>
          {editing ? (
            <input
              value={editValue}
              onChange={e => setEditValue(e.target.value)}
              autoFocus
              disabled={saving}
              onKeyDown={async e => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  const trimmed = editValue.trim()
                  if (!trimmed) { setEditing(false); return }
                  if (trimmed === (session.title || session.repo)) { setEditing(false); return }
                  setSaving(true)
                  try {
                    await apiFetch(`/api/sessions/${sessionId}`, {
                      method: 'PATCH',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({ title: trimmed }),
                    })
                    setSaving(false)
                    setEditing(false)
                  } catch {
                    setSaving(false)
                  }
                } else if (e.key === 'Escape') {
                  setEditing(false)
                }
              }}
              onBlur={async () => {
                const trimmed = editValue.trim()
                if (!trimmed || trimmed === (session.title || session.repo)) { setEditing(false); return }
                setSaving(true)
                try {
                  await apiFetch(`/api/sessions/${sessionId}`, {
                    method: 'PATCH',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ title: trimmed }),
                  })
                  setSaving(false)
                  setEditing(false)
                } catch {
                  setSaving(false)
                }
              }}
              style={{ fontWeight: 600, fontSize: 15, color: 'var(--chalk)', background: 'transparent', border: 'none', borderBottom: '1px solid var(--blaze)', outline: 'none', width: '100%', fontFamily: 'inherit' }}
            />
          ) : (
            <div
              onClick={() => { setEditing(true); setEditValue(session.title || session.repo || '') }}
              style={{ flexShrink: 0, maxWidth: '100%', fontWeight: 600, fontSize: 15, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--chalk)', cursor: 'text' }}
            >
              {session.title || repoLabel(session.repo)}
            </div>
          )}
          <div style={{ fontSize: 12, color: 'var(--fog)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {isAutomated(session) && <span data-testid="detail-started-by">started by {startedByLabel(session, parentSession)} · </span>}
            {/* Fixed at start; an interactive session is the ordinary case and goes unmarked. */}
            {session.interaction === 'unattended' && (
              <span data-testid="detail-unattended" title="Started unattended: the agent does not wait for you in the chat">Unattended · </span>
            )}
            {daemon?.name ?? session.daemon_id} · {session.project_path}

          </div>
        </div>
        {!isMobile && !isAgent && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
            {(['up', 'down', 'left', 'right'] as const).map(dir => (
              <button
                key={dir}
                type="button"
                onClick={() => sendArrow(dir)}
                style={{
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 5,
                  color: 'var(--chalk)',
                  cursor: 'pointer',
                  fontSize: 13,
                  fontFamily: 'inherit',
                  padding: '3px 7px',
                  minWidth: 28,
                  textAlign: 'center',
                }}
              >
                {({ up: '↑', down: '↓', left: '←', right: '→' } as const)[dir]}
              </button>
            ))}
            {([
              { label: '⏎', seq: KEY_SEQ.enter },
              { label: 'esc', seq: KEY_SEQ.esc },
              { label: 'tab', seq: KEY_SEQ.tab },
            ] as const).map(({ label, seq }) => (
              <button
                key={label}
                type="button"
                onClick={() => sendKey(seq)}
                style={{
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 5,
                  color: 'var(--chalk)',
                  cursor: 'pointer',
                  fontSize: 12,
                  fontFamily: 'inherit',
                  padding: '3px 7px',
                  minWidth: 28,
                  textAlign: 'center',
                }}
              >
                {label}
              </button>
            ))}
          </div>
        )}
        {/* The header's controls, one style (IconButton): star, insights, Stop (while the agent works) and End session. Phones keep Commands and Stop;
            session actions are available from the palette. */}
        <div className="icon-btn-group" data-testid="session-header-controls">
          <PaletteButton triggerRef={isMobile ? endRef : undefined} />
          {!isMobile && <button
            type="button"
            className={session.starred ? 'icon-btn starred' : 'icon-btn'}
            data-testid="star-toggle"
            data-starred={session.starred ? 'true' : 'false'}
            aria-pressed={!!session.starred}
            aria-label={session.starred ? 'Unstar' : 'Star'}
            title={session.starred ? 'Starred: kept at the top of the list. Click to unstar' : 'Star: keep it at the top of the list'}
            onClick={() => send({ type: 'set_session_star', session_id: session.id, starred: !session.starred })}
          >
            <StarIcon filled={!!session.starred} />
          </button>}
          {/* This session's figures: time, work, subagents, tokens and cost. On a phone it is in the palette,
              to leave the title room beside the bell. */}
          {!isMobile && (
            <button
              type="button"
              className="icon-btn"
              data-testid="session-insights-toggle"
              aria-haspopup="dialog"
              aria-label="Insights"
              title="Insights: this session's time, work, tokens and cost"
              onClick={() => setShowInsights(true)}
            >
              <InsightsIcon />
            </button>
          )}
          {/* Stop, immediately left of End session (on a phone, where End session is in the palette,
              last in the row): only while the agent works. */}
          {isActive && isAgent && chatBusy && chatInterrupt && (
            <button
              ref={stopRef}
              type="button"
              className="icon-btn"
              data-testid="stop-turn"
              aria-label="Stop the agent's current turn"
              title="Stop the agent's current turn"
              aria-busy={stopping === session.id || undefined}
              disabled={stopping === session.id}
              onClick={stopTurn}
            >
              <StopTurnIcon />
            </button>
          )}
          {/* disconnected (cluster pod gone) still offers End session: the server deletes the Job and marks
              the session stopped. It asks first, in a confirmation anchored to the ✕. */}
          {isActive && !isMobile && (
            <button
              ref={endRef}
              type="button"
              className="icon-btn danger"
              data-testid="stop-session"
              aria-label="End session"
              aria-haspopup="dialog"
              aria-expanded={confirmEnd}
              aria-busy={ending || undefined}
              disabled={ending}
              title="End session: the history stays"
              onClick={() => setConfirmEnd(true)}
            >
              <EndSessionIcon />
            </button>
          )}
          {isActive && confirmEnd && (
            <ConfirmPopover anchor={endRef} title="End this session?" description={endSessionText(session.runtime, !isAgent)}
              confirmLabel="End session" onConfirm={() => void endSession()} onCancel={() => setConfirmEnd(false)} testId="end-session-confirm" />
          )}
          {!session.archived_at && (session.parent_session_id
            ? <ChildDiscardAction key={session.id} palette focusTarget={endRef} running={working} parentId={session.parent_session_id} childId={session.id} title={session.title || 'Untitled session'} />
            : <ParentDiscardAction key={session.id} palette focusTarget={endRef} running={working} sessionId={session.id} title={session.title || 'Untitled session'} onDiscarded={() => navigate('/sessions')} />)}
        </div>
        {showInsights && (
          <SessionInsights
            sessionId={session.id}
            title={session.title || repoLabel(session.repo)}
            live={isActive}
            background={shownState === 'background'}
            onClose={() => setShowInsights(false)}
          />
        )}
        {showCaps && !isAgent && (
          <CapabilitiesPanel
            report={null}
            unavailable="Not available for terminal sessions. A terminal session runs the engine's interactive CLI, which doesn't report what it loaded; start an agent session to see its skills, plugins and MCP servers here."
            onClose={() => setShowCaps(false)}
          />
        )}
        {/* What the badge counts, said to a screen reader when it changes. */}
        {isAgent && <span className="rail-announce" role="status" aria-live="polite" data-testid="rail-announce">{railLabel(work.inside, work.children)}</span>}
      </div>

      {/* Below the header: the session's column (banners, the chat or terminal) and, for an agent
          session on a wide screen, the side panel beside it. Desktop columns reserve usable
          chat space; the panel becomes a drawer when columns cannot fit. Content stays contained. */}
      <div data-testid="session-columns" style={{ position: 'relative', display: 'flex', flex: 1, minHeight: 0, minWidth: 0, overflowX: 'hidden', overflowY: 'hidden' }}>
      <div style={{ display: 'flex', flex: 1, flexDirection: 'column', minWidth: 0, minHeight: 0 }}>
      {/* One group: its strip spans the explorer and carries the toggles. A split draws a strip per
          group instead, with the toggles at the ends of the top row (SessionGroups), never a row of its own. */}
      {(isMobile || tabs.groups.length <= 1) && tabBar(tabs.groups[0])}
      <div ref={explorerColumnsRef} style={{ display: 'flex', flex: 1, minHeight: 0, minWidth: 0 }}>
      <ExplorerDock key={`${session.id}:${explorer.account}`} sessionId={session.id} layout={explorer} />
      <div className={!tabs.visible.includes('chat') ? 'session-showing-file' : undefined} data-testid="session-chat-column" style={{ display: 'flex', flexDirection: 'column', flex: 1, minWidth: isMobile ? 0 : EXPLORER_CHAT_MIN, minHeight: 0 }}>

      {/* Why a finished session ended ("Ended by you", "The process exited
          on its own", ...). A failure the error banner below already explains
          is left to the banner; a session with no recorded reason (it ended
          before the runner kept them) shows nothing new. */}
      {endLine && !endLine.coveredByError && (
        <div data-testid="end-reason" style={{ fontSize: 12, color: 'var(--fog)', padding: '4px 12px', borderBottom: '1px solid var(--stone)' }}>
          {endLine.text}
        </div>
      )}

      {/* Spawn/run failure reason + a way to clear the row once the daemon
          that owned it is gone (trial blocker 2 / R7). */}
      <SessionFailureNotice status={session.status} reason={session.error_reason} />

      {/* Tab toggle — Terminal / Messages. Terminal sessions only: an agent session shows its
          asks, updates and notes in its own conversation and is answered from its composer, so
          it has one view and no tabs. A terminal has no conversation, so the messages its
          `blerg-runner ask/update/note` sends (and the box to answer an ask) are a tab. */}
      {!isAgent && <div style={{ display: 'flex', borderBottom: '1px solid var(--stone)', background: 'var(--basalt)', flexShrink: 0 }}>
        <button
          data-testid="tab-terminal"
          onClick={() => setActiveTab('terminal')}
          style={{
            flex: 1,
            padding: '7px 16px',
            background: 'none',
            border: 'none',
            borderBottom: activeTab === 'terminal' ? '2px solid var(--selected-border)' : '2px solid transparent',
            color: activeTab === 'terminal' ? 'var(--chalk)' : 'var(--fog-dim)',
            cursor: 'pointer',
            fontSize: 13,
            fontFamily: 'inherit',
            fontWeight: activeTab === 'terminal' ? 600 : 400,
          }}
        >
          Terminal
        </button>
        <button
          data-testid="tab-chat"
          onClick={() => setActiveTab('chat')}
          style={{
            flex: 1,
            padding: '7px 16px',
            background: 'none',
            border: 'none',
            borderBottom: activeTab === 'chat' ? '2px solid var(--selected-border)' : '2px solid transparent',
            color: activeTab === 'chat' ? 'var(--chalk)' : 'var(--fog-dim)',
            cursor: 'pointer',
            fontSize: 13,
            fontFamily: 'inherit',
            fontWeight: activeTab === 'chat' ? 600 : 400,
          }}
        >
          Messages
        </button>
      </div>}

      {session.desktop_status === 'refused'
        ? <DesktopEnrollNotice refused reason={session.desktop_status_reason} />
        : session.desktop_compatibility && session.daemon_id && <DesktopEnrollNotice />}
      {/* Terminal / agent region — always mounted; hidden when the Messages tab is active */}
      <div style={{ position: 'relative', display: 'flex', flexDirection: 'column', flex: 1, overflow: 'hidden', minHeight: 0, ...(showMessages && tabs.active === 'chat' ? { display: 'none' } : {}) }}>
        {isAgent
          // Keyed by id: draft, pending turn and notices belong to one
          // session and must not carry over when the route switches sessions.
          ? <ShellWorkspace tabs={tabs} enabled={shellAvailable} capabilityPending={shellCapability === null} key={session.id + tokenSubject(getAccessToken())} ref={shellRef} sessionId={session.id} accountId={tokenSubject(getAccessToken())}><div className="session-agent-pane"><SessionGroups key={tabs.scope} tabs={tabs} bar={tabBar} /><AgentChatView transcript={renderTranscript} key={session.id} session={session} thinking={showThinking} compact={compactTools} openArtifact={fileAsk} onOpenChecklistItem={openChecklistItem} onManageChildApproval={openChildApproval} checklistItems={sessionLive.source === 'server' ? (liveState.checklist?.items ?? EMPTY_CHECKLIST_ITEMS) : undefined} mcpHold={liveState.mcpHold ?? null} canReleasePrompt={sessionLive.source === 'loading' ? undefined : liveState.canReleasePrompt === true} canAnswerApprovals={sessionLive.source === 'loading' ? null : liveState.canReleasePrompt === true} reasoningTokens={liveState.reasoningTokens === true} /></div></ShellWorkspace>
          : <><SessionGroups key={tabs.scope} tabs={tabs} bar={tabBar} />{createPortal(<TerminalDOMView sessionId={sessionId ?? ''} ref={termRef} readOnly={!!session.archived_at}/>, tabs.targetFor('chat'))}{blergTransport().files && <HostedViewers sessionId={session.id} files={blergTransport().files!} repo={session.repo} />}</>}
      </div>

      {/* Messages — only rendered when its tab is active */}
      {showMessages && tabs.active === 'chat' && (
        <div style={{ flex: 1, overflow: 'auto', minHeight: 0, background: 'var(--basalt)' }}>
          <MessageThread messages={sessionMessages} hideSessionLabel />
        </div>
      )}

      {/* Docked key row (mobile only) — terminal sessions only */}
      {isMobile && !isAgent && tabs.active === 'chat' && !session.archived_at && (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          padding: '6px 10px',
          borderTop: '1px solid var(--stone)',
          background: 'var(--basalt)',
          flexWrap: 'wrap',
        }}>
          {(['up', 'down', 'left', 'right'] as const).map(dir => (
            <button
              key={dir}
              type="button"
              onPointerDown={e => { e.preventDefault(); blurKeyboard(); sendArrow(dir) }}
              style={{
                background: 'var(--scree)',
                border: '1px solid var(--stone)',
                borderRadius: 6,
                color: 'var(--chalk)',
                cursor: 'pointer',
                fontSize: 16,
                fontFamily: 'inherit',
                padding: '6px 10px',
                minWidth: 40,
                textAlign: 'center',
              }}
            >
              {({ up: '↑', down: '↓', left: '←', right: '→' } as const)[dir]}
            </button>
          ))}
          {([
            { label: '⏎', seq: KEY_SEQ.enter },
            { label: 'esc', seq: KEY_SEQ.esc },
            { label: 'tab', seq: KEY_SEQ.tab },
          ] as const).map(({ label, seq }) => (
            <button
              key={label}
              type="button"
              onPointerDown={e => { e.preventDefault(); blurKeyboard(); sendKey(seq) }}
              style={{
                background: 'var(--scree)',
                border: '1px solid var(--stone)',
                borderRadius: 6,
                color: 'var(--chalk)',
                cursor: 'pointer',
                fontSize: 13,
                fontFamily: 'inherit',
                padding: '6px 10px',
                minWidth: 40,
                textAlign: 'center',
              }}
            >
              {label}
            </button>
          ))}
        </div>
      )}
      </div>

      </div></div>
      {isAgent && (
        <SessionRail
          key={session.id}
          onPause={canPause ? () => { void pause() } : undefined}
          capabilitiesRequest={capabilitiesRequest}
          id={railId}
          paneLayout={{ ...panelDimensions, id: railId, open: railOpen, toggle: toggleRail, close: closeRail }}
          session={session}
          machine={daemon?.name}
          live={liveState}
          tasks={sessionLive.tasks}
          childSessions={sessionLive.children}
          delegationRequests={sessionLive.delegationRequests}
          reviewRequests={sessionLive.reviewRequests}
          delegationGrants={sessionLive.delegationGrants}
          liveRunning={liveRunning}
          toolChanges={toolChanges}
          proposals={sessionProposals}
          waitingProposals={sessionProposalsWaiting}
          proposalsError={proposalInbox.error}
          onProposalsChanged={() => void proposalInbox.refresh()}
          mode={narrow ? 'drawer' : 'panel'}
          open={railOpen}
          onClose={closeRail}
          onReveal={revealPanel}
          onCapabilitiesShown={toolsLook}
          onOpenArtifact={openArtifact}
          openChildApproval={approvalTarget?.session === session.id ? approvalTarget.n : undefined}
          onChildApprovalShown={childApprovalShown}
          openChecklistItem={checklistTarget?.session === session.id ? checklistTarget : null}
          // A drawer only ever opens from the toggle: it shows the tab the person chose.
          showLive={!narrow && liveAsk === session.id}
          onLiveShown={liveShown}
        />
      )}
      </div>

    </div>
    </ViewerHostContext.Provider>
  )
}

// The toggle's name: what it opens, and what its two counts say ("Session panel: 3 subagents and
// background tasks · 4 child sessions"). What needs you is not counted here (owner rule,
// 2026-10-26): the session's own asks show in the chat and on Needs you, and a child's never
// surfaces to the person.
function railLabel(inside: number, children: number): string {
  const counts = runningWorkLabel(inside, children)
  return counts ? `Session panel: ${counts}` : 'Session panel'
}
