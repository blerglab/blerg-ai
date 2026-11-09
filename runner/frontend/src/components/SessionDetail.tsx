import { rootSessionId } from '../lib/sessionTree'
import { useSessionRoute } from '../hooks/useSessionRoute'
import WorkspaceLayouts from './WorkspaceLayouts'
import NeedsYouTabs from './NeedsYouTabs'
import { useDecisionAttention } from '../hooks/useSessionAttention'
import { useQuestionItems } from '../hooks/useQuestionItems'
import { QuestionThreadsProvider } from '@blerglab/chat'
import { questionThreads } from '../lib/questionThreads'
import QuestionTabs from './QuestionTabs'
import InboxTabs from './Inbox'
import { openInbox } from '../hooks/useInbox'
import BlackboardTabs, { BlackboardButton, BlackboardMenuItem } from './BlackboardTabs'
import InsightsTabs from './InsightsTabs'
import { ChannelPanelsContext } from '../lib/channelPanels'
import RepositoryGraphTabs from './RepositoryGraphTabs'
import { GraphTabContext } from '../lib/graphTabContext'
import SessionBoardTabs from './SessionBoardTabs'
import { SessionColorPicker } from './SessionColor'
import { sessionRepos } from '../lib/sessionRepos'
import ActionError from './ActionError'
import RuntimeIcon from './RuntimeIcon'
import { useSessionFocus } from '../hooks/useSessionFocus'
import { UI_REVEAL_APPLY, validReveal } from '../lib/uiReveal'
import { OPEN_SESSION_FILE, revealSessionFile, type SessionFileRequest } from '../lib/openSessionFile'
import { mark, markWhenPainted } from '../lib/releasePerfMarks'
import { ViewerHostContext, HostedViewers, useChatReportStore, useTranscriptStore, type ViewerHostValue } from '@blerglab/chat'
import { sessionTabLabels } from '../lib/sessionTabLabels'
import { useSessionTabs } from '../hooks/useSessionTabs'
import SessionGroups, { type StripEnds } from './SessionGroups'
import type { TabGroup } from '../lib/sessionGroups'
import { createPortal } from 'react-dom'
import SessionTabBar, { ShellToggleButton } from './SessionTabBar'
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
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { isAutomated, startedByLabel } from '../lib/startedBy'
import { Activity, type ReactNode, useCallback, useContext, useRef, useState, useEffect, useMemo } from 'react'
import { sessionNoun } from '../lib/sessionPresentation'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useIsMobile } from '../hooks/useIsMobile'
import { usePendingToolChanges } from '../hooks/usePendingToolChanges'
import { useSessionLive } from '../hooks/useSessionLive'
import { send } from '../ws'
import { sessionViewportBox } from '../terminalRenderer'
import TerminalHistory from './TerminalHistory'
import SessionBoardButton, { SessionBoardMenuItem } from './SessionBoardButton'
import ShellWorkspace, { type ShellWorkspaceHandle } from './ShellWorkspace'
import { tokenSubject } from '../claims'
import { getAccessToken } from '../authClient'
import AgentChatView from './AgentChatView'
import { ParentDiscardAction, ChildDiscardAction } from './ChildDiscard'
import ReleaseStorage from './ReleaseStorage'
import { MessageThread } from './MessageThread'
import { apiFetch } from '../apiFetch'
import { StartProgress, placeholderAttempt, describeSessionEnd, useBackgroundTasks } from '@blerglab/chat'
import { displayState } from '../lib/displayState'
import SessionRail from './SessionRail'
import SessionChatTabs from './SessionChatTabs'
import ExplorerDock from './ExplorerDock'
import { OpenReviewContext } from '../lib/openReview'
import { EXPLORER_CHAT_MIN, useExplorerLayout } from '../hooks/useExplorerLayout'
import { PaletteButton, PaletteMenuItem } from './CommandPalette'
import { useSessionTabCommands } from '../hooks/useSessionTabCommands'
import { usePaletteCommands } from '../commands/context'
import { InsightsIcon, PanelToggleIcon, SubagentFace, StarIcon, EndSessionIcon, StopTurnIcon, OpenIcon, ShellIcon, IconMenu, IconMenuItem } from './IconButton'
import { readRailPref, runningWork, runningWorkLabel, useCompact, useThinking } from '../lib/sessionRail'
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
  const { id: routeId } = useParams<{ id: string }>()
  const sessionId = useSessionStore(s => rootSessionId(s.sessions, routeId))
  const routeSession = useSessionStore(s => s.sessions.find(session => session.id === routeId))
  const navigate = useNavigate()
  const location = useLocation()
  const isMobile = useIsMobile()
  const restorePanes = useContext(PaneContext)?.restore

  const session = useSessionStore(s => s.sessions.find(sess => sess.id === sessionId))
  const noun = session ? sessionNoun(session) : 'workspace'
  const titledNoun = noun[0].toUpperCase() + noun.slice(1)
  const historyRequest = session && new URLSearchParams(location.search).get('panel') === 'history' ? sessionId : undefined
  // The page keeps what this session's views read (transcript, Files, file contents, shells) for
  // the sessions viewed most recently, so coming back draws them at once (lib/sessionRetention).
  const shownId = session?.id
  useEffect(() => shownId ? sessionShown(tokenSubject(getAccessToken()), shownId) : undefined, [shownId])
  // BLG-453: a no-op everywhere except the release-perf harness.
  // workspace-switched fires once the session itself resolves — that is
  // what "switched" means, regardless of what its transcript shows yet.
  // transcript-rendered used to fire from this same signal, which marked
  // "the session resolved," not "its transcript rendered": a Codex review
  // of d7e50390 found this reported transcript-open and cold-load's
  // readiness far too early, before any events had actually painted.
  // markWhenPainted (below, keyed on transcriptPaneRef) now gates it on
  // the real DOM: a [data-event-id] row actually present, one frame after.
  useEffect(() => {
    if (!shownId) return
    mark('workspace-switched')
  }, [shownId])
  useEffect(() => {
    if (!shownId) return
    return markWhenPainted('transcript-rendered', transcriptPaneRef.current, '[data-event-id]')
  }, [shownId])
  const explorer = useExplorerLayout(!!session, sessionId)
  const { columnsRef: explorerColumnsRef } = explorer
  const shellCapability = useShellCapability(sessionId, session?.status)
  const shellAvailable = shellCapability === true
  const daemon = useSessionStore(s => s.daemons.find(d => d.id === session?.daemon_id))
  const pendingSessionIds = useSessionStore(s => s.pendingSessionIds)
  const failedSessions = useSessionStore(s => s.failedSessions)
  const clearFailedSession = useSessionStore(s => s.clearFailedSession)

  const actionErrorAnchor = useRef<HTMLElement | null>(null)
  const [actionError, setActionError] = useState<{ title: string; message: string; retry(): void } | null>(null)

  const rootRef = useRef<HTMLDivElement | null>(null)
  const transcriptPaneRef = useRef<HTMLDivElement | null>(null)

  // Keep the mobile session above the soft keyboard.
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
  const routerHistory = useMemo(() => ({ back: () => {
    if (!(location.state?.workspaceTab?.depth > 0)) return false
    navigate(-1)
    return true
  } }), [navigate, location.state])
  const tabs = useSessionTabs(tokenSubject(getAccessToken()), sessionId ?? '', isMobile, sessionTabLabels, routerHistory)
  useSessionRoute(tabs, routeSession)
  const focusedView = tabs.tabs.find(tab => tab.id === tabs.active)?.view
  const headerId = focusedView?.kind === 'session-chat' ? focusedView.sessionId : sessionId
  const selectedSession = useSessionStore(s => s.sessions.find(session => session.id === headerId)) ?? session
  const headerDaemon = useSessionStore(s => s.daemons.find(daemon => daemon.id === selectedSession?.daemon_id))
  const headerParent = useSessionStore(s => s.sessions.find(session => session.id === selectedSession?.parent_session_id))
  const chatBusy = useChatReportStore(st => st.reports[headerId ?? '']?.busy === true)
  const chatInterrupt = useChatReportStore(st => st.reports[headerId ?? '']?.interrupt)
  const stopRef = useRef<HTMLButtonElement>(null)
  const [stopping, setStopping] = useState<string | null>(null)
  if (stopping !== null && (stopping !== headerId || !chatBusy || !!selectedSession?.paused_reason)) setStopping(null)
  const [headerOwner, setHeaderOwner] = useState(headerId)
  if (headerOwner !== headerId) { setHeaderOwner(headerId); setEditing(false); setConfirmEnd(false) }
  useSessionTabCommands(tabs)
  const { takeFocusIntent, targetFor, open: openSessionTab } = tabs
  const channelPanels = useMemo(() => {
    const channelId = (id: string) => id === 'chat' ? sessionId ?? '' : tabs.tabs.flatMap(t => t.id === id && t.view.kind === 'session-chat' ? [t.view.sessionId] : [])[0] ?? ''
    return { visible: tabs.visible.map(channelId).filter(Boolean), focused: channelId(tabs.active) }
  }, [sessionId, tabs.tabs, tabs.visible, tabs.active])
  const chatVisible = tabs.visible.includes('chat')
  useEffect(() => {
    if (sessionId && chatVisible) send({ type: 'mark_session_read', session_id: sessionId })
  }, [sessionId, chatVisible])
  const renderTranscript = useCallback((content: ReactNode) => <Activity mode={chatVisible ? 'visible' : 'hidden'}><div className="session-transcript-pane" ref={transcriptPaneRef}>{content}</div></Activity>, [chatVisible])
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
    else if (changed && (focus === 'content' || focus === 'close') && tabs.active === 'chat' && !isMobile) (targetFor('chat').querySelector<HTMLTextAreaElement>('.agent-composer textarea') ?? selectedTab)?.focus()
    let cancelFocus = () => {}
    if (changed && tabs.active !== 'chat' && focus === 'content' && !(isMobile && tabs.active.startsWith('session-chat:'))) {
      const target = targetFor(tabs.active)
      if (target) {
        const focusView = () => {
          const view = target.querySelector<HTMLElement>(isMobile ? '[data-view-focus], .hosted-view:not([hidden]) [role="region"], .session-board-columns' : '[data-view-focus], .agent-composer textarea, .hosted-view:not([hidden]) [role="region"], .session-board-columns')
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
  const panelDimensions = usePaneDimensions('session-panel', { min: 280, max: Math.max(280, viewport * .4), default: 340 }, tokenSubject(getAccessToken()), sessionId)
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
  // The header's "opens a tab" group (shell, board, Insights, blackboard): below this width it
  // collapses into one Open button with a menu, so the row still fits (card BLG-450). The header
  // spans the whole session (sessionRoom), the same width a split pane or a phone narrows. A phone
  // (isMobile) collapses it too, even before a real ResizeObserver has measured anything (jsdom has
  // no ResizeObserver; its tests drive this through isMobile instead).
  const openGroupCompact = isMobile || sessionRoom !== null && sessionRoom < 640
  // On a phone (review round 2 of card BLG-450), Search and Star join the same Open menu too — not
  // just the opens-a-tab group — so the title keeps the header's room instead of being squeezed.
  // A narrow split pane keeps them as controls: this app's own window stays wide there, so there is
  // room; only isMobile's real phone chrome is this tight.
  const headerCompact = isMobile
  const railKey = `blerg-runner.workspace-panel.${encodeURIComponent(tokenSubject(getAccessToken()))}.${encodeURIComponent(sessionId ?? '')}`
  // The global key is a read-only migration fallback; new choices belong to this workspace.
  const readWorkspaceRail = () => { try { const v = localStorage.getItem(railKey); return v === null ? readRailPref() : v === '1' } catch { console.warn('Workspace panel preference unavailable'); return null } }
  const [railOwner, setRailOwner] = useState(railKey)
  const [railPref, setRailPref] = useState<boolean | null>(readWorkspaceRail)
  if (railOwner !== railKey) { setRailOwner(railKey); setRailPref(readWorkspaceRail()) }
  useEffect(() => {
    // Only a choice the person made is saved: null (no choice yet) keeps the panel's automatic opening.
    if (railOwner !== railKey || railPref === null) return
    try { localStorage.setItem(railKey, railPref ? '1' : '0') } catch { console.warn('Workspace panel preference save failed') }
  }, [railOwner, railKey, railPref])
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
    setRailPref(true)
    setReopenFor(null)
    if (narrow) setDrawerFor(sessionId)
    setChecklistTarget(previous => ({session: sessionId, id, n: (previous?.n ?? 0) + 1}))
  }, [sessionId, narrow, restorePanes])
  const [approvalTarget, setApprovalTarget] = useState<{session:string; n:number} | null>(null)
  const openChildApproval = useCallback(() => {
    if (!sessionId) return
    restorePanes?.()
    setRailPref(true)
    setReopenFor(null)
    if (narrow) setDrawerFor(sessionId)
    setApprovalTarget(previous => ({session:sessionId, n:(previous?.n ?? 0)+1}))
  }, [sessionId, narrow, restorePanes])
  // Taken once the modal opens: the number stays (the next request is a new one), the session clears.
  const childApprovalShown = useCallback(() => setApprovalTarget(previous => previous && previous.session ? {session:'', n:previous.n} : previous), [])
  // The blackboard recommendation card's "Set up myself" (BLG-454): stable identity, so an
  // unrelated re-render (a streaming child) never invalidates the chat context memo that holds it.
  const openBlackboard = useCallback(() => {
    tabs.open({ kind: 'blackboard', blackboardId: '', label: 'Blackboard', path: { level: 'project' } }, true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabs.open])
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
  const attention = useQuestionItems(sessionId ?? '')
  const proposalInbox = useProposalInbox()
  const sessionProposals = useMemo(() => proposalInbox.pending.filter(p => p.session_id === sessionId), [proposalInbox.pending, sessionId])
  // Only proposals that still await a decision belong in the shared attention collection.
  const sessionProposalsWaiting = useProposalsWaiting(sessionProposals, proposalInbox.clockOffset ?? 0)
  const liveState = sessionLive.live
  // The tool changes come with the live state; an older server's page reads them itself.
  const toolChangesOwn = usePendingToolChanges(sessionId ?? '', agentSession && sessionLive.source === 'client', toolsNonce)
  const toolChangesRead = sessionLive.source === 'client' ? toolChangesOwn : sessionLive.toolChanges
  const toolChanges = toolChangesRead ?? 0
  // Feed the same attention source from whichever live contract this server supports.
  useEffect(() => {
    if (!sessionId || sessionLive.source === 'loading') return
    const asks = liveState.openAsks
    useDecisionAttention.setState(s => {
      const current = { ...s.live[sessionId], session_id: sessionId, tool_changes: toolChanges,
        proposals: sessionProposalsWaiting.length,
        review_requests: sessionLive.reviewRequests ?? [], answer_requests: sessionLive.answerRequests ?? [], delegation_requests: sessionLive.delegationRequests ?? [],
        child_asks: asks.filter(a => a.childSessionId).map(a => ({ session_id: a.childSessionId!, child_title: a.childTitle ?? 'Worker', message_id: a.messageId, question: a.question, asked_at: new Date(a.askedAt ?? 0).toISOString(), no_wait: a.noWait, owner_only: a.ownerOnly })),
        asks: asks.filter(a => !a.childSessionId).map(a => ({ event_id: a.eventId, message_id: a.messageId, question: a.question, asked_at: new Date(a.askedAt ?? 0).toISOString(), seq: 0, no_wait: a.noWait, owner_only: a.ownerOnly })) }
      return JSON.stringify(s.live[sessionId]) === JSON.stringify(current) ? s : { live: { ...s.live, [sessionId]: current } }
    })
  }, [sessionId, sessionLive.source, liveState.openAsks, sessionLive.reviewRequests, sessionLive.answerRequests, sessionLive.delegationRequests, toolChanges, sessionProposalsWaiting.length])
  // With no saved choice the panel is open; a close is remembered, and something new reopens it once.
  const panelOpen = railPref === null ? true : railPref || reopenFor === sessionId
  const drawerOpen = drawerFor === sessionId
  const rawRailOpen = agentSession && (narrow ? drawerOpen : panelOpen)
  const [revealControlsHost, setRevealControlsHost] = useState<HTMLDivElement | null>(null)
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
  const needsYou = attention.items.length
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
    setRailPref(open)
    setReopenFor(null)
    if (open) setToolsNonce(n => n + 1)
    else if (sessionId) setQuiet({ id: sessionId, since: Date.now() - liveState.clockOffset, tools: toolChangesRead })
  }
  const panelControl = usePaneControl('session-panel', 'Toggle workspace panel', { destructive: false }, rawRailOpen, open => {
    if (narrow) { setDrawerFor(open ? sessionId ?? null : null); if (open) setToolsNonce(n => n + 1) }
    else choose(open)
  }, !!agentSession, sessionId)
  const setPanelOpen = panelControl.setOpen
  useEffect(() => {
    const capture = (event: Event) => { const detail = (event as CustomEvent).detail; if (detail.scope === sessionId) detail.panels['session-panel'] = { open: rawRailOpen, width: panelDimensions.width } }
    const apply = (event: Event) => { const detail = (event as CustomEvent).detail; const panel = detail.panels?.['session-panel']; if (detail.scope !== sessionId || !panel || typeof panel.open !== 'boolean' || !Number.isFinite(panel.width)) return; panelControl.restoreOpen(panel.open); panelDimensions.resize(panel.width) }
    window.addEventListener('blerg-layout-capture', capture)
    window.addEventListener('blerg-layout-apply', apply)
    return () => { window.removeEventListener('blerg-layout-capture', capture); window.removeEventListener('blerg-layout-apply', apply) }
  }, [sessionId, rawRailOpen, panelDimensions, panelControl])
  useEffect(() => {
    if (!historyRequest) return
    setPanelOpen(true)
    const search = new URLSearchParams(location.search)
    search.delete('panel')
    navigate({ pathname: location.pathname, search: search.toString(), hash: location.hash }, { replace: true })
    // A History request is consumed once. Pane callbacks change as the live panel changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [historyRequest])
  const railOpen = panelControl.open
  const toggleRail = panelControl.toggle
  const closeRail = () => panelControl.setOpen(false)

  const revealPanel = useCallback(() => {
    restorePanes?.()
    setRailPref(true)
    if (narrow && sessionId) setDrawerFor(sessionId)
  }, [narrow, sessionId, restorePanes])

  const { host: tabHost, open: openTab } = tabs
  const { closeForNavigation: closeExplorer } = explorer
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
  useEffect(() => {
    const apply = (event: Event) => {
      const request = (event as CustomEvent).detail
      if (request?.session !== sessionId || request.account !== tokenSubject(getAccessToken()) || !validReveal(request)) return
      if (request.action === 'open') viewerHost.open({ kind: 'workspace', path: request.target })
      if (request.action === 'reveal') {
        if (!explorer.open) explorer.toggle()
        revealSessionFile({ session: sessionId!, account: tokenSubject(getAccessToken()), path: request.target })
      }
    }
    window.addEventListener(UI_REVEAL_APPLY, apply)
    return () => window.removeEventListener(UI_REVEAL_APPLY, apply)
  }, [sessionId, explorer, viewerHost])
  const canPause = !!session && session.kind !== 'tmux' && !session.archived_at && session.runtime === 'cluster' && ['running', 'idle', 'waiting'].includes(session.status)
  const pause = useCallback(async () => {
    actionErrorAnchor.current = rootRef.current?.querySelector<HTMLElement>('[data-testid=pause-session]') ?? endRef.current
    if (!sessionId) return
    try {
      const res = await apiFetch(`/api/sessions/${sessionId}/pause`, { method: 'POST' })
      if (!res.ok) setActionError({ title: 'Pause failed', message: `Server returned ${res.status}. The ${noun} is still running.`, retry: () => { void pause() } })
    } catch { setActionError({ title: 'Pause failed', message: `Network error. The ${noun} is still running.`, retry: () => { void pause() } }) }
  }, [sessionId, noun])
  const paletteCommands = useMemo(() => session ? [
    { id: `inbox:${session.id}`, destructive: false, title: 'Open inbox', shortcut: 'Ctrl/⌘+Alt+I', shortcutGroup: 'In a session' as const, run: () => openInbox() },
    ...(selectedSession ? [{ id: `star-session:${selectedSession.id}`, destructive: false, title: selectedSession.starred ? `Unstar ${sessionNoun(selectedSession)}` : `Star ${sessionNoun(selectedSession)}`, run: () => send({ type: 'set_session_star', session_id: selectedSession.id, starred: !selectedSession.starred }) }] : []),
    { id: `insights-session:${session.id}`, destructive: false, title: `${titledNoun} insights`, here: 7, run: () => openSessionTab({ kind: 'insights', workspace: session.id }, true) },
    ...(session.kind !== 'tmux' ? [{ id: `capabilities-session:${session.id}`, destructive: false, title: 'Skills & plugins', run: () => { revealPanel(); setCapabilitiesRequest(n => n + 1) } }] : []),
    ...(canPause ? [{ id: `pause-session:${session.id}`, destructive: true, title: `Pause ${noun}`, run: () => { void pause() } }] : []),
    ...(selectedSession && selectedSession.kind !== 'tmux' && !ending && !selectedSession.archived_at && ['running', 'idle', 'waiting', 'starting', 'disconnected'].includes(selectedSession.status) ? [{ id: `end-session:${selectedSession.id}`, title: `End ${sessionNoun(selectedSession)}…`, keywords: `End this ${sessionNoun(selectedSession)}`, destructive: true, run: () => { endRef.current?.focus(); setConfirmEnd(true) } }] : []),
  ] : [], [session, selectedSession, canPause, pause, revealPanel, ending, noun, titledNoun, openSessionTab])
  usePaletteCommands(paletteCommands)

  if (!session) {
    const isFailed = sessionId !== undefined && sessionId in failedSessions
    const isPending = sessionId !== undefined && sessionId in pendingSessionIds

    if (isFailed) {
      const message = failedSessions[sessionId!] || 'Workspace failed to start.'
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
        <div>Workspace not found</div>
      </div>
    )
  }

  const liveRunning = sessionLive.source === 'server'
    ? liveState.subagents.length + (sessionLive.tasks ?? []).filter(task => !task.done).length
    : null
  const headerSession = selectedSession ?? session
  const headerRepositories = sessionRepos(headerSession)
  const headerNoun = sessionNoun(headerSession)
  const headerAgent = headerSession.kind !== 'tmux'
  const headerActive = headerAgent && !headerSession.archived_at && ['running', 'idle', 'waiting', 'starting', 'disconnected'].includes(headerSession.status)
  const headerWorking = !headerSession.paused_reason && WORKING_STATES.has(displayState(headerSession, headerId === sessionId ? liveRunning : null))

  // Explicit legacy Terminal rows retain only a history viewer.
  const isAgent = session.kind !== 'tmux'
  // The Messages tab exists for terminal sessions only (see the tab toggle below).
  const showMessages = !isAgent && activeTab === 'chat'
  const endLine = describeSessionEnd(session)

  const tabBar = (group?: TabGroup, ends?: StripEnds) => (<SessionTabBar group={group} grouped={!!ends} start={ends?.start ?? true} end={ends?.end ?? true} tabs={tabs} explorer={explorer} shellOpen={false} />)

  // The header's panel toggle (card BLG-450: moved out of the tab bar into the header's
  // non-destructive group, alongside Search and Star). Its live meters (work inside the session,
  // its children) are unchanged.
  const panelToggle = isAgent && (
    <button
      type="button"
      className="icon-btn"
      ref={panelDimensions.triggerRef}
      data-testid="rail-toggle"
      aria-expanded={railOpen}
      aria-controls={railId}
      aria-label={railLabel(work.inside, work.children)}
      title={railOpen ? 'Hide the workspace panel' : 'Show the workspace panel: what needs you, what is running, this workspace'}
      onClick={toggleRail}
    >
      <PanelToggleIcon />
      {work.inside > 0 && <span className="icon-btn-count" data-testid="rail-badge-inside" data-glyph="🤖" aria-hidden="true"><SubagentFace className="icon-btn-count-glyph" />{work.inside}</span>}
      {work.children > 0 && <span className="icon-btn-count" data-testid="rail-badge-children" data-glyph="↳" aria-hidden="true">↳{work.children}</span>}
    </button>
  )
  // The header's "opens a tab" group (card BLG-450: moved out of the tab bar's right side into the
  // top bar, grouped by what they do). At a narrow width they collapse into one Open menu instead.
  const canOpenShell = isAgent && shellAvailable && !session.archived_at
  const openShell = () => shellRef.current?.open()
  const openInsights = () => tabs.open({ kind: 'insights', workspace: session.id }, true)
  const toggleStar = () => send({ type: 'set_session_star', session_id: headerSession.id, starred: !headerSession.starred })
  const openTabGroup = openGroupCompact ? (
    <IconMenu label="Open" icon={<OpenIcon />} testId="open-tab-menu" align="left">
      {canOpenShell && <IconMenuItem label="New shell" icon={<ShellIcon />} onSelect={openShell} />}
      <SessionBoardMenuItem sessionId={session.id} refreshKey={boardsKey} onSelect={() => {}} />
      <IconMenuItem label="Insights" icon={<InsightsIcon />} onSelect={openInsights} />
      <BlackboardMenuItem sessionId={session.id} tabs={tabs} onSelect={() => {}} />
      <IconMenuItem label="Restore layout" icon={<PanelToggleIcon />} onSelect={tabs.restoreLayout} />
      {/* On a phone (review round 2): Search and Star join the same menu instead of staying their
          own header controls, so the title keeps the header's room. */}
      {headerCompact && <WorkspaceLayouts account={tokenSubject(getAccessToken())} tabs={tabs} menu />}
      {headerCompact && <PaletteMenuItem onSelect={() => {}} />}
      {headerCompact && <IconMenuItem label={headerSession.starred ? 'Unstar' : 'Star'} icon={<StarIcon filled={!!headerSession.starred} />} onSelect={toggleStar} />}
    </IconMenu>
  ) : (
    <>
      {canOpenShell && <ShellToggleButton tabs={tabs} onOpen={openShell} />}
      <SessionBoardButton sessionId={session.id} refreshKey={boardsKey} />
      <button type="button" className="icon-btn" data-testid="session-insights-toggle" aria-label="Insights" title="Insights: this workspace's time, work, tokens and cost" onClick={openInsights}>
        <InsightsIcon />
      </button>
      <BlackboardButton sessionId={session.id} tabs={tabs} />
    </>
  )

  function stopTurn() {
    if (!headerId || !chatInterrupt || headerSession.paused_reason || stopping === headerId) return
    const id = headerId
    setStopping(id)
    window.setTimeout(() => setStopping(cur => (cur === id ? null : cur)), 10_000)
    chatInterrupt().catch(() => {
      console.warn('Stop turn failed', { cause: 'request_failed' })
      setStopping(cur => (cur === id ? null : cur))
      actionErrorAnchor.current = stopRef.current
      setActionError({ title: 'Stopping the turn failed', message: 'Stop could not reach the worker. All work is retained; retry when connected.', retry: () => stopTurn() })
    })
  }
  async function endSession() {
    if (!headerId || ending) return
    actionErrorAnchor.current = endRef.current
    setConfirmEnd(false)
    setEnding(true)
    let ended = false
    try {
      const res = await apiFetch(`/api/sessions/${headerId}`, { method: 'DELETE' })
      if (res.ok && res.status !== 202) {
        ended = true
        navigate('/sessions')
      } else {
        const body = (typeof res.json === 'function' ? await res.json().catch(() => null) : null) as { error?: string; message?: string } | null
        console.warn('End conversation failed', { cause: 'http_status', status: res.status })
        const reason = typeof body?.error === 'string' ? body.error : typeof body?.message === 'string' ? body.message : undefined
        setActionError({ title: `Ending the ${headerNoun} failed`, message: reason || (headerSession?.paused_reason ? 'End could not confirm workspace cleanup. The workspace and its work are retained; retry End when its daemon is connected.' : `Server returned ${res.status}. The ${headerNoun} may still be running.`), retry: () => { void endSession() } })
      }
    } catch {
      console.warn('End conversation failed', { cause: 'request_failed' })
      setActionError({ title: `Ending the ${headerNoun} failed`, message: headerSession?.paused_reason ? 'End could not be confirmed because the connection failed. The workspace and its work are retained; retry End when connected.' : `Network error. The ${headerNoun} may still be running.`, retry: () => { void endSession() } })
    } finally {
      if (!ended) {
        refocusEnd.current = true
        setEnding(false)
      }
    }
  }


  return (
    <QuestionThreadsProvider client={questionThreads}><GraphTabContext.Provider value={(sessionId, memberId, label) => { tabs.open({ kind: 'branch-graph', sessionId, memberId, label }, true); if (isMobile) explorer.closeForNavigation() }}><ViewerHostContext.Provider value={viewerHost}>
    <NeedsYouTabs tabs={tabs} attention={attention} settled={liveState.settled} /><InboxTabs tabs={tabs} onOpen={isMobile ? explorer.closeForNavigation : undefined} /><RepositoryGraphTabs tabs={tabs} /><InsightsTabs tabs={tabs} workspaceId={session.id} />
    {actionError && <ActionError anchor={actionErrorAnchor} title={actionError.title} message={actionError.message} onRetry={() => { setActionError(null); actionError.retry() }} onDismiss={() => setActionError(null)} />}
    <div ref={rootRef} data-session-root style={{ display: 'flex', flexDirection: 'column', minHeight: 0, minWidth: 0, overflow: 'hidden', ...(isMobile ? { position: 'fixed', inset: 0 } : { height: '100%' }), background: 'var(--basalt)', color: 'var(--chalk)' }}>
      <SessionColorPicker key={session.id} sessionId={session.id} color={session.color} commandsOnly />
      {/* Header */}
      <div data-testid="session-header" style={{ padding: isMobile ? '12px 8px' : '12px 16px', borderBottom: '1px solid var(--stone)', display: 'flex', alignItems: 'center', gap: isMobile ? 8 : 12, background: 'var(--basalt)', minWidth: 0 }}>
        {isMobile && (
          <button
            aria-label={tabs.active === 'chat' ? 'Back to workspaces' : 'Back to Main'}
            onClick={() => tabs.active === 'chat' ? navigate('/sessions') : tabs.back()}
            style={{ background: 'none', border: 'none', color: 'var(--blaze)', cursor: 'pointer', fontSize: 18, lineHeight: 1, padding: 0, fontFamily: 'inherit' }}
          >
            ←
          </button>
        )}
        <RuntimeIcon runtime={headerSession.runtime} machine={headerDaemon?.name ?? headerSession.daemon_id} />
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
                  if (trimmed === (headerSession.title || headerRepositories.label)) { setEditing(false); return }
                  setSaving(true)
                  try {
                    await apiFetch(`/api/sessions/${headerId}`, {
                      method: 'PATCH',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({ title: trimmed }),
                    })
                    setSaving(false)
                    setEditing(false)
                  } catch {
                    console.warn('Conversation title save failed', { cause: 'request_failed' })
                    setSaving(false)
                  }
                } else if (e.key === 'Escape') {
                  setEditing(false)
                }
              }}
              onBlur={async () => {
                const trimmed = editValue.trim()
                if (!trimmed || trimmed === (headerSession.title || headerRepositories.label)) { setEditing(false); return }
                setSaving(true)
                try {
                  await apiFetch(`/api/sessions/${headerId}`, {
                    method: 'PATCH',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ title: trimmed }),
                  })
                  setSaving(false)
                  setEditing(false)
                } catch {
                  console.warn('Conversation title save failed', { cause: 'request_failed' })
                  setSaving(false)
                }
              }}
              style={{ fontWeight: 600, fontSize: 15, color: 'var(--chalk)', background: 'transparent', border: 'none', borderBottom: '1px solid var(--blaze)', outline: 'none', width: '100%', fontFamily: 'inherit' }}
            />
          ) : (
            <div
              onClick={() => { setEditing(true); setEditValue(headerSession.title || headerRepositories.label) }}
              // Review round 2 of card BLG-450: the title is read first, so it is the last thing
              // that gives up room. The controls group carries the space pressure now (the "opens a
              // tab" group collapses, and on a phone Search and Star join it too), and the subtitle
              // below shrinks away first (flexShrink 3, down to nothing) before the title gives up
              // any of its own.
              style={{ flexShrink: 1, minWidth: 80, maxWidth: '100%', fontWeight: 600, fontSize: 15, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--chalk)', cursor: 'text' }}
            >
              {headerSession.title || headerRepositories.label}
            </div>
          )}
          <div style={{ flexShrink: 3, minWidth: 0, fontSize: 12, color: 'var(--fog)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {isAutomated(headerSession) && <span data-testid="detail-started-by">started by {startedByLabel(headerSession, headerParent)} · </span>}
            {/* Fixed at start; an interactive session is the ordinary case and goes unmarked. */}
            {headerSession.interaction === 'unattended' && (
              <span data-testid="detail-unattended" title="Started unattended: the agent does not wait for you in the chat">Unattended · </span>
            )}
            {(!isMobile || !headerSession.workspace?.members.length) && <>{headerDaemon?.name ?? headerSession.daemon_id} · </>}
            {headerSession.workspace?.members.length ? <span title={headerRepositories.full} aria-label={headerRepositories.full}
              style={{ display: 'inline-block', maxWidth: '100%', overflow: 'hidden', textOverflow: 'ellipsis', verticalAlign: 'bottom' }}>
              {headerRepositories.label}
            </span> : headerSession.project_path}

          </div>
        </div>
        {/* The header's controls, one style (IconButton), grouped by what they do (card BLG-450):
            opens a tab (shell, board, Insights, blackboard — one Open menu below a narrow width);
            non-destructive (search, star, the workspace panel toggle); destructive (Stop while the
            agent works, then End session, after a visibly wider gap). All stay visible at every
            width now; only the first group collapses. */}
        <div className="icon-btn-group" data-testid="session-header-controls">
          <div className="session-header-group">{openTabGroup}</div>
          <div className="session-header-group">
            {!headerCompact && <PaletteButton />}
            {!headerCompact && (
              <button
                type="button"
                className={headerSession.starred ? 'icon-btn starred' : 'icon-btn'}
                data-testid="star-toggle"
                data-starred={headerSession.starred ? 'true' : 'false'}
                aria-pressed={!!headerSession.starred}
                aria-label={headerSession.starred ? 'Unstar' : 'Star'}
                title={headerSession.starred ? 'Starred: kept at the top of the list. Click to unstar' : 'Star: keep it at the top of the list'}
                onClick={toggleStar}
              >
                <StarIcon filled={!!headerSession.starred} />
              </button>
            )}
            {!headerCompact && <WorkspaceLayouts account={tokenSubject(getAccessToken())} tabs={tabs} />}
            {panelToggle}
          </div>
          <div className="session-header-group session-header-group-destructive">
 {headerSession.runtime === 'docker' && !headerActive && <ReleaseStorage key={headerSession.id} sessionId={headerSession.id} />}
            {/* Stop, immediately left of End session: only while the agent works. */}
            {headerActive && headerAgent && !headerSession.paused_reason && chatBusy && chatInterrupt && (
              <button
                ref={stopRef}
                type="button"
                className="icon-btn"
                data-testid="stop-turn"
                aria-label="Stop the agent's current turn"
                title="Stop the agent's current turn"
                aria-busy={stopping === headerSession.id || undefined}
                disabled={stopping === headerSession.id}
                onClick={stopTurn}
              >
                <StopTurnIcon />
              </button>
            )}
            {/* disconnected (cluster pod gone) still offers End session: the server deletes the Job and marks
                the session stopped. It asks first, in a confirmation anchored to the ✕. */}
            {headerActive && (
              <button
                ref={endRef}
                type="button"
                className="icon-btn danger"
                data-testid="stop-session"
                aria-label={`End ${headerNoun}`}
                aria-haspopup="dialog"
                aria-expanded={confirmEnd}
                aria-busy={ending || undefined}
                disabled={ending}
                title={`End ${headerNoun}: the history stays`}
                onClick={() => setConfirmEnd(true)}
              >
                <EndSessionIcon />
              </button>
            )}
            {headerActive && confirmEnd && (
              <ConfirmPopover anchor={endRef} title={`End this ${headerNoun}?`} description={endSessionText(headerSession.runtime, !headerAgent)}
                confirmLabel={`End ${headerNoun}`} onConfirm={() => void endSession()} onCancel={() => setConfirmEnd(false)} testId="end-session-confirm" />
            )}
            {!headerSession.archived_at && (headerSession.parent_session_id
              ? <ChildDiscardAction noun={headerNoun} key={headerSession.id} palette focusTarget={endRef} running={headerWorking} parentId={headerSession.parent_session_id} childId={headerSession.id} title={headerSession.title || 'Untitled chat'} />
              : <ParentDiscardAction noun={headerNoun} key={headerSession.id} palette focusTarget={endRef} running={headerWorking} sessionId={headerSession.id} title={headerSession.title || 'Untitled chat'} onDiscarded={() => navigate('/sessions')} />)}
          </div>
        </div>
        {/* What the badge counts, said to a screen reader when it changes. */}
        {headerAgent && <span className="rail-announce" role="status" aria-live="polite" data-testid="rail-announce">{railLabel(work.inside, work.children)}</span>}
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
      <QuestionTabs tabs={tabs} sessionId={session.id} onOpen={() => { if (narrow) closeRail(); if (isMobile) explorer.closeForNavigation() }} />
      <BlackboardTabs tabs={tabs} sessionId={session.id} />
      <SessionBoardTabs tabs={tabs} sessionId={session.id} live={sessionLive} refreshKey={boardsKey} />
      <ChannelPanelsContext.Provider value={channelPanels}>
      <OpenReviewContext.Provider value={(owner, artifact) => { if (isMobile) explorer.closeForNavigation(); if (owner === session.id) openArtifact(artifact); else navigate(`/sessions/${encodeURIComponent(owner)}?artifact=${encodeURIComponent(artifact)}`) }}>
        <ExplorerDock key={`${session.id}:${explorer.account}`} sessionId={session.id} selectedSessionId={(() => { const view = tabs.tabs.find(t => t.id === tabs.active)?.view; return view?.kind === 'session-chat' ? view.sessionId : session.id })()} layout={explorer} onOpen={(chat, keep = false) => { if (isMobile) explorer.closeForNavigation(); if (chat.id === session.id) tabs.select('chat'); else tabs.open({ kind: 'session-chat', sessionId: chat.id, label: chat.title || 'Untitled chat' }, isMobile || keep) }} />
      </OpenReviewContext.Provider>
      </ChannelPanelsContext.Provider>
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
          History
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
          ? <ShellWorkspace tabs={tabs} enabled={shellAvailable} capabilityPending={shellCapability === null} key={session.id + tokenSubject(getAccessToken())} ref={shellRef} sessionId={session.id} accountId={tokenSubject(getAccessToken())}><div className="session-agent-pane"><SessionGroups key={tabs.scope} tabs={tabs} bar={tabBar} /><SessionChatTabs tabs={tabs} /><AgentChatView revealControlsHost={tabs.active === 'chat' ? revealControlsHost : null} transcript={renderTranscript} key={session.id} session={session} thinking={showThinking} compact={compactTools} openArtifact={fileAsk} onOpenChecklistItem={openChecklistItem} onManageChildApproval={openChildApproval} onOpenBlackboard={openBlackboard} checklistItems={sessionLive.source === 'server' ? (liveState.checklist?.items ?? EMPTY_CHECKLIST_ITEMS) : undefined} mcpHold={liveState.mcpHold ?? null} canReleasePrompt={sessionLive.source === 'loading' ? undefined : liveState.canReleasePrompt === true} canAnswerApprovals={sessionLive.source === 'loading' ? null : liveState.canReleasePrompt === true} reasoningTokens={liveState.reasoningTokens === true} /></div></ShellWorkspace>
          : <><SessionGroups key={tabs.scope} tabs={tabs} bar={tabBar} />{createPortal(<TerminalHistory sessionId={sessionId ?? ''}/>, tabs.targetFor('chat'))}{blergTransport().files && <HostedViewers sessionId={session.id} files={blergTransport().files!} repo={session.repo} />}</>}
      </div>

      {/* Messages — only rendered when its tab is active */}
      {showMessages && tabs.active === 'chat' && (
        <div style={{ flex: 1, overflow: 'auto', minHeight: 0, background: 'var(--basalt)' }}>
          <MessageThread messages={sessionMessages} hideSessionLabel />
        </div>
      )}

      </div>

      </div></div>
      {isAgent && (
        <SessionRail
          attention={attention}
          onOpenNeedsYou={() => { tabs.open({ kind: 'needs-you' }, true); closeRail() }}
          key={session.id}
          onPause={canPause ? () => { void pause() } : undefined}
          onRevealControlsHost={setRevealControlsHost}
          capabilitiesRequest={capabilitiesRequest}
          id={railId}
          paneLayout={{ ...panelDimensions, id: railId, open: railOpen, toggle: toggleRail, close: closeRail }}
          session={session}
          machine={daemon?.name}
          live={liveState}
          tasks={sessionLive.tasks}
          historyRequest={historyRequest}
          childSessions={sessionLive.children}
          delegationRequests={sessionLive.delegationRequests}
          answerRequests={sessionLive.answerRequests}
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
    </ViewerHostContext.Provider></GraphTabContext.Provider></QuestionThreadsProvider>
  )
}

// The toggle's name: what it opens, and what its two counts say ("Session panel: 3 subagents and
// background tasks · 4 child sessions"). What needs you is not counted here (owner rule,
// 2026-10-26). Questions from the session and its children appear in Needs you.
function railLabel(inside: number, children: number): string {
  const counts = runningWorkLabel(inside, children)
  return counts ? `Workspace panel: ${counts}` : 'Workspace panel'
}
