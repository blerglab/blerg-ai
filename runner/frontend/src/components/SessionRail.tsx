import { PendingProposal } from './ProposalAttention'
import type { ProposalInfo } from '../types'
import { reasoningLabel } from '@blerglab/chat'
import { modelDisplayName } from '@blerglab/chat'
// SessionRail: the session page's side panel, in tabs. Live: the pinned tickets (PinLines, a line
// each), what needs the person (the agent's
// open questions, with a box that answers each; tool changes waiting on a decision), what is running
// (Main, the agent itself, first, as the working line under the chat says; the subagents and background
// tasks that run inside it indented under it; started sessions at its level), the session's
// facts (with Resume when limit resets). History (HistoryTab.tsx): the pieces of work the session did. Files
// (FilesTab.tsx): the session's files. Capabilities (CapabilitiesTab.tsx): what it loaded. A tab other than Live
// reads the server only while it is on screen. The tab chosen is kept per browser. On a wide screen it is a panel beside the chat; on a narrow one a drawer over it (modal:
// Esc and the backdrop close it, focus stays inside, the page does not scroll behind it).
//
// It stays mounted while closed (hidden), so the background tasks' state — which failures were
// dismissed, since when it watches — lives as long as the page shows the session. Render it under
// a key of the session.
import { useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link, useNavigate } from 'react-router-dom'
import { OpenSessionContext } from '../lib/openSession'
import {
  AskReply,
  checklistAssignment,
  KindIcon,
  commandOf,
  isShell,
  doneCount,
  planLabel,
  progressText,
  loadDiffSummary,
  statusTone,
  StatusGlyph,
  elapsedOf,
  scrollToCall,
  scrollToEvent,
  taskFailed,
  titleOf,
  tokenLabel,
  useBackdropClose,
  useBackgroundTasks,
  useTranscriptStore,
  useTicker,
  clockLabel,
  runningLabel,
  fileCount,
  CommitViewerHost,
  Hover,
  HovercardProvider,
  getHovercardClock,
  useMainStep,
  useWorkingLineStore,
  type ArtifactInfo,
  type AskLive,
  type BackgroundTask,
  type PlanLive,
  type LiveState,
  type MainStep,
  type CardTarget,
  type ChecklistItem,
  type HovercardSources,
  type Ticket,
  type TicketSummaryLive,
} from '@blerglab/chat'
import { readProgress, type LiveProgress, type ProgressSubagent, type SessionPanelLiveState } from '../lib/liveState'
import { blergTransport } from '../lib/chatTransport'
import { ENGINE_LABELS, ENGINE_SHORT, RUNTIME_LABELS } from '../lib/runtimes'
import { assigneeName, diffArtifacts, hasPlanTab, planList, planSummary, planTabLabel, type PlanList } from '../lib/railPlan'
import PlanChecklist from './PlanChecklist'
import { RAIL_TABS, lifetimeLabel, readRailTab, waitLabel, writeRailTab, type RailTab } from '../lib/sessionRail'
import FilesTab from './FilesTab'
import { PinnedTicketModal } from './BoardCardView'
import { boardCardRef, boardReadSettled, cardPreview, checkAccount, signedInAccount, useLiveBoardCards, useTicketView } from '../lib/liveBoardCard'
import { PINS_SHOWN, STATE_GLYPH, livePins, pinView, withWorkers, workTicket, type ChildPin, type PinView } from '../lib/pins'
import { runTarget, subagentModelFact } from '../lib/runFacts'
import { startedByLabel } from '../lib/startedBy'
import HistoryTab from './HistoryTab'
import { GRANT_GLYPH } from '../lib/grantGlyphs'
import CapabilitiesTab from './CapabilitiesTab'
import { useSessionStore } from '../hooks/useSessionStore'
import DelegationLiveReview from './DelegationLiveReview'
import {useSessionPlugins} from '../hooks/useSessionPlugins'
import { useCapabilities } from '../hooks/useCapabilities'
import { enabledCapabilityCount, latestCapabilitiesEvent } from '../lib/capabilities'
import ShowLink, { ShowMissing } from './RailShowLink'
import RunCard, { RunGlyph, RunStep } from './RunCard'
import type { SessionInfo } from '../types'
import StatusPill from './StatusPill'
import UsageLimitResume from './UsageLimitResume'
import SessionPermissions from './SessionPermissions'
import { backgroundCount, displayState } from '../lib/displayState'
import { ChildSessionRow, StartedSessions } from './ChildSessions'
import { childIsWorking, type LiveChildSession, type LiveDelegationGrant, type LiveDelegationRequest, type LiveReviewRequest } from '../lib/liveState'
import { SessionBoardContext, useLoadSessionBoard, useSessionBoard, withSessionBoard } from '../lib/sessionBoard'
import { CardRefStates, CardRefText } from './ChecklistRefs'
import { ChecklistRefsContext, useChecklistRefs } from '../lib/checklistRefs'
import './SessionRail.css'
import { canDismissAsk, dismissWithUndo, useDismissed } from '../lib/needsYouDismiss'

/** How long Live waits for a board card's read before it trusts the pinned state. */
export const PIN_READ_WAIT_MS = 1500

/** How long a "Show in conversation" that found nothing says so. */
const MISSING_MS = 4000

const FOCUSABLE = 'button:not([disabled]), textarea:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'

export interface SessionRailProps {
  proposals?: ProposalInfo[]
  /** The proposals that still wait on the person (useProposalsWaiting): what Needs you counts. An
   *  overdue one is still listed, as expired. Without it, all of proposals count. */
  waitingProposals?: ProposalInfo[]
  proposalsError?: string | null
  onProposalsChanged?: () => void
  openChildApproval?: number
  /** The Permissions modal took the open request: clear it so a remount does not open it again. */
  onChildApprovalShown?: () => void
  openChecklistItem?: {id: string; n: number} | null
  childSessions?: LiveChildSession[]
  delegationRequests?: LiveDelegationRequest[]
  /** The session's pending review requests: one Needs you row each, opening the file at its version. */
  reviewRequests?: LiveReviewRequest[]
  delegationGrants?: LiveDelegationGrant[]
  /** The element's id (the header toggle's aria-controls). */
  id: string
  session: SessionInfo
  /** Where it runs, by name (the daemon's), for the Session facts. */
  machine?: string
  /** What is live in the session, read once by the page (useSessionLive: the server's whole log, or
   *  the chat's useLiveState on an older server); its header badge counts from it too. */
  live: SessionPanelLiveState
  /** The background tasks still running, by the server; absent or null: the chat's list (what the
   *  transcript holds). Finished ones always come from the chat's list (they linger, a failure stays). */
  tasks?: BackgroundTask[] | null
  /** What runs live (subagents and tasks not done) by the server's whole log, for the Status row;
   *  null when there is no such read (the server's per-session count is used). The header's too. */
  liveRunning?: number | null
  /** Tool changes waiting on a decision on this session. */
  toolChanges: number
  /** A panel beside the chat (a wide screen) or a drawer over it. */
  mode: 'panel' | 'drawer'
  open: boolean
  onClose: () => void
  /** The Capabilities tab came on screen: the page reads the tool changes again. */
  onCapabilitiesShown?: () => void
  /** Opens a file in the chat's viewer (History's version chips). */
  /** Opens a session file in the chat's viewer; a diff at `commit` when one is given. */
  onOpenArtifact: (id: string, commit?: string) => void
  /** The panel opened by itself for a question: show Live (not saved as the choice), then say so. */
  showLive?: boolean
  /** Called once Live was shown for showLive; the page clears its request. */
  onLiveShown?: () => void
}

const NO_EVENTS: never[] = []

export default function SessionRail(props: SessionRailProps) {
  const { session, live, toolChanges: pendingToolChanges, mode, open, onClose } = props
  const toolChanges = session.archived_at ? 0 : pendingToolChanges
  const bg = useBackgroundTasks(session.id)
  // A background subagent is also a background task: its subagent row says more, so the task row goes.
  const subKeys = new Set(live.subagents.map(s => s.key))
  const ended = new Set(bg.recent.map(t => t.taskId))
  const running = (props.tasks ?? bg.running).filter(t => !ended.has(t.taskId))
  const tasks = [...running, ...bg.recent].filter(t => !(t.kind === 'agent' && subKeys.has(t.callId)))
  const runningTasks = tasks.filter(t => !t.done).length
  const children = (props.childSessions ?? []).filter(child => !child.archived_at)
  // A child at work lives in Running now, once, with everything; it moves to Started sessions when
  // it stops (idle, done, failed).
  const runningChildren = children.filter(childIsWorking)
  const requests = session.archived_at ? [] : props.delegationRequests ?? []
  // What the person dismissed (or is waiting out its Undo) leaves Needs you at once.
  const dismissed = useDismissed()
  const reviews = (session.archived_at ? [] : props.reviewRequests ?? []).filter(r => !dismissed(`review:${r.request_id}`))
  const openAsks = (session.archived_at ? [] : live.openAsks).filter(a => !a.messageId || !dismissed(`ask:${a.messageId}`))
  // The main agent at work, as the working line under the chat says (ChatView reports it): its row
  // heads Running now and counts there.
  const lineReported = useWorkingLineStore(s => !!s.lines[session.id])
  const mainWorking = !session.archived_at && lineReported
  // The clock runs only while something timed is on screen.
  const timed = open && (mainWorking || live.subagents.length > 0 || live.openAsks.length > 0 || tasks.length > 0 || runningChildren.length > 0)
  const tick = useTicker(timed)
  const now = tick - live.clockOffset
  // Main's clocks run on the chat's own offset (the transcript's), as the working line's do.
  const chatOffset = useTranscriptStore(s => s.sessions[session.id]?.clockOffset ?? 0)
  const mainNow = tick - chatOffset
  const mainStep = useMainStep(session.id, mainNow)
  // A live session that is not working: idle (Zz) or waiting on the person (?). Any other state
  // (starting, stopped, ended, failed, disconnected) has no Main card and keeps "Nothing is running."
  const mainResting = session.status === 'idle' || session.status === 'waiting'
  const mainShown = !!mainStep || mainResting
  // Lifetime moves by the minute: a slow clock while the panel is open.
  const minute = useMinute(open)

  // "Show in conversation" that found nothing (the row is above what the transcript has loaded).
  const [missing, setMissing] = useState<string | null>(null)
  useEffect(() => {
    if (missing === null) return
    const t = setTimeout(() => setMissing(null), MISSING_MS)
    return () => clearTimeout(t)
  }, [missing])

  // The chat is on the page under the drawer too: look first, and close the drawer only when the
  // row was found, so a row that is not loaded says so where the person is looking.
  const jump = (rowKey: string, go: () => boolean) => {
    if (!go()) {
      setMissing(rowKey)
      return
    }
    setMissing(null)
    if (mode === 'drawer') onClose()
  }

  // Live (what needs the person, what runs, the session) or History; the person's choice is kept.
  const [chosen, setTab] = useState<RailTab>(readRailTab)
  // Opened by itself for a question: show it, without making Live the person's choice; then tell
  // the page, which clears the request, so the next one is taken again.
  const wantLive = props.showLive ?? false
  const [liveTaken, setLiveTaken] = useState(false)
  if (wantLive && !liveTaken) {
    setLiveTaken(true)
    setTab('live')
  } else if (!wantLive && liveTaken) {
    setLiveTaken(false)
  }
  useEffect(() => {
    if (!props.openChildApproval || !open) return
    const timer = window.setTimeout(() => {
      setTab('live')
    }, 0)
    return () => window.clearTimeout(timer)
  }, [props.openChildApproval, open])
  const { onLiveShown } = props
  useEffect(() => { if (liveTaken) onLiveShown?.() }, [liveTaken, onLiveShown])
  // Plan is there only when the session has a ticket, a list or a diff: otherwise a saved Plan shows Live.
  const tickets = live.tickets ?? []
  // Every pin, read live from its board (a Blerg Board card): Live lists the ones in progress, Plan
  // all of them (lib/pins.ts).
  const pinRefs = tickets.flatMap(t => { const r = boardCardRef(t); return r ? [r] : [] })
  // Read again while Live or Plan is on screen, so a card moved to Done shows ✓ before the next pin.
  const boardCards = useLiveBoardCards(pinRefs, session.id, open && (chosen === 'live' || chosen === 'plan'))
  // A switch of account forgets what the hovercards read under the other one.
  useEffect(() => { checkAccount() })
  const basePinViews = tickets.map(t => {
    const r = boardCardRef(t)
    const v = pinView(t, r ? boardCards.get(`${r.board}#${r.number}`) : null)
    return r && !v.live && !boardReadSettled(r.board, r.number) ? { ...v, pending: true } : v
  })
  // Live waits for the board's read (at most PIN_READ_WAIT_MS) before it shows a board card, so a
  // card already done never flashes there; Plan shows the pinned state at once, muted.
  const pendingKey = basePinViews.filter(v => v.pending).map(v => v.pin.key).join(',')
  const [waitOver, setWaitOver] = useState<string | null>(null)
  useEffect(() => {
    if (!pendingKey) return
    const clock = getHovercardClock()
    const t = clock.setTimeout(() => setWaitOver(pendingKey), PIN_READ_WAIT_MS)
    return () => clock.clearTimeout(t)
  }, [pendingKey])
  const diffs = useMemo(() => diffArtifacts(live.files?.items), [live.files?.items])
  const list = planList(live.checklist, live.plan)
  const planShown = hasPlanTab(list, tickets.length, diffs.length)
  const tab: RailTab = chosen === 'plan' && !planShown ? 'live' : chosen
  const pick = (t: RailTab) => {
    setTab(t)
    writeRailTab(t)
  }
  // The summary line on Live: to the Plan tab, with the focus on its tab.
  const goPlan = () => {
    pick('plan')
    document.getElementById(`${props.id}-tab-plan`)?.focus()
  }

  const checklistNodes = useRef(new Map<string, HTMLLIElement>())
  const nodeKey = (id: string) => JSON.stringify([session.id, id])
  const [highlight, setHighlight] = useState<{session: string; id: string} | null>(null)
  const highlightedId = highlight?.session === session.id ? highlight.id : undefined
  const [checklistNotice, setChecklistNotice] = useState<{session: string; text: string} | null>(null)
  const [checklistJump, setChecklistJump] = useState<{session: string; id: string} | null>(null)
  const onChecklistElement = useCallback((id: string, element: HTMLLIElement | null) => {
    const key = JSON.stringify([session.id, id])
    if (element) checklistNodes.current.set(key, element)
    else checklistNodes.current.delete(key)
  }, [session.id])
  // The done items fold into one row; a jump to one of them unfolds it.
  const [doneOpen, setDoneOpen] = useState(false)
  const openDoneFor = useCallback((id: string) => {
    if (checklistNodes.current.get(JSON.stringify([session.id, id]))?.hidden) setDoneOpen(true)
  }, [session.id])
  const jumpChecklist = (id: string) => {
    if (!checklistNodes.current.has(nodeKey(id))) return
    openDoneFor(id)
    setHighlight(null)
    setChecklistNotice(null)
    pick('plan')
    setChecklistJump({session: session.id, id})
  }
  const completedJump = useRef<object | null>(null)
  const highlightTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => { if (highlightTimer.current) clearTimeout(highlightTimer.current) }, [session.id])
  useEffect(() => {
    if (!checklistJump || checklistJump.session !== session.id || tab !== 'plan' || completedJump.current === checklistJump) return
    completedJump.current = checklistJump
    const frame = requestAnimationFrame(() => {
      const row = checklistNodes.current.get(JSON.stringify([checklistJump.session, checklistJump.id]))
      if (!row?.isConnected) return
      row.scrollIntoView({block: 'center', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'})
      row.focus({preventScroll: true})
      setHighlight({session: checklistJump.session, id: checklistJump.id})
      if (highlightTimer.current) clearTimeout(highlightTimer.current)
      highlightTimer.current = setTimeout(() => setHighlight(previous => previous?.session === checklistJump.session ? null : previous), 1500)
    })
    return () => cancelAnimationFrame(frame)
  }, [checklistJump, tab, session.id])
  const externalJumpSeen = useRef<{session: string; request: object} | null>(null)
  useEffect(() => {
    const request = props.openChecklistItem
    if (!request || !open || (externalJumpSeen.current?.session === session.id && externalJumpSeen.current.request === request)) return
    const frame = requestAnimationFrame(() => {
      externalJumpSeen.current = {session: session.id, request}
      if (!checklistNodes.current.has(JSON.stringify([session.id, request.id]))) {
        setChecklistNotice({session: session.id, text: 'This checklist item is no longer available.'})
        return
      }
      setChecklistNotice(null)
      setHighlight(null)
      openDoneFor(request.id)
      setTab('plan')
      writeRailTab('plan')
      setChecklistJump({session: session.id, id: request.id})
    })
    return () => cancelAnimationFrame(frame)
  }, [props.openChecklistItem, open, session.id, openDoneFor])
  // What a worker is on: its plan item (its checklist assignment) and the card, its started
  // session's own pin or the pinned ticket the item names.
  const workFor = (kind: 'subagent' | 'child_session', id: string, own?: ChildPin | null) => {
    const item = checklistAssignment(live.checklist?.items ?? [], kind, id) ?? null
    return { item, ticket: workTicket(item, tickets, own) }
  }
  const pinId = (t: { source: string; key: string; boardId?: string }) => `${t.source}|${t.boardId ?? ''}|${t.key}`
  // Who works on each pinned card, by name: the card's hovercard and its Live line say so.
  const workers = new Map<string, string>()
  // Two workers on one card: both names.
  const addWorker = (k: string, name: string) => workers.set(k, workers.has(k) ? `${workers.get(k)}, ${name}` : name)
  for (const s of live.subagents) {
    const w = workFor('subagent', s.childId || s.key)
    if (w.ticket) addWorker(pinId(w.ticket), s.title)
  }
  // Who works on a card right now (a running subagent, a started session at work): that card is in
  // progress, whatever its column says.
  const working = new Set<string>()
  for (const s of live.subagents) {
    const t = workFor('subagent', s.childId || s.key).ticket
    if (t) working.add(pinId(t))
  }
  for (const c of children) {
    const w = workFor('child_session', c.id, c.ticket)
    if (w.ticket) addWorker(pinId(w.ticket), c.title || 'a started session')
    if (w.ticket && childIsWorking(c)) working.add(pinId(w.ticket))
  }
  const pinViews = withWorkers(basePinViews, v => working.has(pinId(v.pin)))
  const shownPins = livePins(pinViews, waitOver !== null && waitOver === pendingKey)
  // A card the parent pinned opens its ticket; one only the started session pinned opens that
  // session (where its pin and its conversation are).
  const openChild = useContext(OpenSessionContext)
  const navigate = useNavigate()
  const openWorkTicket = (t: TicketSummaryLive, childId?: string) => {
    const mine = tickets.find(p => pinId(p) === pinId(t))
    if (mine) openTicket(mine)
    else if (childId) {
      if (mode === 'drawer') onClose()
      if (openChild) openChild(childId)
      else navigate(`/sessions/${encodeURIComponent(childId)}`)
    } else openTicket(t)
  }
  // A worker on a card: one more line on its row, "↳ <item> · #122" (the item opens it on Plan, the
  // key the ticket).
  const workLine = (kind: 'subagent' | 'child_session', id: string, worker: string, own?: ChildPin | null) => {
    const w = workFor(kind, id, own)
    if (!w.ticket) return undefined
    return <WorkLine item={w.item} ticket={w.ticket} worker={worker} sessionId={session.id}
      onItem={w.item?.id ? () => jumpChecklist(w.item!.id!) : undefined} onTicket={() => openWorkTicket(w.ticket!, kind === 'child_session' ? id : undefined)} />
  }
  // A Running now row's hovercard (card #129): where the session runs and what card each worker is on.
  const runBase = { engine: session.engine ?? '', runtime: session.runtime || 'daemon', model: session.model ?? '' }
  const cardLabel = (t: TicketSummaryLive | null | undefined) => (t ? `${t.key} ${t.title}` : undefined)
  const cardOf = (kind: 'subagent' | 'child_session', id: string, own?: ChildPin | null) => cardLabel(workFor(kind, id, own).ticket)
  // Who started it: a cron, a token or a parent by name; else the person, "you" for its owner (the
  // runner knows no account names, so another viewer reads "its owner").
  const mainStartedBy = session.started_by || session.cron_id ? startedByLabel(session) : live.canReleasePrompt === false ? 'its owner' : 'you'
  // The plan item a worker is assigned to: a link under a started session's row; on a Running now card
  // (glyph) a small ☰ whose name and tooltip are the item. A worker on a card has its work line instead.
  const assignmentReference = (kind: 'subagent' | 'child_session', id: string, glyph = false, own?: ChildPin | null) => {
    const { item, ticket } = workFor(kind, id, own)
    if (ticket) return glyph ? null : workLine(kind, id, children.find(c => c.id === id)?.title || 'a started session', own) ?? null
    if (!item?.id) return null
    if (glyph) return <button type="button" className="rail-run-glyph rail-run-plan rail-checklist-reference" data-testid="rail-checklist-reference" aria-label={`Plan · ${item.text}`} title={`Plan · ${item.text}`} onClick={() => jumpChecklist(item.id!)}>☰</button>
    return <button type="button" className="rail-link rail-checklist-reference" data-testid="rail-checklist-reference" onClick={() => jumpChecklist(item.id!)}>Plan · {item.text}</button>
  }

  const capabilitiesShown = open && tab === 'capabilities'
  const { onCapabilitiesShown } = props
  // Only a switch to Capabilities while the panel is open: opening the panel (on Capabilities, or on mount)
  // already has the page read the tool changes again.
  const wasOpen = useRef(false)
  useEffect(() => {
    const opened = open && !wasOpen.current
    wasOpen.current = open
    if (capabilitiesShown && !opened) onCapabilitiesShown?.()
  }, [capabilitiesShown, open, onCapabilitiesShown])
  // What the session loaded: the newest report in the transcript, and the server's once the tab has
  // been on screen. The tab shows it; the tab's header badge counts what is enabled in it.
  const capEvent = useTranscriptStore(s => latestCapabilitiesEvent(s.sessions[session.id]?.events))
  // A decided proposal may have added a permission ("Allow for this session" on its card): the
  // permissions block reads again when one lands.
  const decidedProposals = useTranscriptStore(s => (s.sessions[session.id]?.events ?? []).reduce((n, e) => n + (e.kind === 'proposal_decided' ? 1 : 0), 0))
  const capEvents = useMemo(() => (capEvent ? [capEvent] : NO_EVENTS), [capEvent])
  const capabilities = useCapabilities(session.id, capEvents, capabilitiesShown)
  const canManagePlugins = !session.archived_at && live.canReleasePrompt === true
  const daemonName = useSessionStore(s => s.daemons.find(d => d.id === session.daemon_id)?.name)
  const plugins = useSessionPlugins(session.id, canManagePlugins && open && (tab === 'live' || tab === 'capabilities'))
  const enabled = enabledCapabilityCount(capabilities?.payload)
  // On a phone the drawer goes first, so the file opens over the chat the person is looking at.
  const openFile = (id: string, commit?: string) => {
    if (mode === 'drawer') onClose()
    props.onOpenArtifact(id, commit)
  }
  // "± View diff" says the diff's counts: read once (the chat's cache) through the page's transport.
  const diffSummary = useMemo(() => {
    const files = blergTransport().files
    return files ? (id: string) => loadDiffSummary(session.id, id, x => files.raw(session.id, x)) : undefined
  }, [session.id])
  const attach = session.archived_at ? undefined : live.files?.attach
  const attachFiles = attach ? () => {
    if (mode === 'drawer') onClose()
    attach()
  } : undefined
  // What was typed in an ask's box, by ask: it outlives the drawer closing (the box goes with it).
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const draftFor = (eventId: string) => ({
    draft: drafts[eventId] ?? '',
    onDraft: (text: string) => setDrafts(d => ({ ...d, [eventId]: text })),
  })

  // Where the panel's hovercards read: the page's file transport, the session's list, its open
  // review requests, the board (a card read with the person's own permissions) and the workers.
  // The session's one board (#139): a card chip that names no board means it.
  const sessionBoard = useLoadSessionBoard(session.id, open)
  // Card references in the checklist (#157): confirmed on the session's one board, a link that
  // opens the ticket, and the card's state beside an item whose own state differs.
  const checklistTexts = useMemo(() => (live.checklist?.items ?? []).map(i => i.text), [live.checklist])
  const openTicketRef = useRef<((t: TicketSummaryLive) => void) | null>(null)
  const openRefCard = useCallback((t: CardTarget) => openTicketRef.current?.({ source: 'blerg-board', key: t.key, title: t.title ?? '', status: t.status ?? '', url: '', eventId: '', ...(t.boardId ? { boardId: t.boardId } : {}) } as TicketSummaryLive), [])
  const checklistRefs = useChecklistRefs(session.id, checklistTexts, sessionBoard, open, openRefCard)
  const hoverSources: HovercardSources = {
    files: live.files?.ops ?? blergTransport().files ?? null,
    artifact: (_s, id) => live.files?.items.find(a => a.id === id),
    reviewOpen: (_s, id) => reviews.some(r => r.artifact_id === id),
    card: (t: CardTarget) => cardPreview(t, session.id),
    worker: (t: CardTarget) => workers.get(pinId(t)),
    // A commit a checklist item names is in that item's published diff.
    diffFor: (_s, sha) => live.checklist?.items.find(i => i.diff && i.commits?.some(c => c.length >= 7 && sha.length >= 7 && (c.startsWith(sha) || sha.startsWith(c))))?.diff,
    repo: () => session.repo,
    account: signedInAccount,
  }
  // A commit chip with no diff of its own opens the published diff that holds the commit, else the
  // commit's diff generated on demand from the session's workspace.
  const openPublishedCommit = (s: string, sha: string) => {
    const diff = hoverSources.diffFor?.(s, sha)
    if (!diff) return false
    openFile(diff, sha)
    return true
  }

  const pendingProposals = props.proposals ?? []
  const waitingProposals = (props.waitingProposals ?? pendingProposals).length
  const needsCount = openAsks.length + (toolChanges > 0 ? 1 : 0) + waitingProposals + requests.length + reviews.length
  const needsRows = needsCount > 0 || pendingProposals.length > 0
  // A started session still running is running work too: it counts here and has its row (the same
  // compact row Started sessions lists it with), beside the subagents and background tasks.
  // The main agent counts while it works.
  const runningCount = (mainWorking ? 1 : 0) + live.subagents.length + runningTasks + (session.archived_at ? 0 : runningChildren.length)
  const sessionProgress = readProgress(live.progress)

  // A pinned ticket opened over the page (the current one in full, another by its summary).
  const [ticketOpen, setTicketOpen] = useState<{ ticket: Ticket; eventId: string } | null>(null)
  const openTicket = (t: TicketSummaryLive) => {
    const full = live.ticket && live.ticket.source === t.source && live.ticket.key === t.key && (live.ticket.board_id ?? '') === (t.boardId ?? '') ? live.ticket : null
    setTicketOpen({ ticket: full ?? { source: t.source, key: t.key, title: t.title, ...(t.boardId ? { board_id: t.boardId } : {}), ...(t.status ? { status: t.status } : {}), ...(t.url ? { url: t.url } : {}) }, eventId: t.eventId })
  }
  useEffect(() => { openTicketRef.current = openTicket })
  const planBody = (
    <>
      {tickets.length > 0 && (
        // Every pin of the plan, one compact line each with its state's glyph; the count of done in the
        // header, the checklist right under it.
        <Section title={tickets.length === 1 ? 'Ticket' : 'Tickets'} testId="rail-ticket"
          meta={<span data-testid="rail-ticket-done">{pinViews.filter(v => v.done).length} of {pinViews.length} done</span>}>
          <ul className="rail-list rail-pins">
            {pinViews.map(v => (
              <PinRow key={`${v.pin.source}:${v.pin.boardId ?? ''}:${v.pin.key}`} v={v} worker={workers.get(pinId(v.pin))} onOpen={openTicket}
                testId="rail-ticket-row" openTestId="rail-ticket-open" />
            ))}
          </ul>
        </Section>
      )}

      {live.checklist
        ? (
            <Section title="Checklist" testId="rail-checklist" meta={<ProgressMeta done={doneCount(live.checklist.items)} total={live.checklist.items.length} />}>
              {/* One line per item; the commit chip opens the diff the CLI published, in the chat's viewer. */}
              <PlanChecklist sessionId={session.id} items={live.checklist.items} onViewDiff={openFile} diffSummary={diffSummary} live={!session.archived_at && !session.ended_at && ['starting', 'running', 'idle', 'waiting'].includes(session.status)} highlightedId={highlightedId} onItemElement={onChecklistElement}
                doneOpen={doneOpen} onDoneOpen={setDoneOpen}
                assignees={item => (item.assignees ?? []).map(a => ({
                  kind: a.kind,
                  name: assigneeName(a, live.checklist?.workerTitles, live.subagents, children),
                  ...(a.kind === 'child_session' && children.find(c => c.id === a.id)?.engine ? { engine: children.find(c => c.id === a.id)!.engine } : {}),
                }))} />
            </Section>
          )
        : live.plan && (
            <Section title="Plan" testId="rail-plan" meta={<ProgressMeta done={doneCount(live.plan.items)} total={live.plan.items.length} text={planLabel(live.plan)} />}>
              <PlanItems plan={live.plan} />
            </Section>
          )}

      <DiffsSection diffs={diffs} sessionId={session.id} onOpen={openFile} />
    </>
  )

  const liveBody = (
    <>
      {shownPins.length > 0 && (
        <Section title={shownPins.length === 1 ? 'Ticket' : 'Tickets'} count={shownPins.length} testId="rail-pins">
          <PinLines views={shownPins} workerOf={v => workers.get(pinId(v.pin))} onOpen={openTicket} />
        </Section>
      )}
      {list && <PlanSummary list={list} onOpen={() => goPlan()} />}

      <Section title="Needs you" count={openAsks.length + toolChanges + waitingProposals + requests.length + reviews.length} tone="attention" testId="rail-needs" empty={props.proposalsError ? undefined : "Nothing needs you."}>
        {(props.proposalsError || needsRows) && <>
        {props.proposalsError && <p role="alert">{props.proposalsError} <button type="button" onClick={props.onProposalsChanged}>Retry proposals</button></p>}
        {needsRows && (
          <ul className="rail-list">
            {pendingProposals.map(p => <li className="rail-item" key={p.id}><PendingProposal proposal={p} refresh={() => props.onProposalsChanged?.()} /></li>)}
            {reviews.map(r => <ReviewRequestRow key={r.request_id} review={r} onOpen={() => props.onOpenArtifact(r.artifact_id)}
              onDismiss={() => dismissWithUndo(`review:${r.request_id}`, `Review: ${r.artifact_name}`)} />)}
            {requests.map(r => <li key={r.request_id} className="rail-item"><div className="rail-item-title"><span className="rail-glyph" aria-hidden="true" title="Can start sessions" data-testid="rail-child-approval-glyph">{GRANT_GLYPH.child}</span> Approve started session: {r.title || r.engine}</div><DelegationLiveReview sessionId={session.id} requestId={r.request_id} /></li>)}
            {openAsks.map(a => (
              <AskRow
                key={a.eventId}
                ask={a}
                now={now}
                // Only the owner answers a no-wait question; while that is not known (the panel reads
                // the chat's own state) the box is offered and the server decides.
                answer={session.archived_at || (a.noWait && live.canReleasePrompt === false) ? undefined : live.answer}
                {...draftFor(a.eventId)}
                missing={missing === a.eventId}
                onShow={() => jump(a.eventId, () => scrollToEvent(a.eventId))}
                // Whoever may answer it may dismiss it (they may have answered in the chat); never an
                // engine command approval.
                onDismiss={canDismissAsk(a, live.canReleasePrompt, !!session.archived_at)
                  ? () => dismissWithUndo(`ask:${a.messageId}`, `Question: ${a.question.slice(0, 80)}`) : undefined}
              />
            ))}
            {toolChanges > 0 && (
              <li className="rail-item" data-testid="rail-tool-changes">
                <div className="rail-item-title">
                  {toolChanges} tool {toolChanges === 1 ? 'change waits' : 'changes wait'} on your decision
                </div>
                <div className="rail-item-line">An MCP server changed or added a tool this session uses; it is off until you decide.</div>
                <div className="rail-actions">
                  <Link className="rail-link" to={`/proposals?session=${encodeURIComponent(session.id)}`}>Review on the Proposals page</Link>
                </div>
              </li>
            )}
          </ul>
        )}
        </>}
      </Section>

      {/* Main (the agent you talk to) is pinned first; its subagents and background commands run
          inside it and are indented under it; a started session runs on its own, at Main's level. */}
      {!session.archived_at && <Section title="Running now" count={runningCount} tone="running" testId="rail-running" empty={session.status === 'idle' ? 'Idle.' : session.status === 'waiting' ? 'Waiting on you.' : 'Nothing is running.'} id={`${props.id}-running`}>
        {(mainStep || (mainResting && sessionProgress) || live.subagents.length + tasks.length + runningChildren.length > 0) && (
          <ul className="rail-list">
            {mainShown && <MainRow step={mainStep} engine={session.engine} waiting={session.status === 'waiting'} progress={sessionProgress} now={mainNow}
              base={runBase} card={cardLabel(shownPins.find(v => !v.done)?.pin)} startedBy={mainStartedBy}
              missing={missing === 'main'} onShow={() => jump('main', showConversationEnd)} />}
            {live.subagents.map(s => (
              <SubagentRow
                key={s.key}
                nested={mainShown}
                sub={s}
                sessionId={session.id}
                reasoningTokens={live.reasoningTokens === true}
                sessionEffort={session.effort}
                assignment={assignmentReference('subagent', s.childId || s.key, true)}
                work={workLine('subagent', s.childId || s.key, s.title)}
                base={runBase} card={cardOf('subagent', s.childId || s.key)}
                now={now}
                missing={missing === `sub:${s.key}`}
                onShow={() => jump(`sub:${s.key}`, () => scrollToCall(s.key))}
              />
            ))}
            {tasks.map(t => (
              <TaskRow
                key={t.taskId}
                nested={mainShown}
                t={t}
                base={runBase}
                now={now}
                missing={missing === `task:${t.taskId}`}
                onShow={() => jump(`task:${t.taskId}`, () => scrollToCall(t.callId))}
                onDismiss={() => bg.dismiss(t.taskId)}
              />
            ))}
            {runningChildren.map(child => <ChildSessionRow key={child.id} variant="run" child={reasoningChild(child, now)} now={now} parentId={session.id} assignment={assignmentReference('child_session', child.id, true, child.ticket)}
              work={workLine('child_session', child.id, child.title || 'a started session', child.ticket)}
              card={cardOf('child_session', child.id, child.ticket)} />)}
          </ul>
        )}
      </Section>}

      <StartedSessions readOnly={!!session.archived_at} sessionId={session.id} children={children} now={now} assignmentFor={id => assignmentReference('child_session', id, false, children.find(c => c.id === id)?.ticket)}
        onShowRunning={session.archived_at ? undefined : () => {
          const running = document.getElementById(`${props.id}-running`)
          running?.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
          running?.focus({ preventScroll: true })
        }} />
      {!session.archived_at && <SessionPermissions sessionId={session.id} active={open && tab === 'live'} delegationGrants={props.delegationGrants ?? []} refreshKey={[...(props.delegationGrants ?? []).map(g => g.id), String(decidedProposals)].join(',')} openRequest={open && tab === 'live' ? props.openChildApproval : undefined} onOpenRequestTaken={props.onChildApprovalShown} />}
      <Section title="Session" testId="rail-session">
        <SessionFacts session={session} machine={props.machine} now={minute} liveRunning={props.liveRunning ?? null} />
        {!session.archived_at && session.limit_resume_supported === true &&
          <div className="rail-settings"><UsageLimitResume key={session.id} sessionId={session.id} /></div>}
      </Section>
    </>
  )

  const body = (
    <>
      {checklistNotice?.session === session.id && <p className="rail-checklist-unavailable" role="status">{checklistNotice.text}</p>}
      <RailTabs idBase={props.id} tab={tab} onPick={pick} plan={planShown ? (planTabLabel(list, tickets.length, diffs.length) ?? '') : null} needs={openAsks.length + toolChanges + waitingProposals + requests.length} files={live.files ? fileCount(live.files.items) : 0} enabled={enabled} />
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-live`} aria-labelledby={`${props.id}-tab-live`} hidden={tab !== 'live'}>
        {liveBody}
      </div>
      {planShown && (
        <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-plan`} aria-labelledby={`${props.id}-tab-plan`} hidden={tab !== 'plan'}>
          {planBody}
        </div>
      )}
      {/* Kept mounted while Live shows, so its list, opened cards and read state survive a switch;
          it reads the server and moves its clock only while on screen. */}
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-history`} aria-labelledby={`${props.id}-tab-history`} hidden={tab !== 'history'}>
        <HistoryTab sessionId={session.id} active={open && tab === 'history'} filesKey={live.filesKey} missing={missing} onJump={jump} onOpenArtifact={openFile}
          progress={list && list.total > 0 ? { done: list.done, total: list.total } : null} />
      </div>
      {/* Files and Capabilities, kept mounted the same way: opened versions, a filter, opened groups. */}
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-files`} aria-labelledby={`${props.id}-tab-files`} hidden={tab !== 'files'}>
        <FilesTab sessionId={session.id} active={open && tab === 'files'} files={live.files} onOpenArtifact={openFile} onAttach={attachFiles} />
      </div>
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-capabilities`} aria-labelledby={`${props.id}-tab-capabilities`} hidden={tab !== 'capabilities'}>
        <CapabilitiesTab report={capabilities} pendingToolChanges={toolChanges} liveMcp={live.mcp ?? null} startedBySession={session.started_by?.kind === 'session'}
          plugins={canManagePlugins ? plugins : null} runtime={session.runtime} machine={daemonName} />
      </div>
    </>
  )

  const ticketModal = ticketOpen && (
    <PinnedTicketModal
      ticket={ticketOpen.ticket}
      sessionId={session.id}
      worker={workers.get(pinId({ source: ticketOpen.ticket.source, key: ticketOpen.ticket.key, boardId: ticketOpen.ticket.board_id }))}
      onClose={() => setTicketOpen(null)}
      onShow={() => jump(ticketOpen.eventId, () => {
        const found = scrollToEvent(ticketOpen.eventId)
        if (found) setTicketOpen(null)
        return found
      })}
    />
  )

  const hovered = (
    <HovercardProvider value={hoverSources}>
      <SessionBoardContext.Provider value={sessionBoard}>
        <ChecklistRefsContext.Provider value={checklistRefs}>
          <CommitViewerHost files={hoverSources.files} repo={() => session.repo} opened={openPublishedCommit}>{body}</CommitViewerHost>
        </ChecklistRefsContext.Provider>
      </SessionBoardContext.Provider>
    </HovercardProvider>
  )
  if (mode === 'drawer') return open ? <Drawer id={props.id} onClose={onClose}>{hovered}{ticketModal}</Drawer> : null
  return (
    // data-hc-boundary: a hovercard of a chip in the panel opens beside the panel (to its left),
    // never over the rows of its list.
    <aside id={props.id} className="session-rail panel" aria-label="Session panel" data-testid="session-rail" data-hc-boundary="" hidden={!open}>
      {/* No close button of its own: the header's panel toggle, right above, opens and closes it. */}
      <header className="rail-header">
        <h2 className="rail-heading">Session panel</h2>
      </header>
      <div className="rail-body">{hovered}</div>
      {ticketModal}
    </aside>
  )
}

/** What can take the focus in the drawer, the tabs not on screen left out. */
function shown(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(n => !n.closest('[hidden]'))
}

/** A narrow screen: the panel slides over the chat from the right, over a dimmed page. */
function Drawer({ id, onClose, children }: { id: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLElement | null>(null)
  const titleId = useId()
  const backdrop = useBackdropClose(onClose)

  // Focus moves in on open and back to whatever had it (the toggle) on close; the page behind does
  // not scroll while it is open.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    if (ref.current) shown(ref.current)[0]?.focus()
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = overflow
      opener?.focus?.({ preventScroll: true })
    }
  }, [])

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
      return
    }
    if (e.key !== 'Tab' || !ref.current) return
    const nodes = shown(ref.current)
    if (nodes.length === 0) return
    const first = nodes[0]
    const last = nodes[nodes.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }

  return createPortal(
    <div className="rail-overlay" data-testid="session-rail-backdrop" {...backdrop}>
      <aside
        ref={ref}
        id={id}
        className="session-rail drawer"
        data-hc-boundary=""
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="session-rail"
        onKeyDown={onKeyDown}
      >
        <header className="rail-header">
          <h2 className="rail-heading" id={titleId}>Session panel</h2>
          <button type="button" className="rail-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="rail-body">{children}</div>
      </aside>
    </div>,
    document.body,
  )
}

const ALL_TABS = RAIL_TABS
const TAB_LABEL: Record<RailTab, string> = { live: 'Live', plan: 'Plan', history: 'History', files: 'Files', capabilities: 'Capabilities' }

/** Live · Plan · History · Files · Capabilities (Plan only when there is one), with the arrow keys, Home and End moving between them (the tabs pattern). */
function RailTabs({ idBase, tab, onPick, plan, needs, files, enabled }: { idBase: string; tab: RailTab; onPick: (t: RailTab) => void; plan: string | null; needs: number; files: number; enabled: number }) {
  const TABS = ALL_TABS.filter(t => t !== 'plan' || plan !== null)
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const at = TABS.indexOf(tab)
    const to: Record<string, number> = { ArrowRight: (at + 1) % TABS.length, ArrowLeft: (at + TABS.length - 1) % TABS.length, Home: 0, End: TABS.length - 1 }
    if (!(e.key in to)) return
    e.preventDefault()
    const next = TABS[to[e.key]]
    onPick(next)
    document.getElementById(`${idBase}-tab-${next}`)?.focus()
  }
  return (
    <div className="rail-tabs" role="tablist" aria-label="Session panel views" onKeyDown={onKeyDown}>
      {TABS.map(t => (
        <button
          key={t}
          type="button"
          role="tab"
          id={`${idBase}-tab-${t}`}
          className={`rail-tab${tab === t ? ' on' : ''}`}
          aria-selected={tab === t}
          aria-controls={`${idBase}-panel-${t}`}
          tabIndex={tab === t ? 0 : -1}
          onClick={() => onPick(t)}
        >
          {TAB_LABEL[t]}
          {/* What needs the person, on Live: a question shows from History too. */}
          {t === 'live' && needs > 0 && (
            <>
              <span className="rail-count attention rail-tab-count" aria-hidden="true" data-testid="rail-tab-count">{needs}</span>
              <span className="rail-sr">, {needs} {needs === 1 ? 'needs' : 'need'} you</span>
            </>
          )}
          {/* Done of total (or a count of tickets and diffs), on Plan. */}
          {t === 'plan' && plan && (
            <>
              <span className="rail-count rail-tab-count" aria-hidden="true" data-testid="rail-plan-tab-count">{plan}</span>
              <span className="rail-sr">, {plan}</span>
            </>
          )}
          {/* How many files the session holds (names, not versions), on Files, to draw the eye there. */}
          {t === 'files' && files > 0 && (
            <>
              <span className="rail-count rail-tab-count" aria-hidden="true" data-testid="rail-files-tab-count">{files}</span>
              <span className="rail-sr">, {files} {files === 1 ? 'file' : 'files'}</span>
            </>
          )}
          {/* What the session has switched on, on Capabilities. */}
          {t === 'capabilities' && enabled > 0 && (
            <>
              <span className="rail-count rail-tab-count" aria-hidden="true" data-testid="rail-capabilities-tab-count">{enabled}</span>
              <span className="rail-sr">, {enabled} enabled</span>
            </>
          )}
        </button>
      ))}
    </div>
  )
}

function Section({ title, count, meta, tone, testId, empty, children, id }: {
  title: string
  /** An id to bring the section into view by (Started sessions' "N running ↑"). */
  id?: string
  count?: number
  /** Words beside the title in place of a count ("3 of 7 done"). */
  meta?: ReactNode
  /** How a count above zero is coloured: something waits on the person, or something runs. */
  tone?: 'attention' | 'running'
  testId: string
  empty?: string
  children?: ReactNode
}) {
  const headId = useId()
  const hasContent = !!children
  return (
    <section className="rail-section" aria-labelledby={headId} data-testid={testId} id={id} tabIndex={id ? -1 : undefined}>
      <h3 className="rail-section-head" id={headId}>
        <span>{title}</span>
        {count !== undefined && <span className={`rail-count${count > 0 && tone ? ` ${tone}` : ''}`} data-testid={`${testId}-count`}>{count}</span>}
        {meta && <span className="rail-section-meta" data-testid={`${testId}-meta`}>{meta}</span>}
      </h3>
      {hasContent ? children : empty && <p className="rail-empty">{empty}</p>}
    </section>
  )
}

/** Live's one line about the plan: the list ("Checklist · 6 of 8", a short bar, the next item, ›)
 *  that opens the Plan tab. The pins are listed above it (PinLines). */
function PlanSummary({ list, onOpen }: { list: PlanList; onOpen: () => void }) {
  const all = list.total > 0 && list.done >= list.total
  const head = all ? `${list.kind} · all ${list.total} done` : `${list.kind} · ${list.done} of ${list.total}`
  return (
    <div className="rail-plan-summary" data-testid="rail-plan-summary">
      {/* The line opens Plan on a click anywhere; its control is the head (the keyboard's). The next
          item's text may hold a card link (#157), so the line itself is not a button. */}
      <div className="rail-plan-summary-list" title={planSummary(list)} onClick={onOpen}>
        <span className="rail-plan-summary-text" data-testid="rail-plan-summary-text">
          <button type="button" className="rail-plan-summary-head" data-testid="rail-plan-summary-open" onClick={e => { e.stopPropagation(); onOpen() }}>{head}</button>
          <Progress done={list.done} total={list.total} />
          {!all && (list.doing?.length || list.next) && (
            <span className="rail-plan-summary-next">
              {list.doing?.length
                ? <> · Doing: {list.doing.map((d, i) => <span key={i}>{i > 0 ? '; ' : ''}<CardRefText text={d} /><CardRefStates text={d} state="doing" /></span>)}</>
                : <> · Next: <CardRefText text={list.next ?? ''} /><CardRefStates text={list.next ?? ''} state="todo" /></>}
            </span>
          )}
        </span>
        <span className="rail-plan-summary-go" aria-hidden="true">›</span>
      </div>
    </div>
  )
}

/** A card's hovercard target from a pin (and what Live knows of it). */
function cardTarget(t: TicketSummaryLive, title: string, status: string, worker?: string): CardTarget {
  return { kind: 'card', source: t.source, key: t.key, ...(t.boardId ? { boardId: t.boardId } : {}), title, status, ...(worker ? { worker } : {}) }
}

/** Live's pins: the ones in progress, one compact line each, newest first. The first PINS_SHOWN
 *  show, "+N more" the rest in place. */
function PinLines({ views, workerOf, onOpen }: { views: PinView[]; workerOf: (v: PinView) => string | undefined; onOpen: (t: TicketSummaryLive) => void }) {
  const [all, setAll] = useState(false)
  const shown = all ? views : views.slice(0, PINS_SHOWN)
  const rest = views.length - PINS_SHOWN
  return (
    <ul className="rail-list rail-pins" data-testid="rail-pin-list">
      {shown.map(v => <PinRow key={`${v.pin.source}:${v.pin.boardId ?? ''}:${v.pin.key}`} v={v} worker={workerOf(v)} onOpen={onOpen} testId="rail-pin" openTestId="rail-pin-open" />)}
      {rest > 0 && (
        <li className="rail-pin-more">
          <button type="button" className="rail-link" data-testid="rail-pin-more" aria-expanded={all} onClick={() => setAll(a => !a)}>{all ? 'Show fewer' : `+${rest} more`}</button>
        </li>
      )}
    </ul>
  )
}

/** One pin on one line, the same on Live and Plan: its state as one glyph (✓ done, ◐ in progress,
 *  ○ not started, all one weight; the words in its tooltip, its name and the hovercard), the key,
 *  the title (cut), ↳ when a worker is on it. A line opens the ticket; a hover previews it (which
 *  also says when the card was read live from the board). */
function PinRow({ v, worker, onOpen, testId, openTestId }: { v: PinView; worker?: string; onOpen: (t: TicketSummaryLive) => void; testId: string; openTestId: string }) {
  const sessionBoard = useSessionBoard()
  const state = STATE_GLYPH[v.state]
  const words = `${state.words}${v.status && v.status.toLowerCase() !== state.words.toLowerCase() ? ` (${v.status})` : ''}`
  const label = `${v.pin.key} ${v.title}, ${words}${worker ? `, ${worker} works on it` : ''}`
  return (
    <li className={`rail-pin${v.done ? ' done' : ''}`} data-testid={testId} data-ticket-key={v.pin.key} data-state={v.state}
      data-done={v.done ? 'true' : undefined} data-live={v.live ? 'true' : undefined}>
      <Hover target={cardTarget(withSessionBoard(v.pin, sessionBoard), v.title, v.status, worker)}>
        <button type="button" className="rail-pin-open" data-testid={openTestId} aria-label={label} onClick={() => onOpen(v.pin)}>
          <span className={`rail-pin-state ${v.state}${v.pending ? ' pending' : ''}`} aria-hidden="true" title={v.pending ? `${words}, as pinned; reading the board` : words} data-testid="rail-pin-state" data-pending={v.pending ? 'true' : undefined}>{state.glyph}</span>
          <span className="ticket-key rail-pin-key">{v.pin.key}</span>
          <span className={`rail-pin-title${statusTone(v.status) === 'canceled' ? ' canceled' : ''}`}>{v.title}</span>
        </button>
      </Hover>
      {worker && <span className="rail-run-glyph rail-pin-worker" role="img" aria-label={`Worker: ${worker}`} title={`Worker: ${worker}`} data-testid="rail-pin-worker">↳</span>}
    </li>
  )
}

/** A worker's card on its Running now row: "↳ <plan item> · #122" (the item opens it on Plan, the
 *  key opens the ticket and previews it on hover). */
function WorkLine({ item, ticket, worker, sessionId, onItem, onTicket }: {
  item: ChecklistItem | null
  ticket: TicketSummaryLive
  worker: string
  sessionId: string
  onItem?: () => void
  onTicket: () => void
}) {
  const view = useTicketView(ticket, sessionId)
  const sessionBoard = useSessionBoard()
  const text = item?.text || view.title
  return (
    <span className="rail-work" data-testid="rail-work">
      <span className="rail-work-arrow" aria-hidden="true">↳</span>
      {onItem
        ? <button type="button" className="rail-work-item" data-testid="rail-work-item" title={`Plan · ${text}`} onClick={onItem}>{text}</button>
        : <span className="rail-work-item" title={text}>{text}</span>}
      <span className="rail-work-sep" aria-hidden="true">·</span>
      <Hover target={cardTarget(withSessionBoard(ticket, sessionBoard), view.title, view.status, worker)}>
        <button type="button" className="ticket-key rail-work-key" data-testid="rail-work-ticket" aria-label={`Open ${ticket.key}: ${view.title}`} onClick={onTicket}>{ticket.key}</button>
      </Hover>
    </span>
  )
}

/** The diffs the session published, newest first: a row each, opening the viewer. */
function DiffsSection({ diffs, sessionId, onOpen }: { diffs: ArtifactInfo[]; sessionId: string; onOpen: (id: string) => void }) {
  if (diffs.length === 0) return null
  return (
    <Section title="Diffs" count={diffs.length} testId="rail-diffs">
      <ul className="rail-list">
        {diffs.map(d => {
          const at = d.created_at ? new Date(d.created_at) : null
          return (
            <li key={d.id} className="rail-item rail-diff" data-testid="rail-diff-row">
              <Hover target={{ kind: 'file', sessionId, id: d.id, artifact: d }}>
                <button type="button" className="rail-link rail-diff-open" onClick={() => onOpen(d.id)}>{d.name}</button>
              </Hover>
              {at && !Number.isNaN(at.getTime()) && (
                <time className="rail-diff-time" dateTime={at.toISOString()}>{at.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}</time>
              )}
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

/** A short rounded bar: how much of the list is done (green when all of it is). */
/** A section header's words beside a short bar. */
function ProgressMeta({ done, total, text }: { done: number; total: number; text?: string }) {
  return <><span>{text ?? progressText(done, total)}</span><Progress done={done} total={total} /></>
}

function Progress({ done, total }: { done: number; total: number }) {
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  return (
    <div className={`rail-progress${total > 0 && done >= total ? ' complete' : ''}`} role="progressbar" aria-label={progressText(done, total)} aria-valuemin={0} aria-valuemax={total} aria-valuenow={done}>
      <div className="rail-progress-fill" style={{ width: `${pct}%` }} />
    </div>
  )
}

const PLAN_GLYPH = { completed: '☑', in_progress: '◐', pending: '☐' } as const

/** The engine's plan: its items with their state (read-only). */
function PlanItems({ plan }: { plan: PlanLive }) {
  return (
    <ul className="checklist-items rail-plan-items">
      {plan.items.map((it, i) => (
        <li key={i} className={it.status === 'completed' ? 'done' : it.status === 'in_progress' ? 'now' : undefined}>
          <span className="checklist-box" aria-hidden="true">{PLAN_GLYPH[it.status] ?? '☐'}</span>
          <span className="checklist-text">{it.status === 'in_progress' && it.active ? it.active : it.text}</span>
          <span className="rail-sr">{it.status === 'completed' ? ' (done)' : it.status === 'in_progress' ? ' (in progress)' : ' (to do)'}</span>
        </li>
      ))}
    </ul>
  )
}

function AskRow({ ask, now, answer, draft, onDraft, missing, onShow, onDismiss }: {
  ask: AskLive
  now: number
  answer?: LiveState['answer']
  draft: string
  onDraft: (text: string) => void
  missing: boolean
  onShow: () => void
  onDismiss?: () => void
}) {
  return (
    <li className="rail-item rail-ask" data-testid="rail-ask" data-no-wait={ask.noWait ? 'true' : undefined}>
      <div className="rail-item-kicker">
        <span>Question</span>
        {/* A question the agent did not wait on: it keeps working, and the answer is sent to it. */}
        {ask.noWait && <span className="rail-ask-nowait" title="The agent keeps working; your answer is sent to it as a message">not blocking</span>}
        <span className="rail-wait" data-testid="rail-ask-wait">{waitLabel(ask.askedAt, now)}</span>
      </div>
      <div className="rail-ask-text">{ask.question}</div>
      {answer
        ? <AskReply answer={answer} messageId={ask.messageId || undefined} label="Your answer" className="rail-reply" draft={draft} onDraft={onDraft} />
        : <div className="rail-item-line">{ask.noWait ? 'Only the session owner can answer this question.' : 'Answer it from the message box once the session can take a message.'}</div>}
      <div className="rail-actions">
        <ShowLink onShow={onShow} missing={missing} />
        {onDismiss && <DismissButton what={`question: ${ask.question}`} onDismiss={onDismiss} />}
      </div>
    </li>
  )
}

/** Dismiss: close it without deciding or answering (the person may have answered in the chat). */
function DismissButton({ what, onDismiss }: { what: string; onDismiss: () => void }) {
  return (
    <button type="button" className="rail-link rail-dismiss" data-testid="rail-dismiss" onClick={onDismiss}
      aria-label={`Dismiss ${what.length > 80 ? `${what.slice(0, 79)}…` : what}`}
      title="Dismiss: you answered elsewhere, or it no longer needs you. The agent is told.">
      Dismiss
    </button>
  )
}

function WorkProgress({ progress, testId }: { progress: LiveProgress; testId?: string }) {
  const valid = readProgress(progress)
  if (!valid) return null
  return <div className="rail-work-progress" data-testid={testId}>
    <span className="rail-work-count">{valid.completed}/{valid.total}</span>
    <Progress done={valid.completed} total={valid.total} />
    {valid.label && <span className="rail-work-label" title={valid.label}>{valid.label}</span>}
  </div>
}

type RunBase = { engine?: string; runtime?: string; model?: string }

function SubagentRow({ sub, now, missing, onShow, assignment, work, sessionId, reasoningTokens, sessionEffort, nested, base, card }: { sessionId: string; sub: ProgressSubagent; now: number; missing: boolean; onShow: () => void; assignment?: ReactNode; work?: ReactNode; reasoningTokens: boolean; sessionEffort?: string; nested: boolean; base: RunBase; card?: string }) {
  const model = modelDisplayName(sub.model)
  const observed = useTranscriptStore(s => s.sessions[sessionId]?.reasoning?.[sub.key])
  const shown = observed?.active === false ? null : observed ?? sub.reasoning
  // Every viewer of a shared session gets the live events; only the owner (or an administrator)
  // sees the count, the way the server's /live reply hides it. Anyone else sees the clock.
  const reasoning = shown && !reasoningTokens ? { ...shown, tokens: null } : shown
  const doing = reasoning ? reasoningLabel(reasoning, now) : sub.doing
  // A subagent that reports no effort of its own runs at the session's: say so, never "unknown".
  const effort = `effort: ${sub.effort || (sessionEffort ? `${sessionEffort} (session's)` : 'unknown')}`
  const tooltip = [sub.title, [sub.background ? 'in the background' : 'running', sub.resumed ? 'resumed' : '', sub.type, model, effort].filter(Boolean).join(' · ')].join('\n')
  const tokens = sub.tokens !== null && sub.tokens > 0 ? tokenLabel(sub.tokens) : ''
  const hover = runTarget({
    title: sub.title, kind: ['Subagent', sub.type, sub.background ? 'in the background' : '', sub.resumed ? 'resumed' : ''].filter(Boolean).join(' · '),
    engine: base.engine, runtime: base.runtime, modelAs: subagentModelFact(sub.model || undefined, base.model || undefined), state: `${sub.background ? 'Running in the background' : 'Running'} · ${effort}`,
    step: doing || undefined, elapsed: sub.startedAt !== null ? clockLabel(now - sub.startedAt) : undefined, tokens: tokens || undefined, card, startedBy: 'Main',
  })
  return (
    // A background subagent is drawn dashed, like its card in the transcript.
    <RunCard testId="rail-subagent" nested={nested} className={`rail-sub${sub.background ? ' background' : ''}`} data={{ key: sub.key, status: sub.background ? 'background' : 'running' }}
      state={<span className="rail-spin" role="img" aria-label={sub.background ? 'Running in the background' : 'Running'} />}
      title={<><span className="rail-sub-icon" role="img" aria-label="Subagent">🤖</span> {sub.title}</>}
      tooltip={tooltip} action="Show in conversation" onActivate={onShow}
      elapsed={sub.startedAt !== null ? <span className="rail-clock" data-testid="rail-elapsed">{clockLabel(now - sub.startedAt)}</span> : undefined}
      step={sub.progress
        ? <><WorkProgress progress={sub.progress} />{doing && <RunStep text={doing} testId="rail-sub-doing" />}</>
        : doing ? <RunStep text={doing} testId="rail-sub-doing" /> : undefined}
      glyphs={<>
        {sub.resumed && <RunGlyph label="Resumed" testId="rail-sub-resumed">↻</RunGlyph>}
        {model && <RunGlyph label={`Model: ${model}`} testId="rail-sub-model">{model.charAt(0).toUpperCase()}</RunGlyph>}
        {tokens && <RunGlyph label={tokens} testId="rail-sub-tokens" className="tokens">{tokens.replace(/ tokens?$/, '')}</RunGlyph>}
        {assignment}
      </>}
      work={work} hover={hover}
      after={missing ? <ShowMissing /> : undefined} />
  )
}

/** The Main card's tooltip and accessible description: what it is, and how the rows under it read. */
const MAIN_TOOLTIP = "The agent you're talking to. Indented rows run inside it; child sessions run on their own."

/** The end of the conversation, where the working line is: brought into view (the working line
 *  flashes when it is there). False when the chat is not on the page. */
function showConversationEnd(): boolean {
  const timeline = document.querySelector<HTMLElement>('.agent-timeline')
  // A chat that is not on screen (hidden behind another view) is not shown: say so.
  if (!timeline || timeline.offsetParent === null) return false
  timeline.scrollTo?.({ top: timeline.scrollHeight, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
  const line = document.querySelector('[data-testid="working"]')
  if (line) {
    line.classList.add('bg-flash')
    setTimeout(() => line.classList.remove('bg-flash'), 1600)
  }
  return true
}

/** The main agent's card, first in Running now: "Main" and its engine's mark; while it works a
 *  spinner, the turn's clock and its current step from the working line (the call it waits on,
 *  with that call's clock, or what it is on); otherwise Zz (idle) or ? (waiting on you). The
 *  session's own progress bar, when the engine reports one, is on its step line. */
function MainRow({ step, engine, waiting, progress, now, missing, onShow, base, card, startedBy }: {
  base: RunBase
  card?: string
  startedBy: string
  step: MainStep | null
  engine?: string
  waiting: boolean
  progress: LiveProgress | null
  now: number
  missing: boolean
  onShow: () => void
}) {
  const id = engine || 'claude'
  const name = ENGINE_LABELS[id] ?? id
  const stateWord = step ? 'Working' : waiting ? 'Waiting on you' : 'Idle'
  const tooltip = [`Main · ${name} · ${step ? step.text : stateWord}`, MAIN_TOOLTIP].join('\n')
  const stepLine = step && <span className="rail-main-step" data-testid="rail-main-step">
    <RunStep text={step.text} testId="rail-main-doing" />
    {step.stepSince !== undefined && now >= step.stepSince && <span className="rail-clock" data-testid="rail-main-step-elapsed" title="How long this call has run">{runningLabel(Math.max(0, now - step.stepSince))}</span>}
  </span>
  const hover = runTarget({
    title: 'Main', kind: "Main agent (the one you're talking to)", engine: base.engine, runtime: base.runtime, model: base.model, state: stateWord,
    step: step?.text, elapsed: step && step.since !== null && now >= step.since ? runningLabel(Math.max(0, now - step.since)) : undefined,
    card, startedBy,
  })
  return (
    <RunCard testId="rail-main" actionTestId="rail-main-show" hover={hover} className="rail-main" data={{ state: step ? 'working' : waiting ? 'waiting' : 'idle' }}
      state={step ? <span className="rail-spin" role="img" aria-label="Working" />
        : <span className={`rail-run-idle${waiting ? ' attention' : ''}`} role="img" aria-label={stateWord} title={stateWord}>{waiting ? '?' : 'Zz'}</span>}
      title={<span data-testid="rail-main-title">Main</span>}
      tooltip={tooltip} action="Show the end of the conversation" onActivate={onShow}
      elapsed={step && step.since !== null && now >= step.since ? <span className="rail-clock" data-testid="rail-elapsed" title="This turn">{runningLabel(Math.max(0, now - step.since))}</span> : undefined}
      step={progress || stepLine
        ? <>{progress && <WorkProgress progress={progress} testId="rail-session-progress" />}{stepLine}</>
        : undefined}
      glyphs={<RunGlyph label={`Engine: ${name}`} testId="rail-main-engine">{ENGINE_SHORT[id] ?? name.charAt(0)}</RunGlyph>}
      after={missing ? <span className="rail-missing" role="status">The conversation is not on screen.</span> : undefined} />
  )
}

function TaskRow({ t, now, missing, onShow, onDismiss, nested, base }: {
  base: RunBase
  nested: boolean
  t: BackgroundTask
  now: number
  missing: boolean
  onShow: () => void
  onDismiss: () => void
}) {
  const failed = taskFailed(t)
  const state = !t.done ? 'running' : failed ? 'failed' : 'done'
  const shell = isShell(t)
  // A command: what it is for as the title when it says so, the command itself on line 2. An agent
  // task: its description; its result on line 2 once it ends.
  const note = shell && t.command && t.description && t.description !== t.command ? t.description : ''
  const started = t.startedAt !== null ? `started ${new Date(t.startedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}` : ''
  const kind = shell ? (t.kind === 'monitor' ? 'watcher' : 'shell') : ''
  const summary = t.done && t.done.summary ? t.done.summary : ''
  const tooltip = [titleOf(t), [kind, state, started].filter(Boolean).join(' · '), summary].filter(Boolean).join('\n')
  const elapsed = elapsedOf(t, now)
  // What a tap on the step line shows whole: what it is for, the command, and its result.
  const whole = [note, shell ? commandOf(t) : '', summary].filter(Boolean).join('\n\n')
  const hover = runTarget({
    title: titleOf(t), kind: shell ? (t.kind === 'monitor' ? 'Background watcher' : 'Background command') : 'Background task',
    runtime: base.runtime, state: [state, started].filter(Boolean).join(' · '),
    step: whole || undefined, elapsed: elapsed || undefined, startedBy: 'Main',
  })
  return (
    <RunCard testId="rail-task" nested={nested} className={`rail-task${shell ? ' rail-shell' : ''} ${state}`} data={{ 'task-id': t.taskId }}
      state={t.done ? <StatusGlyph t={t} /> : <span className="rail-spin" role="img" aria-label="Running" />}
      title={shell ? (note ? <span data-testid="rail-task-note">{note}</span> : <code className="rail-cmd" data-testid="rail-command">{commandOf(t)}</code>) : (t.description || 'Background task')}
      tooltip={tooltip} action="Show in conversation" onActivate={onShow}
      elapsed={elapsed ? <span className="rail-clock" data-testid="rail-elapsed">{elapsed}</span> : undefined}
      controls={failed ? <button type="button" className="rail-run-dismiss" data-testid="rail-dismiss" aria-label="Dismiss" title="Dismiss" onClick={onDismiss}>×</button> : undefined}
      step={summary
        ? <RunStep text={summary} full={whole} testId="rail-task-summary" className={failed ? 'bad' : undefined} />
        : note ? <RunStep text={commandOf(t)} full={whole} testId="rail-command" className="rail-cmd" /> : undefined}
      glyphs={<KindIcon t={t} />} hover={hover}
      after={missing ? <ShowMissing /> : undefined} />
  )
}

function SessionFacts({ session, machine, now, liveRunning }: { session: SessionInfo; machine?: string; now: number; liveRunning: number | null }) {
  const started = Date.parse(session.started_at)
  const ended = session.ended_at ? Date.parse(session.ended_at) : NaN
  const runtime = RUNTIME_LABELS[session.runtime || 'daemon'] ?? session.runtime
  const engine = ENGINE_LABELS[session.engine || 'claude'] ?? session.engine
  const model = session.model ? modelDisplayName(session.model) : 'engine default'
  return (
    <dl className="rail-facts">
      <div>
        <dt>Status</dt>
        <dd><StatusPill state={displayState(session, liveRunning)} tasks={backgroundCount(session, liveRunning)} testId="rail-status" /></dd>
      </div>
      <div><dt>Runs on</dt><dd>{runtime}{machine ? ` · ${machine}` : ''}</dd></div>
      <div><dt>Engine</dt><dd>{engine}</dd></div>
      <div><dt>Model</dt><dd><span title={session.model}>{model}</span>{session.effort && <span className="rail-chip rail-effort" title="Configured effort">{session.effort}</span>}</dd></div>
      {Number.isFinite(started) && (
        <div>
          <dt>Started</dt>
          <dd><time dateTime={new Date(started).toISOString()}>{new Date(started).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}</time></dd>
        </div>
      )}
      {Number.isFinite(started) && (
        <div>
          <dt>{Number.isFinite(ended) ? 'Ran for' : 'Lifetime'}</dt>
          <dd data-testid="rail-lifetime">{lifetimeLabel((Number.isFinite(ended) ? ended : now) - started)}</dd>
        </div>
      )}
    </dl>
  )
}

function useMinute(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [active])
  return now
}

/** A pending review request: "Review: <file>", the ask under it; the row opens the file at the
 *  requested version, where the viewer's bar has Approve and Request changes. */
function ReviewRequestRow({ review, onOpen, onDismiss }: { review: LiveReviewRequest; onOpen: () => void; onDismiss: () => void }) {
  return (
    <li className="rail-item rail-child-item rail-review-item" data-testid="rail-review-request">
      <button type="button" className="rail-child-button" onClick={onOpen} title={`Open ${review.artifact_name} v${review.artifact_version}`}>
        <span className="rail-item-title"><span aria-hidden="true">✎</span> Review: {review.artifact_name}</span>
        <span className="rail-item-line">{review.artifact_version > 1 && <span className="rail-chip">v{review.artifact_version}</span>} <span className="rail-review-ask">{review.ask}</span></span>
      </button>
      <div className="rail-actions"><DismissButton what={`review request: ${review.artifact_name}`} onDismiss={onDismiss} /></div>
    </li>
  )
}

// Only the working line formats a live reasoning clock; status glyphs remain unchanged.
function reasoningChild(child: LiveChildSession, now: number): LiveChildSession {
  if (child.activity !== 'Thinking…' || !child.activity_since) return child
  const since = Date.parse(child.activity_since)
  if (!Number.isFinite(since)) return child
  return { ...child, activity: reasoningLabel({ since, tokens: null }, now), activity_since: undefined }
}
