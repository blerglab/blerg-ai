import { PendingProposal } from './ProposalAttention'
import type { ProposalInfo } from '../types'
import { reasoningLabel } from '@blerglab/chat'
import { modelDisplayName } from '@blerglab/chat'
// SessionRail: the session page's side panel, in four tabs. Live: what needs the person (the agent's
// open questions, with a box that answers each; tool changes waiting on a decision), what is running
// (subagents and background tasks, each with a way to its place in the conversation), the session's
// facts (with Resume when limit resets). History (HistoryTab.tsx): the pieces of work the session did. Files
// (FilesTab.tsx): the session's files. Capabilities (CapabilitiesTab.tsx): what it loaded. A tab other than Live
// reads the server only while it is on screen. The tab chosen is kept per browser. On a wide screen it is a panel beside the chat; on a narrow one a drawer over it (modal:
// Esc and the backdrop close it, focus stays inside, the page does not scroll behind it).
//
// It stays mounted while closed (hidden), so the background tasks' state — which failures were
// dismissed, since when it watches — lives as long as the page shows the session. Render it under
// a key of the session.
import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link } from 'react-router-dom'
import {
  AskReply,
  ChecklistItems,
  checklistAssignment,
  KindIcon,
  commandOf,
  isShell,
  SourceBadge,
  StatusChip,
  TicketKey,
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
  fileCount,
  type ArtifactInfo,
  type AskLive,
  type BackgroundTask,
  type PlanLive,
  type LiveState,
  type Ticket,
  type TicketSummaryLive,
} from '@blerglab/chat'
import { readProgress, type LiveProgress, type ProgressSubagent, type SessionPanelLiveState } from '../lib/liveState'
import { blergTransport } from '../lib/chatTransport'
import { ENGINE_LABELS, RUNTIME_LABELS } from '../lib/runtimes'
import { diffArtifacts, hasPlanTab, planList, planSummary, planTabLabel, type PlanList } from '../lib/railPlan'
import { RAIL_TABS, lifetimeLabel, readRailTab, waitLabel, writeRailTab, type RailTab } from '../lib/sessionRail'
import FilesTab from './FilesTab'
import { LiveMark, PinnedTicketModal } from './BoardCardView'
import { useTicketView } from '../lib/liveBoardCard'
import HistoryTab from './HistoryTab'
import CapabilitiesTab from './CapabilitiesTab'
import {PluginPicker,PluginRequests} from './SessionPlugins'
import DelegationLiveReview from './DelegationLiveReview'
import {useSessionPlugins} from '../hooks/useSessionPlugins'
import { useCapabilities } from '../hooks/useCapabilities'
import { enabledCapabilityCount, latestCapabilitiesEvent } from '../lib/capabilities'
import ShowLink from './RailShowLink'
import type { SessionInfo } from '../types'
import StatusPill from './StatusPill'
import UsageLimitResume from './UsageLimitResume'
import SessionPermissions from './SessionPermissions'
import { backgroundCount, displayState } from '../lib/displayState'
import { ChildSessionRow, StartedSessions } from './ChildSessions'
import { childIsWorking, type LiveChildSession, type LiveDelegationGrant, type LiveDelegationRequest, type LiveReviewRequest } from '../lib/liveState'
import './SessionRail.css'

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
  const reviews = session.archived_at ? [] : props.reviewRequests ?? []
  const openAsks = session.archived_at ? [] : live.openAsks
  // The clock runs only while something timed is on screen.
  const timed = open && (live.subagents.length > 0 || live.openAsks.length > 0 || tasks.length > 0 || runningChildren.length > 0)
  const now = useTicker(timed) - live.clockOffset
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
  const jumpChecklist = (id: string) => {
    if (!checklistNodes.current.has(nodeKey(id))) return
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
      setTab('plan')
      writeRailTab('plan')
      setChecklistJump({session: session.id, id: request.id})
    })
    return () => cancelAnimationFrame(frame)
  }, [props.openChecklistItem, open, session.id])
  const assignmentReference = (kind: 'subagent' | 'child_session', id: string) => {
    const item = checklistAssignment(live.checklist?.items ?? [], kind, id)
    return item?.id ? <button type="button" className="rail-link rail-checklist-reference" data-testid="rail-checklist-reference" onClick={() => jumpChecklist(item.id!)}>Plan · {item.text}</button> : null
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

  const pendingProposals = props.proposals ?? []
  const waitingProposals = (props.waitingProposals ?? pendingProposals).length
  const needsCount = openAsks.length + (toolChanges > 0 ? 1 : 0) + waitingProposals + requests.length + reviews.length
  const needsRows = needsCount > 0 || pendingProposals.length > 0
  // A started session still running is running work too: it counts here and has its row (the same
  // compact row Started sessions lists it with), beside the subagents and background tasks.
  const runningCount = live.subagents.length + runningTasks + (session.archived_at ? 0 : runningChildren.length)
  const sessionProgress = readProgress(live.progress)

  // A pinned ticket opened over the page (the current one in full, another by its summary).
  const [ticketOpen, setTicketOpen] = useState<{ ticket: Ticket; eventId: string } | null>(null)
  const openTicket = (t: TicketSummaryLive) => {
    const full = live.ticket && live.ticket.source === t.source && live.ticket.key === t.key && (live.ticket.board_id ?? '') === (t.boardId ?? '') ? live.ticket : null
    setTicketOpen({ ticket: full ?? { source: t.source, key: t.key, title: t.title, ...(t.boardId ? { board_id: t.boardId } : {}), ...(t.status ? { status: t.status } : {}), ...(t.url ? { url: t.url } : {}) }, eventId: t.eventId })
  }
  const planBody = (
    <>
      {tickets.length > 0 && (
        <Section title={tickets.length === 1 ? 'Ticket' : 'Tickets'} testId="rail-ticket">
          <ul className="rail-list">
            {tickets.map((t, i) => (
              <TicketRow key={`${t.source}:${t.boardId ?? ''}:${t.key}`} t={t} sessionId={session.id} current={i === 0} onOpen={() => openTicket(t)} />
            ))}
          </ul>
        </Section>
      )}

      {live.checklist
        ? (
            <Section title="Checklist" testId="rail-checklist" meta={<ProgressMeta done={doneCount(live.checklist.items)} total={live.checklist.items.length} />}>
              {/* "View diff" opens the diff the CLI published of an item's commits, in the chat's viewer. */}
              <ChecklistItems items={live.checklist.items} onViewDiff={openFile} diffSummary={diffSummary} live={!session.archived_at && !session.ended_at && ['starting', 'running', 'idle', 'waiting'].includes(session.status)} highlightedId={highlightedId} onItemElement={onChecklistElement} renderAssignees={item => {
                if (!item.assignees?.length) return null
                const label = 'Assigned: ' + item.assignees.map(assignee => {
                  const worker = assignee.kind === 'subagent' ? live.subagents.find(sub => (sub.childId || sub.key) === assignee.id)?.title : children.find(child => child.id === assignee.id)?.title
                  return worker || `${assignee.kind === 'subagent' ? 'Subagent' : 'Session'} ${assignee.id}`
                }).join('; ')
                return <span className="rail-checklist-assignees" title={label}>{label}</span>
              }} />
            </Section>
          )
        : live.plan && (
            <Section title="Plan" testId="rail-plan" meta={<ProgressMeta done={doneCount(live.plan.items)} total={live.plan.items.length} text={planLabel(live.plan)} />}>
              <PlanItems plan={live.plan} />
            </Section>
          )}

      <DiffsSection diffs={diffs} onOpen={openFile} />
    </>
  )

  const liveBody = (
    <>
      {list && <PlanSummary list={list} ticket={tickets[0]} sessionId={session.id} onOpenTicket={() => openTicket(tickets[0])} onOpen={() => goPlan()} />}

      <Section title="Needs you" count={openAsks.length + toolChanges + waitingProposals + requests.length + reviews.length} tone="attention" testId="rail-needs" empty={props.proposalsError ? undefined : "Nothing needs you."}>
        {(props.proposalsError || needsRows) && <>
        {props.proposalsError && <p role="alert">{props.proposalsError} <button type="button" onClick={props.onProposalsChanged}>Retry proposals</button></p>}
        {needsRows && (
          <ul className="rail-list">
            {pendingProposals.map(p => <li className="rail-item" key={p.id}><PendingProposal proposal={p} refresh={() => props.onProposalsChanged?.()} /></li>)}
            {reviews.map(r => <ReviewRequestRow key={r.request_id} review={r} onOpen={() => props.onOpenArtifact(r.artifact_id)} />)}
            {requests.map(r => <li key={r.request_id} className="rail-item"><div className="rail-item-title">Approve started session: {r.title || r.engine}</div><DelegationLiveReview sessionId={session.id} requestId={r.request_id} /></li>)}
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

      {!session.archived_at && <Section title="Running now" count={runningCount} tone="running" testId="rail-running" empty="Nothing is running." id={`${props.id}-running`}>
        {(sessionProgress || live.subagents.length + tasks.length + runningChildren.length > 0) && (
          <ul className="rail-list">
            {sessionProgress && <li className="rail-item" data-testid="rail-session-progress">
              <div className="rail-item-head"><span className="rail-item-title">Session</span></div>
              <WorkProgress progress={sessionProgress} />
            </li>}
            {live.subagents.map(s => (
              <SubagentRow
                key={s.key}
                sub={s}
                sessionId={session.id}
                reasoningTokens={live.reasoningTokens === true}
                sessionEffort={session.effort}
                assignment={assignmentReference('subagent', s.childId || s.key)}
                now={now}
                missing={missing === `sub:${s.key}`}
                onShow={() => jump(`sub:${s.key}`, () => scrollToCall(s.key))}
              />
            ))}
            {tasks.map(t => (
              <TaskRow
                key={t.taskId}
                t={t}
                now={now}
                missing={missing === `task:${t.taskId}`}
                onShow={() => jump(`task:${t.taskId}`, () => scrollToCall(t.callId))}
                onDismiss={() => bg.dismiss(t.taskId)}
              />
            ))}
            {runningChildren.map(child => <ChildSessionRow key={child.id} child={reasoningChild(child, now)} now={now} parentId={session.id} assignment={assignmentReference('child_session', child.id)} />)}
          </ul>
        )}
      </Section>}

      <StartedSessions readOnly={!!session.archived_at} sessionId={session.id} children={children} now={now} assignmentFor={id => assignmentReference('child_session', id)}
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
        <HistoryTab sessionId={session.id} active={open && tab === 'history'} filesKey={live.filesKey} missing={missing} onJump={jump} onOpenArtifact={openFile} />
      </div>
      {/* Files and Capabilities, kept mounted the same way: opened versions, a filter, opened groups. */}
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-files`} aria-labelledby={`${props.id}-tab-files`} hidden={tab !== 'files'}>
        <FilesTab sessionId={session.id} active={open && tab === 'files'} files={live.files} onOpenArtifact={openFile} onAttach={attachFiles} />
      </div>
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-capabilities`} aria-labelledby={`${props.id}-tab-capabilities`} hidden={tab !== 'capabilities'}>
        {canManagePlugins && <>
          <PluginPicker key={session.id} model={plugins} />
          <PluginRequests model={plugins} />
        </>}
        <CapabilitiesTab report={capabilities} pendingToolChanges={toolChanges} liveMcp={live.mcp ?? null} startedBySession={session.started_by?.kind === 'session'} />
      </div>
    </>
  )

  const ticketModal = ticketOpen && (
    <PinnedTicketModal
      ticket={ticketOpen.ticket}
      sessionId={session.id}
      onClose={() => setTicketOpen(null)}
      onShow={() => jump(ticketOpen.eventId, () => {
        const found = scrollToEvent(ticketOpen.eventId)
        if (found) setTicketOpen(null)
        return found
      })}
    />
  )

  if (mode === 'drawer') return open ? <Drawer id={props.id} onClose={onClose}>{body}{ticketModal}</Drawer> : null
  return (
    <aside id={props.id} className="session-rail panel" aria-label="Session panel" data-testid="session-rail" hidden={!open}>
      {/* No close button of its own: the header's panel toggle, right above, opens and closes it. */}
      <header className="rail-header">
        <h2 className="rail-heading">Session panel</h2>
      </header>
      <div className="rail-body">{body}</div>
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

/** Live's one line about the plan, two controls: the pinned ticket (a chip that opens its modal) and the
 *  list ("Checklist · 6 of 8", a short bar, the next item, ›) that opens the Plan tab. */
function PlanSummaryTicket({ ticket, sessionId, onOpen }: { ticket: TicketSummaryLive; sessionId: string; onOpen: () => void }) {
  const view = useTicketView(ticket, sessionId)
  return (
    <button type="button" className="rail-plan-summary-ticket" data-testid="rail-plan-summary-ticket" onClick={onOpen} title={`Open ${ticket.key}`}>
      <span className="ticket-key">{ticket.key}</span>
      <StatusChip status={view.status} />
      {view.live && <LiveMark />}
    </button>
  )
}

function PlanSummary({ list, ticket, sessionId, onOpenTicket, onOpen }: { list: PlanList; ticket?: TicketSummaryLive; sessionId: string; onOpenTicket: () => void; onOpen: () => void }) {
  const all = list.total > 0 && list.done >= list.total
  const head = all ? `${list.kind} · all ${list.total} done` : `${list.kind} · ${list.done} of ${list.total}`
  return (
    <div className="rail-plan-summary" data-testid="rail-plan-summary">
      {ticket && <PlanSummaryTicket ticket={ticket} sessionId={sessionId} onOpen={onOpenTicket} />}
      <button type="button" className="rail-plan-summary-list" data-testid="rail-plan-summary-open" onClick={onOpen} title={planSummary(list)}>
        <span className="rail-plan-summary-text" data-testid="rail-plan-summary-text">
          <span className="rail-plan-summary-head">{head}</span>
          <Progress done={list.done} total={list.total} />
          {!all && (list.doing?.length || list.next) && <span className="rail-plan-summary-next">{list.doing?.length ? ` · Doing: ${list.doing.join('; ')}` : ` · Next: ${list.next}`}</span>}
        </span>
        <span className="rail-plan-summary-go" aria-hidden="true">›</span>
      </button>
    </div>
  )
}

/** The diffs the session published, newest first: a row each, opening the viewer. */
function DiffsSection({ diffs, onOpen }: { diffs: ArtifactInfo[]; onOpen: (id: string) => void }) {
  if (diffs.length === 0) return null
  return (
    <Section title="Diffs" count={diffs.length} testId="rail-diffs">
      <ul className="rail-list">
        {diffs.map(d => {
          const at = d.created_at ? new Date(d.created_at) : null
          return (
            <li key={d.id} className="rail-item rail-diff" data-testid="rail-diff-row">
              <button type="button" className="rail-link rail-diff-open" onClick={() => onOpen(d.id)}>{d.name}</button>
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

/** One pinned ticket: the current one with its title on a line of its own, the others in one line.
 *  A Blerg Board card shows the card as it is now (its column and title), marked live. */
function TicketRow({ t, sessionId, current, onOpen }: { t: TicketSummaryLive; sessionId: string; current: boolean; onOpen: () => void }) {
  const view = useTicketView(t, sessionId)
  const title = `rail-ticket-title rail-link${statusTone(view.status) === 'canceled' ? ' canceled' : ''}`
  return (
    <li className={`rail-item rail-ticket${current ? ' current' : ''}`} data-testid="rail-ticket-row" data-ticket-key={t.key} data-live={view.live ? 'true' : undefined}>
      <div className="rail-item-head">
        <SourceBadge source={t.source} />
        <TicketKey ticket={t} />
        <StatusChip status={view.status} />
        {view.live && <LiveMark />}
        {!current && <button type="button" className={title} onClick={onOpen}>{view.title}</button>}
      </div>
      {current && <button type="button" className={title} onClick={onOpen} data-testid="rail-ticket-open">{view.title}</button>}
    </li>
  )
}

function AskRow({ ask, now, answer, draft, onDraft, missing, onShow }: {
  ask: AskLive
  now: number
  answer?: LiveState['answer']
  draft: string
  onDraft: (text: string) => void
  missing: boolean
  onShow: () => void
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
      <div className="rail-actions"><ShowLink onShow={onShow} missing={missing} /></div>
    </li>
  )
}

function WorkProgress({ progress }: { progress: LiveProgress }) {
  const valid = readProgress(progress)
  if (!valid) return null
  return <div className="rail-work-progress">
    <span className="rail-work-count">{valid.completed}/{valid.total}</span>
    <Progress done={valid.completed} total={valid.total} />
    {valid.label && <span className="rail-work-label" title={valid.label}>{valid.label}</span>}
  </div>
}

function SubagentRow({ sub, now, missing, onShow, assignment, sessionId, reasoningTokens, sessionEffort }: { sessionId: string; sub: ProgressSubagent; now: number; missing: boolean; onShow: () => void; assignment?: ReactNode; reasoningTokens: boolean; sessionEffort?: string }) {
  const model = modelDisplayName(sub.model)
  const observed = useTranscriptStore(s => s.sessions[sessionId]?.reasoning?.[sub.key])
  const shown = observed?.active === false ? null : observed ?? sub.reasoning
  // Every viewer of a shared session gets the live events; only the owner (or an administrator)
  // sees the count, the way the server's /live reply hides it. Anyone else sees the clock.
  const reasoning = shown && !reasoningTokens ? { ...shown, tokens: null } : shown
  const doing = reasoning ? reasoningLabel(reasoning, now) : sub.doing
  return (
    // A background subagent is drawn dashed, like its card in the transcript.
    <li className={`rail-item rail-sub${sub.background ? ' background' : ''}`} data-testid="rail-subagent" data-key={sub.key} data-status={sub.background ? 'background' : 'running'}>
      <div className="rail-item-head">
        <span className="rail-sub-icon" role="img" aria-label="Subagent">🤖</span>
        <span className="rail-item-title" title={sub.title}>{sub.title}</span>
        {sub.startedAt !== null && <span className="rail-clock" data-testid="rail-elapsed">{clockLabel(now - sub.startedAt)}</span>}
      </div>
      <div className="rail-chips">
        <span className="rail-chip status"><span className="rail-spin" aria-hidden="true" />{sub.background ? 'in the background' : 'running'}</span>
        {sub.resumed && <span className="rail-chip" data-testid="rail-sub-resumed">resumed</span>}
        {sub.type && <span className="rail-chip" data-testid="rail-sub-type">{sub.type}</span>}
        {model && <span className="rail-chip" data-testid="rail-sub-model" title={sub.model}>{model}</span>}
        {/* A subagent that reports no effort of its own runs at the session's: say so, never "unknown". */}
        <span className="rail-chip rail-effort" data-testid="rail-sub-effort" title={sub.effort ? 'The subagent\'s configured effort' : sessionEffort ? 'No effort of its own: it runs at the session\'s' : 'No effort reported'}>effort: {sub.effort || (sessionEffort ? `${sessionEffort} (session's)` : 'unknown')}</span>
        {sub.tokens !== null && sub.tokens > 0 && <span className="rail-chip" data-testid="rail-sub-tokens">{tokenLabel(sub.tokens)}</span>}
      </div>
      {assignment}
      {sub.progress && <WorkProgress progress={sub.progress} />}
      {doing && <div className="rail-item-line doing" data-testid="rail-sub-doing" title={doing}>{doing}</div>}
      <div className="rail-actions"><ShowLink onShow={onShow} missing={missing} /></div>
    </li>
  )
}

function TaskRow({ t, now, missing, onShow, onDismiss }: {
  t: BackgroundTask
  now: number
  missing: boolean
  onShow: () => void
  onDismiss: () => void
}) {
  const failed = taskFailed(t)
  const state = !t.done ? 'running' : failed ? 'failed' : 'done'
  if (isShell(t)) {
    // A command, not an agent: the command itself, what it is for, and shell · elapsed · status;
    // no model or effort.
    const note = t.command && t.description && t.description !== t.command ? t.description : ''
    return (
      <li className={`rail-item rail-task rail-shell ${state}`} data-testid="rail-task" data-task-id={t.taskId} title={titleOf(t)}>
        <div className="rail-item-head">
          <KindIcon t={t} />
          <code className="rail-cmd" data-testid="rail-command" tabIndex={0} role="region" aria-label="Shell command">{commandOf(t)}</code>
          <span className="rail-glyph"><StatusGlyph t={t} /></span>
        </div>
        {note && <div className="rail-item-line rail-shell-copy" data-testid="rail-task-note" tabIndex={0} role="region" aria-label="Command description">{note}</div>}
        {t.startedAt !== null && (
          <div className="rail-item-line">
            <time dateTime={new Date(t.startedAt).toISOString()}>
              started {new Date(t.startedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
            </time>
          </div>
        )}
        <div className="rail-chips">
          <span className="rail-chip" data-testid="rail-shell-kind">{t.kind === 'monitor' ? 'watcher' : 'shell'}</span>
          {elapsedOf(t, now) && <span className="rail-chip" data-testid="rail-elapsed">{elapsedOf(t, now)}</span>}
          <span className={`rail-chip${t.done ? '' : ' status'}`} data-testid="rail-shell-status">
            {!t.done && <span className="rail-spin" aria-hidden="true" />}
            {!t.done ? 'running' : failed ? 'failed' : 'done'}
          </span>
        </div>
        {t.done && t.done.summary && <div className={`rail-item-line rail-shell-copy${failed ? ' bad' : ''}`} data-testid="rail-task-summary" tabIndex={0} role="region" aria-label="Command result">{t.done.summary}</div>}
        <div className="rail-actions">
          <ShowLink onShow={onShow} missing={missing} />
          {failed && <button type="button" className="rail-link" data-testid="rail-dismiss" onClick={onDismiss}>Dismiss</button>}
        </div>
      </li>
    )
  }
  return (
    <li className={`rail-item rail-task ${state}`} data-testid="rail-task" data-task-id={t.taskId} title={titleOf(t)}>
      <div className="rail-item-head">
        <KindIcon t={t} />
        <span className="rail-item-title">{t.description || t.taskId}</span>
        <span className="rail-clock" data-testid="rail-elapsed">{elapsedOf(t, now)}</span>
        <span className="rail-glyph"><StatusGlyph t={t} /></span>
      </div>
      <div className="rail-item-line">
        {t.startedAt !== null && (
          <time dateTime={new Date(t.startedAt).toISOString()}>
            started {new Date(t.startedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
          </time>
        )}
        {!t.done && <span className="rail-task-state"> · running</span>}
      </div>
      {t.done && t.done.summary && <div className={`rail-item-line${failed ? ' bad' : ''}`} data-testid="rail-task-summary">{t.done.summary}</div>}
      <div className="rail-actions">
        <ShowLink onShow={onShow} missing={missing} />
        {failed && <button type="button" className="rail-link" data-testid="rail-dismiss" onClick={onDismiss}>Dismiss</button>}
      </div>
    </li>
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

/** The side panel's icon: a window with its right column marked. */
export function PanelIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.4">
      <rect x="1.5" y="2.5" width="13" height="11" rx="2" />
      <line x1="10" y1="2.5" x2="10" y2="13.5" />
      <rect x="10.6" y="3.2" width="3.2" height="9.6" rx="1" fill="currentColor" stroke="none" opacity="0.5" />
    </svg>
  )
}

/** A pending review request: "Review: <file>", the ask under it; the row opens the file at the
 *  requested version, where the viewer's bar has Approve and Request changes. */
function ReviewRequestRow({ review, onOpen }: { review: LiveReviewRequest; onOpen: () => void }) {
  return (
    <li className="rail-item rail-child-item rail-review-item" data-testid="rail-review-request">
      <button type="button" className="rail-child-button" onClick={onOpen} title={`Open ${review.artifact_name} v${review.artifact_version}`}>
        <span className="rail-item-title"><span aria-hidden="true">✎</span> Review: {review.artifact_name}</span>
        <span className="rail-item-line">{review.artifact_version > 1 && <span className="rail-chip">v{review.artifact_version}</span>} <span className="rail-review-ask">{review.ask}</span></span>
      </button>
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
