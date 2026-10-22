import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { modelDisplayName } from '../model/modelDisplay'
// ChatView: a whole chat with one session over a Transport — the toolbar, the transcript, the
// composer, the Files panel and viewer. The host adds what is its own through slots (a model
// picker, a rules panel) and themes it through the --chat-* tokens.
import { handleChatCopy } from '../model/richCopy'
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { CardRegistry } from '../cards/registry'
import { useAttachments } from '../hooks/useAttachments'
import { useSession, type PendingTurn, type SessionHandle } from '../hooks/useSession'
import { useBackgroundLiveStore } from '../hooks/useBackgroundTasks'
import { useWorkingLineStore } from '../hooks/useWorkingLine'
import { useChatReportStore, type ChatFiles } from '../hooks/useLiveState'
import { useTicker } from '../hooks/useTicker'
import { attachmentAt, findLatest, groupArtifacts, fileCount } from '../model/artifacts'
import { resolveArtifactLink } from '../model/artifactLinks'
import { MAX_ATTACHMENT_BYTES, type MessageAttachment } from '../model/attachments'
import { backgroundTasks, runningTasks } from '../model/backgroundTasks'
import { ClockOffsetContext } from '../model/clockOffset'
import { liveTurn, oldestLive } from '../model/liveCalls'
import { activityLabel, modelRetry } from '../model/workingActivity'
import { liveSubagents } from '../model/liveState'
import { checklistFromEvents, readChecklist, safeChecklistAssignments } from '../model/worklist'
import { sharedTimeline } from '../model/toolGroups'
import { subagentWaitingOn } from '../model/waitingOnSubagent'
import { askStates } from '../model/asks'
import { isTurnError } from '../model/notices'
import { mergeReviews, parseReviewFile, reviewFileName, type ReviewFile } from '../model/review'
import { describeSessionEnd } from '../model/sessionEnd'
import { latestStartAttempt, placeholderAttempt, startPanelVisible, withSessionOutcome } from '../model/startStages'
import { getDraft, loadCompact, localStorageChatStorage, saveCompact, setDraft as saveDraft, type ChatStorage } from '../model/storage'
import { themeStyle, type ThemeTokens } from '../theme'
import type { ArtifactInfo, SessionMeta, Transport } from '../transport/types'
import type { AgentEvent, ArtifactPayload, ChecklistItem, FiledCard, ReviewRequestPayload, StatusPayload, UserMessagePayload } from '../types'
import ArtifactViewer from './ArtifactViewer'
import { loadDiffSummary } from '../model/artifactPreview'
import WorkspaceFileViewer from './WorkspaceFileViewer'
import Composer from './Composer'
import { AskContext, type AskContextValue, type OwnerOnlyAnswer } from './askContext'
import { DelegationContext, type DelegationContextValue } from './delegationContext'
import { delegationStates } from '../model/delegations'
import { proposalStates } from '../model/proposals'
import { ProposalContext, type ProposalContextValue } from './proposalContext'
import { ReviewRequestContext, type ReviewRequestContextValue } from './reviewRequestContext'
import { newerReview, reviewForArtifact, reviewRequestStates } from '../model/reviewRequests'
import { ChatContext, type ChatContextValue } from './context'
import FilesPanel from './FilesPanel'
import Transcript, { WINDOW, type BackgroundLine } from './Transcript'
import WorkingIndicator from './WorkingIndicator'
import type { DescribeUserMessage, MessageFooter } from './cards/EventCards'
import { RefContext, type RefContextValue } from './RefLinks'
import { refStoreFor, type ChatRefs } from '../model/refs'
import './chat.css'

// How long a sent message may go unanswered — no status change, no event —
// before the optimistic "Working…" gives up on it (the message was dropped:
// no daemon, a socket that died between the check and the write).
const PENDING_ACK_MS = 20_000

const NO_EVENTS: AgentEvent[] = []
const NO_CHECKLIST_ITEMS: ChecklistItem[] = []

export interface ChatSlots {
  /** Leads the toolbar (the runner's model and effort pickers, its Rules button). */
  header?: ReactNode
  /** Beside Send. */
  composerActions?: ReactNode
  /** Under a user or assistant card. */
  messageFooter?: MessageFooter
  /** Heads a live transcript in place of the default "Session ready" card. */
  readyCard?: (meta: SessionMeta, readyAt: number) => ReactNode
  /** Between the transcript and the composer (the runner's held-first-message line). */
  aboveComposer?: ReactNode
}

export interface ChatViewProps {
  /** Current checklist from an authoritative host live read. Absent: use the loaded transcript. */
  checklistItems?: readonly ChecklistItem[]
  /** Opens a stable checklist target in the host's Plan panel, when available. */
  onOpenChecklistItem?: (itemId: string) => void
  session: string
  /** Opens another session (a child a delegation card names) in the host's app. Absent: the card
   *  shows the child's short id. */
  onManageChildApproval?: () => void
  onOpenSession?: (sessionId: string) => void
  /** Opens the host's editor for a board-card proposal's draft (approved from there). Absent: the
   *  proposal card has no Edit. */
  onEditProposal?: (proposalId: string) => void
  /** Opens a card a proposal created, in the host's board. Absent: the ticket card has no Open. */
  onOpenBoardCard?: (card: FiledCard) => void
  /** Card numbers and commit hashes in messages, linked once confirmed (model/refs.ts). Absent: the
   *  text stays text. */
  refs?: ChatRefs
  /** Opens the session's permissions (where an automatic approval's permission is revoked). */
  onManagePermissions?: () => void
  /** Bump when a proposal changed outside the chat (the host's editor approved it). */
  proposalRevision?: number
  /** @deprecated Not used since 0.19.0: commit chips never link out of Blerg (they open the item's
   *  diff at the commit, or copy the sha). Accepted so a host that passes it still compiles. */
  commitUrl?: (sha: string) => string | null | undefined
  transport: Transport
  cards?: CardRegistry
  /** Token values set on the chat's root, over whatever the stylesheets say. */
  theme?: ThemeTokens
  slots?: ChatSlots
  /** Whether the model's thinking is drawn (`'shown'`, the default, as a plain assistant card
   *  captioned "thinking") or left out of the transcript (`'hidden'`). `'open'` and `'collapsed'`
   *  are the old names for shown and hidden, kept for one version. */
  thinking?: 'shown' | 'hidden' | 'open' | 'collapsed'
  /** Drafts and the compact-tools choice; default localStorage behind try/catch. */
  storage?: ChatStorage
  /** Whether runs of tool calls are folded into one summary row, when the host holds that choice
   *  (the runner's session panel has the checkbox): the toolbar then has no "Compact tool calls"
   *  checkbox, and the host keeps the choice (loadCompact / saveCompact use the chat's key). Absent:
   *  the toolbar's checkbox, kept in `storage`. */
  compact?: boolean
  /** The toolbar's Files button and the dialog it opens (default true). False: the host shows the
   *  Files elsewhere, from what the chat reports (useLiveState's `files`: the list, its operations,
   *  the message box's file picker). */
  filesButton?: boolean
  /** Commands that change a setting without starting a turn (default /model <x> and /effort <x>). */
  noTurnCommands?: RegExp
  /** A file to open in the viewer as soon as the list is read (the runner's ?artifact= link). */
  linkedArtifact?: string | null
  /** The commit a linked diff opens at (its section scrolled to and marked). */
  linkedCommit?: string | null
  /** The linked file was opened or dismissed: the host may take it out of its address. */
  onLinkedArtifactConsumed?: () => void
  /** The session as the host knows it, when it has a live row (see useSession). */
  meta?: SessionMeta | null
  /** The largest file, in bytes, a person may attach: the runner's advertised limit (its
   *  initial_state or session status `limits.max_file_bytes`). A larger file is refused before it
   *  is sent. Absent: the session meta's `limits`, else MAX_ATTACHMENT_BYTES (an older runner's). */
  maxFileBytes?: number
  /** What the Files panel's empty state says the agent publishes with. */
  publishHint?: string
  /** How a user message is drawn: who it is from (a host whose automation also messages the
   *  session), or as a collapsed brief (the prompt the session was started with). */
  describeUserMessage?: DescribeUserMessage
  /** No composer: the transcript, files and viewer only (a session the host will not let the
   *  person message, whatever its status). */
  readOnly?: boolean
  /** The composer's placeholder for a session that can take a message, in place of the default. */
  placeholder?: string
  /** The person may open a file the agent's text names (a relative link, a path in inline code)
   *  from the session's folder, in a viewer: the host's say, true only for the session's owner
   *  (the route refuses anyone else). Needs a transport with the read (the runner's). Absent or
   *  false: nothing changes. */
  canOpenWorkspaceFiles?: boolean
  /** The viewer may see token counts: a live reasoning count ("Thinking… 1,234 tokens") shows only
   *  then; otherwise the working line keeps the reasoning clock. Default true (a host that shows
   *  tokens to everyone, or has no such rule). */
  reasoningTokens?: boolean
  /** Whether this viewer may answer an owner-only approval (an ask with owner_only): the host's
   *  say, true only for the session's signed-in owner (the server refuses anyone else). true:
   *  the card has a reply box; false: it says only the owner can answer; null: not known yet (the
   *  host is still loading it), no box and no hint; absent: this host never answers approvals,
   *  the card says to answer from the session in Runner. */
  canAnswerApprovals?: boolean | null
  /** Where the chat shows what the agent left running in the background: 'none' (the only
   *  choice) leaves the list to the host — a side panel reading useBackgroundTasks, say. The
   *  working line names running tasks either way. */
  backgroundTasks?: 'none'
  className?: string
}

const positive = (n: number | undefined) => (typeof n === 'number' && Number.isFinite(n) && n > 0 ? n : undefined)

// pendingSettled: something after the send says the turn is over (or never
// started): a turn end, an error, or the loop going back to idle.
function pendingSettled(p: PendingTurn, events: AgentEvent[]): boolean {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if ((ev.seq ?? 0) <= p.afterSeq) break
    if (ev.kind === 'turn_done' || isTurnError(ev)) return true
    if (ev.kind === 'status_changed' && (ev.payload as StatusPayload)?.status === 'idle') return true
  }
  return false
}

// lastRunningSince: when the in-flight turn started, per the transcript —
// the latest status_changed event, if it says running.
function lastRunningSince(events: AgentEvent[]): number | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (ev.kind !== 'status_changed') continue
    if ((ev.payload as StatusPayload)?.status !== 'running') return null
    const t = Date.parse(ev.ts)
    return Number.isFinite(t) ? t : null
  }
  return null
}

// ReadyCard heads the timeline once a session is live: what it is running,
// where, and since when — the answer to "did it start?".
export function ReadyCard({ meta, readyAt, children }: { meta: SessionMeta; readyAt: number; children?: ReactNode }) {
  const model = meta.model ? modelDisplayName(meta.model) : 'engine default'
  return (
    <div className="agent-card ready-card" data-testid="ready-card">
      <div className="ready-title">
        <span className="ready-dot" aria-hidden="true" />
        Session ready
        <time className="ready-time" dateTime={new Date(readyAt).toISOString()}>
          {new Date(readyAt).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}
        </time>
      </div>
      <dl className="ready-facts">
        <div><dt>Engine</dt><dd>{meta.engine || 'claude'}</dd></div>
        <div><dt>Model</dt><dd><span title={meta.model}>{model}</span>{meta.effort && <span className="model-effort" title="Configured effort">{meta.effort}</span>}</dd></div>
        {children}
        {meta.runtime && <div><dt>Runs on</dt><dd>{meta.runtime}</dd></div>}
      </dl>
    </div>
  )
}

function ownerOnlyAnswer(can: boolean | null | undefined): OwnerOnlyAnswer {
  if (can === true) return 'yes'
  if (can === false) return 'no'
  return can === null ? 'unknown' : 'elsewhere'
}

export default function ChatView(props: ChatViewProps) {
  const { transport, cards, slots = {}, storage = localStorageChatStorage } = props
  const sessionId = props.session
  const s: SessionHandle = useSession(transport, sessionId, { meta: props.meta, noTurnCommands: props.noTurnCommands })
  const { events, streaming, status, meta } = s
  // Opening a session whose long transcript is already held (a revisit): the first render draws
  // the skeleton, so the switch is painted at once, and the transcript follows on the next frame.
  // That second render is an ordinary update, not a transition, so store updates and the clock's
  // tick cannot restart it. A short one is drawn at once, as it always was. Only that first paint
  // waits: after it the transcript gets the events as they are.
  const [painted, setPainted] = useState(() => events.length <= WINDOW)
  useEffect(() => {
    if (painted) return
    const frame = requestAnimationFrame(() => setPainted(true))
    return () => cancelAnimationFrame(frame)
  }, [painted])
  const catchingUp = !painted
  const shownEvents = catchingUp ? NO_EVENTS : events
  const files = transport.files ?? null
  // References in messages: one store per session (lookups batched and kept); its readers follow
  // the host's (the board, once known) and the transport's commit read, for a viewer who may use it.
  const refStore = refStoreFor(sessionId)
  const refsProp = props.refs
  const commitRead = refsProp?.commits && files?.commit ? files : null
  useEffect(() => {
    refStore.setResolvers({
      cards: refsProp?.cards,
      commit: commitRead ? (sha: string) => commitRead.commit!(sessionId, sha).then(d => d?.sha ?? null) : null,
    })
  }, [refStore, refsProp?.cards, commitRead, sessionId])
  const refContext = useMemo<RefContextValue | null>(() => (refsProp ? { store: refStore, sessionId, refs: refsProp } : null), [refStore, sessionId, refsProp])

  const [draft, setDraftState] = useState(() => getDraft(storage, sessionId))
  // Another session: its own draft.
  const [draftFor, setDraftFor] = useState(sessionId)
  if (draftFor !== sessionId) {
    setDraftFor(sessionId)
    setDraftState(getDraft(storage, sessionId))
  }
  function setDraft(text: string) {
    setDraftState(text)
    saveDraft(storage, sessionId, text)
  }
  const maxFileBytes = positive(props.maxFileBytes) ?? positive(meta?.limits?.max_file_bytes) ?? MAX_ATTACHMENT_BYTES
  const attachments = useAttachments(sessionId, files, maxFileBytes)
  const attachInput = useRef<HTMLInputElement | null>(null)
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  const [showFiles, setShowFiles] = useState(false)
  // The file open in the viewer (from a card or the panel), and the one a link names, which
  // opens as soon as the list is read.
  const [viewing, setViewing] = useState<ArtifactInfo | null>(null)
  // The commit a diff opens at, for a file opened here (a linked one has the host's linkedCommit).
  const [diffAnchor, setDiffAnchor] = useState<string | null>(null)
  const linkedProp = props.linkedArtifact ?? null
  const [linked, setLinked] = useState<string | null>(linkedProp)
  const [seenLink, setSeenLink] = useState(linkedProp)
  if (seenLink !== linkedProp) {
    setSeenLink(linkedProp)
    setLinked(linkedProp)
  }
  const [workspaceFile, setWorkspaceFile] = useState<{ path: string; line?: number } | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  // The host's choice when it holds one (props.compact), else the chat's own, from storage.
  const [ownCompact, setOwnCompact] = useState(() => loadCompact(storage))
  const hostCompact = props.compact
  const compact = hostCompact ?? ownCompact
  // Goes up by one on each send: the transcript pins itself to the bottom for the new bubble.
  const [pin, setPin] = useState(0)

  // Uploads leave no transcript event, so read the list again when one finishes or is removed,
  // and whenever the panel is opened or closed.
  const artifacts = s.artifacts
  const refreshArtifacts = artifacts.refresh
  const doneCount = attachments.done.length
  const firstRefresh = useRef(true)
  useEffect(() => {
    if (firstRefresh.current) { firstRefresh.current = false; return }
    refreshArtifacts()
  }, [doneCount, showFiles, refreshArtifacts])

  const linkedArtifact = linked ? artifacts.items.find(a => a.id === linked) ?? null : null
  const linkMissing = linked !== null && artifacts.status === 'ready' && !linkedArtifact
  // Whether the viewer was opened from a preview click: a video or audio file then plays at once.
  const [autoplay, setAutoplay] = useState(false)
  // The file a review line opened, in review rather than view.
  const [reviewing, setReviewing] = useState<string | null>(null)
  const picked = viewing ?? linkedArtifact
  // The viewer shows the list's own entry for the file when there is one: a card carries only its
  // own version, the list also knows how many versions the file has now.
  const openArtifact = picked ? artifacts.items.find(a => a.id === picked.id) ?? picked : null
  const openLatest = openArtifact ? findLatest(artifacts.items, openArtifact) : undefined
  const latestVersions = useMemo(
    () => new Map(groupArtifacts(artifacts.items).map(g => [`${g.origin}\0${g.name}`, g.latest.version ?? 1])),
    [artifacts.items],
  )
  const send = s.send
  const sessionOver = status === 'error' || status === 'stopped' || status === 'ended'
  const publishedSessionUrl = props.transport.publishedSessionUrl?.(sessionId)
  const canOpenWorkspace = props.canOpenWorkspaceFiles === true && !!files?.workspaceFile
  // Keep the shared context stable while unrelated text/tools stream: a checklist changes only
  // when its newest valid full snapshot changes, not on every event in a long transcript.
  const checklistEvent = useMemo(() => {
    for (let i = events.length - 1; i >= 0; i--) {
      if (events[i].kind === 'checklist' && readChecklist(events[i].payload)) return events[i]
    }
    return undefined
  }, [events])
  const checklist = useMemo(() => checklistEvent ? checklistFromEvents([checklistEvent]) : null, [checklistEvent])
  const checklistItems = props.checklistItems ?? checklist?.items ?? NO_CHECKLIST_ITEMS
  const currentChecklistItems = useMemo(() => safeChecklistAssignments(checklistItems, events), [checklistItems, events])
  const onOpenChecklistItem = props.onOpenChecklistItem
  const sessionEffort = meta?.effort || undefined
  const chatContext = useMemo<ChatContextValue>(() => ({
    sessionId,
    sessionOver,
    sessionEffort,
    checklistItems: currentChecklistItems,
    onOpenChecklistItem,
    // A checklist item's "View diff" (or one of its sha chips) opens the diff the CLI published of
    // its commits, from the list, at the commit's section when a chip was clicked.
    viewDiff: (id: string) => {
      const a = files ? artifacts.items.find(x => x.id === id) : undefined
      return a ? (commit?: string) => { setAutoplay(false); setDiffAnchor(commit ?? null); setViewing(a) } : null
    },
    diffSummary: files ? (id: string) => loadDiffSummary(sessionId, id, x => files.raw(sessionId, x)) : undefined,
    files,
    view: (a: ArtifactInfo, opts?: { autoplay?: boolean }) => { setAutoplay(!!opts?.autoplay); setDiffAnchor(null); setViewing(a) },
    // A bare name (no folder) that is a file published in this session opens that file: an agent that
    // says "open `plan.md`" means the one it published, not one at the top of its folder.
    openWorkspaceFile: canOpenWorkspace ? (path: string, line?: number, opts?: { folder?: boolean }) => {
      const published = files && !path.includes('/') && !opts?.folder
        ? findLatest(artifacts.items, { name: path, origin: 'agent' }) ?? findLatest(artifacts.items, { name: path, origin: 'user' })
        : undefined
      if (published) { setAutoplay(false); setDiffAnchor(null); setViewing(published); return }
      setWorkspaceFile({ path, line })
    } : undefined,
    artifactLink: href => files ? resolveArtifactLink(href, publishedSessionUrl, artifacts.items) : null,
    latestVersion: (a: ArtifactPayload) => latestVersions.get(`${a.origin === 'user' ? 'user' : 'agent'}\0${a.name}`),
    attachmentById: (id: string) => artifacts.items.find(a => a.id === id && a.origin === 'user'),
    attachmentAt: (name: string, at: string | undefined) => attachmentAt(artifacts.items, name, at),
    byName: (name: string) => findLatest(artifacts.items, { name, origin: 'user' }),
    // The review and mark-up modes send the way the composer does: every file uploaded first, then
    // the message with the attachment note, then the list read again for the new versions.
    submitFeedback: async (text: string, toUpload: File[]) => {
      if (!files) return false
      const uploaded: MessageAttachment[] = []
      for (const f of toUpload) {
        const up = await files.upload(sessionId, f)
        // The ID goes in the note, so the message previews this exact upload (a mark-up's picture).
        uploaded.push({ id: up.id, name: up.name, size: up.size })
      }
      const r = await send(text, uploaded)
      refreshArtifacts()
      if (r.queued) setPin(n => n + 1)
      return r.queued
    },
    reviewFor: async (name: string): Promise<ReviewFile | null> => {
      if (!files) return null
      let items = artifacts.items
      try {
        items = await files.list(sessionId)
      } catch {
        // The list we already have is the next best thing.
      }
      const reviewName = reviewFileName(name)
      const read = async (origin: 'user' | 'agent') => {
        const a = findLatest(items, { name: reviewName, origin })
        if (!a) return null
        try {
          return parseReviewFile(await (await files.raw(sessionId, a.id)).text())
        } catch {
          return null
        }
      }
      const [person, agent] = await Promise.all([read('user'), read('agent')])
      return mergeReviews(person, agent)
    },
    openReview: files ? (name: string, artifactId?: string) => {
      const a = (artifactId ? artifacts.items.find(x => x.id === artifactId) : undefined) ??
        findLatest(artifacts.items, { name, origin: 'agent' }) ?? findLatest(artifacts.items, { name, origin: 'user' })
      if (!a) return
      setAutoplay(false); setDiffAnchor(null); setReviewing(a.id); setViewing(a)
    } : undefined,
  }), [sessionId, sessionOver, sessionEffort, currentChecklistItems, onOpenChecklistItem, canOpenWorkspace, files, latestVersions, artifacts.items, send, refreshArtifacts, publishedSessionUrl])
  const consumeLink = props.onLinkedArtifactConsumed
  function closeViewer() {
    setAutoplay(false)
    setReviewing(null)
    setViewing(null)
    if (linked !== null) consumeLink?.()
    setLinked(null)
  }

  // ─── Start progress ───────────────────────────────────────────────────────
  const startSession = useMemo(() => ({ status: status ?? 'starting', error_reason: meta?.error_reason, message: meta?.message }), [status, meta?.error_reason, meta?.message])
  const rawAttempt = useMemo(() => latestStartAttempt(events), [events])
  const attempt = useMemo(() => withSessionOutcome(startSession, rawAttempt), [startSession, rawAttempt])
  const starting = startPanelVisible(startSession, rawAttempt)
  const hasUserMessage = events.some(ev => ev.kind === 'user_message' && (ev.payload as UserMessagePayload)?.source !== 'system')
  const live = status === 'running' || status === 'idle' || status === 'waiting'
  // A message was sent to a session with no pod and a new one is being started: the progress
  // panel says so, and the "no pod is running, send a message" banner would contradict it.
  const resumeUnderWay = status === 'disconnected' && starting && !!attempt && !attempt.failed

  // ─── Working / waiting ────────────────────────────────────────────────────
  const terminal = status === 'error' || status === 'stopped'
  // A stopped or ended session has no process and cannot be resumed: say so and lock the composer.
  const ended = status === 'stopped' || status === 'ended'
  const inputLocked = status === null || status === 'starting' || ended
  const endLine = ended && meta ? describeSessionEnd(meta) : null
  const pending = s.pending
  const pendingActive = pending !== null && !terminal && !pendingSettled(pending, events)
  // Neither while the history is still coming in: the session is at its final state by then, and what
  // the replay passes through (running, waiting, idle again) would flash in the line.
  const replayed = s.replayDone && !s.hasMore
  const waiting = replayed && status === 'waiting' && !pendingActive
  const working = replayed && !waiting && (pendingActive || status === 'running')
  // Times below are server time: event ts values are the server's clock, and
  // clockOffset (from the replay's server_time) maps the browser's onto it.
  const clockOffset = s.clockOffset
  const turnSince = pendingActive ? pending!.since : lastRunningSince(events)
  const now = useTicker(working) - clockOffset
  // Tool calls in flight: only while the session runs or waits, and only in
  // the turn now running, so a replayed orphan never shows a ticking clock.
  const turn = useMemo(
    () => liveTurn(events, status === 'running' || status === 'waiting'),
    [events, status],
  )
  const inFlight = turn.calls
  const liveIds = useMemo(() => new Set(inFlight.map(c => c.callId)), [inFlight])
  // No call in flight but a foreground subagent running (a resumed one, whose SendMessage call has
  // returned): the line names the subagent. A call in flight wins.
  const nowMinute = Math.floor(now / 60_000)
  const subWaitingOn = useMemo(
    () => (working && inFlight.length === 0
      ? subagentWaitingOn(liveSubagents(events, { inFlight: true, live: true }, nowMinute * 60_000, evs => sharedTimeline(sessionId, evs, { groupTools: compact })))
      : null),
    [working, inFlight.length, events, nowMinute, sessionId, compact],
  )
  // The working line names a call only when the start of its turn is held: without it, a call
  // whose result is on a page not loaded, or an orphan of a turn long over, would be named. Either
  // suffix is then held like the rest of the line while a history loads (below).
  const waitingOnNow = working ? (turn.bounded ? oldestLive(inFlight) : null) ?? subWaitingOn : null
  const since = turnSince ?? now
  // What the agent is on when the line names no call: thinking (live only), or the engine retrying
  // the model's API (its notice is the newest word of the turn, and nothing live has come since).
  const retryNotice = useMemo(() => modelRetry(events, status === 'running'), [events, status])
  const retry = s.answering ? null : retryNotice
  const observedReasoning = useTranscriptStore(st => st.sessions[sessionId]?.reasoning?.[''])
  const showReasoningTokens = props.reasoningTokens !== false
  const reasoning = useMemo(() => observedReasoning && !showReasoningTokens ? { ...observedReasoning, tokens: null } : observedReasoning,
    [observedReasoning, showReasoningTokens])
  const activity = working ? activityLabel(s.thinking, retry) : null

  // ─── Background tasks ─────────────────────────────────────────────────────
  // What the agent left running in the background, for the working line (the host lists them). A
  // session that is not live runs nothing (whatever it started ended with it).
  const bgTasks = useMemo(() => backgroundTasks(events), [events])
  const bgRunning = useMemo(() => runningTasks(bgTasks, live, now), [bgTasks, live, now])
  const bgCount = bgRunning.length
  const bgDescription = bgCount === 1 ? bgRunning[0].description : ''
  const bgCommand = bgCount === 1 ? bgRunning[0].command : ''
  const bgSince = bgRunning.reduce<number | null>((min, t) => (t.startedAt !== null && (min === null || t.startedAt < min) ? t.startedAt : min), null)
  const backgroundNow = useMemo<BackgroundLine | null>(
    () => (bgCount > 0 ? { count: bgCount, description: bgDescription, command: bgCommand, since: bgSince } : null),
    [bgCount, bgDescription, bgCommand, bgSince],
  )
  // The working line's suffix and background words are read from the events held, so while a
  // history is still coming in (the replay's pages, an older page) a call whose result, or a task
  // whose end, is not in yet would read as running, and the line would change with every page. Until
  // the events are settled the line keeps what it last said of a settled transcript (on a first
  // load: nothing). The call it names belongs to its turn: a new turn starting drops it. The tasks
  // it names end with the session: once it is not live they are not brought back.
  const settledEvents = s.replayDone && !s.hasMore && !s.loadingOlder
  const [lastSettled, setLastSettled] = useState<{
    sessionId: string; turnStart: string | null; waitingOn: typeof waitingOnNow; background: BackgroundLine | null
  } | null>(null)
  if (settledEvents && (lastSettled?.sessionId !== sessionId || lastSettled.turnStart !== turn.turnStart ||
      lastSettled.waitingOn !== waitingOnNow || lastSettled.background !== backgroundNow)) {
    setLastSettled({ sessionId, turnStart: turn.turnStart, waitingOn: waitingOnNow, background: backgroundNow })
  }
  const held = lastSettled?.sessionId === sessionId ? lastSettled : null
  if (!settledEvents && !live && held?.background) setLastSettled({ ...held, background: null })
  const waitingOn = settledEvents ? waitingOnNow : held && held.turnStart === turn.turnStart ? held.waitingOn : null
  const background = settledEvents ? backgroundNow : live ? held?.background ?? null : null
  // A view elsewhere on the page (useBackgroundTasks) reads whether the session is live from here.
  const setBackgroundLive = useBackgroundLiveStore(st => st.setLive)
  useEffect(() => { setBackgroundLive(sessionId, live) }, [setBackgroundLive, sessionId, live])
  // The working line's inputs, for a view elsewhere on the page (the host's Running now "Main" row):
  // the same call, words and clock, so the two never disagree. Cleared when this view goes.
  const setWorkingLine = useWorkingLineStore(st => st.setLine)
  const lineSince = working ? turnSince : null
  useEffect(() => {
    setWorkingLine(sessionId, working ? { since: lineSince, waitingOn, activity } : null)
  }, [setWorkingLine, sessionId, working, lineSince, waitingOn, activity])
  useEffect(() => () => setWorkingLine(sessionId, null), [setWorkingLine, sessionId])

  // Give up on a sent message nothing has answered.
  const answered = pending !== null && (
    events.some(ev => (ev.seq ?? 0) > pending.afterSeq) ||
    status !== 'idle'
  )
  const clearPending = s.clearPending
  const giveUpQueued = s.giveUpQueued
  useEffect(() => {
    if (!pending || answered) return
    const t = setTimeout(() => {
      clearPending()
      giveUpQueued()
      setNotice('No response from the session — it may not have received your message. Try sending it again.')
    }, PENDING_ACK_MS)
    return () => clearTimeout(t)
  }, [pending, answered, clearPending, giveUpQueued])

  // Sent messages the transcript has not recorded yet: each is replaced by its
  // real event, in order. If the session ended or failed first, the message is
  // kept on screen, marked as not delivered, so it never just vanishes.
  const queueState: 'failed' | 'queued' | 'sending' = terminal
    ? 'failed'
    : status === 'running'
      ? 'queued'
      : 'sending'

  // Prefer the session's declared capability. Older backends retain the
  // established engine behavior until they report this field.
  const steers = meta?.steering_supported ?? (!meta?.engine || meta.engine === 'claude' || meta.engine === 'codex')
  const loadingHistory = !s.replayDone || s.hasMore
  // The server holds older events than the transcript does (fetched as the reader scrolls up).
  const hasOlder = !loadingHistory && s.hasOlder
  // An older page is on its way in right now: the "Loading earlier messages" line, and no scrollbar
  // thumb to jump when it lands. Not merely hasOlder, which can stay true for as long as the reader
  // never scrolls up.
  const loadingOlder = !loadingHistory && s.loadingOlder
  // Until the transcript is complete, show placeholder bubbles instead of an empty or filling-in
  // area (a session that is still starting has its own progress panel).
  const showSkeleton = (loadingHistory || catchingUp) && !starting

  async function submit() {
    const typed = draft.trim()
    const done = attachments.done
    if ((!typed && done.length === 0) || attachments.uploading || inputLocked) return
    if (!transport.connected()) {
      // Not connected: nothing was sent. Keep the text; don't pretend.
      setNotice('Not connected — your message was not sent. It is still in the box; send it again once reconnected.')
      return
    }
    const r = await s.send(typed, done)
    if (!r.queued) {
      setNotice('Not connected — your message was not sent. It is still in the box; send it again once reconnected.')
      return
    }
    setNotice(null)
    setDraft('')
    attachments.clear()
    setPin(n => n + 1)
  }

  const hasFiles = (e: React.DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes('Files')
  const dropProps = inputLocked || !files || props.readOnly ? {} : {
    onDragEnter: (e: React.DragEvent) => {
      if (!hasFiles(e)) return
      dragDepth.current++
      setDragging(true)
    },
    onDragOver: (e: React.DragEvent) => { if (hasFiles(e)) e.preventDefault() },
    onDragLeave: (e: React.DragEvent) => {
      if (!hasFiles(e)) return
      dragDepth.current = Math.max(0, dragDepth.current - 1)
      if (dragDepth.current === 0) setDragging(false)
    },
    onDrop: (e: React.DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      dragDepth.current = 0
      setDragging(false)
      attachments.addFiles(e.dataTransfer.files)
    },
  }

  // ─── The agent's questions ────────────────────────────────────────────────
  // The newest open ask: a plain message answers it too (the server routes it there), so the
  // composer says so. Its card has a reply box when the transport can address an answer.
  const closedAsks = s.closedAsks
  const asks = useMemo(() => askStates(shownEvents, closedAsks, terminal || ended), [shownEvents, closedAsks, terminal, ended])
  const canAnswer = !!transport.answersAsks && !props.readOnly && !inputLocked
  const answerAsk = useMemo(() => canAnswer
    ? async (text: string, messageId?: string) => {
        if (!transport.connected()) return false
        const r = await send(text, [], messageId ? { answerTo: messageId } : {})
        if (r.queued) setPin(n => n + 1)
        return r.queued
      }
    : undefined, [canAnswer, transport, send])
  const askContext = useMemo<AskContextValue>(() => ({ states: asks.byEvent, answer: answerAsk, ownerOnly: ownerOnlyAnswer(props.canAnswerApprovals) }), [asks.byEvent, answerAsk, props.canAnswerApprovals])
  const delegationFns = useMemo(() => ({
    decide: transport.decideDelegation && !props.readOnly ? transport.decideDelegation.bind(transport) : undefined,
    fullPrompt: transport.delegationRequest ? async (id: string) => (await transport.delegationRequest!(id)).prompt : undefined,
    decideUrl: transport.publishedSessionUrl?.(sessionId) ?? null,
  }), [transport, props.readOnly, sessionId])
  const delegationContext = useMemo<DelegationContextValue>(() => ({
    states: delegationStates(shownEvents),
    ...delegationFns,
    onOpenSession: props.onOpenSession,
    onManageChildApproval: props.onManageChildApproval,
  }), [shownEvents, delegationFns, props.onOpenSession, props.onManageChildApproval])
  const proposalFns = useMemo(() => ({
    load: transport.proposal ? transport.proposal.bind(transport) : undefined,
    decide: transport.decideProposal && !props.readOnly ? transport.decideProposal.bind(transport) : undefined,
  }), [transport, props.readOnly])
  const proposalContext = useMemo<ProposalContextValue>(() => ({
    states: proposalStates(shownEvents),
    ...proposalFns,
    onEdit: props.readOnly ? undefined : props.onEditProposal,
    onOpenCard: props.onOpenBoardCard,
    onManagePermissions: props.onManagePermissions,
    revision: props.proposalRevision,
  }), [shownEvents, proposalFns, props.readOnly, props.onEditProposal, props.onOpenBoardCard, props.onManagePermissions, props.proposalRevision])
  // Review requests: the transcript's, merged with the server's list (read again whenever a review
  // event arrives), which also says whether this person may decide them.
  const reviewEvents = useMemo(() => shownEvents.reduce((n, e) => n + (e.kind === 'review_requested' || e.kind === 'review_decided' ? 1 : 0), 0), [shownEvents])
  const [reviewList, setReviewList] = useState<{ sessionId: string; requests: ReviewRequestPayload[]; canDecide: boolean } | null>(null)
  useEffect(() => {
    if (!transport.reviewRequests) return
    let cancelled = false
    transport.reviewRequests(sessionId).then(r => { if (!cancelled) setReviewList({ sessionId, ...r }) }, () => {})
    return () => { cancelled = true }
  }, [transport, sessionId, reviewEvents])
  const reviewContext = useMemo<ReviewRequestContextValue>(() => {
    const merged = new Map(reviewRequestStates(shownEvents))
    const list = reviewList?.sessionId === sessionId ? reviewList : null
    for (const r of list?.requests ?? []) merged.set(r.request_id, newerReview(merged.get(r.request_id), r))
    return {
      forArtifact: (id: string) => reviewForArtifact(merged.values(), id),
      decide: list?.canDecide && transport.decideReview && !props.readOnly ? transport.decideReview.bind(transport) : undefined,
      open: files ? (r: ReviewRequestPayload) => {
        const a = artifacts.items.find(x => x.id === r.artifact_id)
        if (a) { setAutoplay(false); setDiffAnchor(null); setViewing(a) }
      } : undefined,
    }
  }, [shownEvents, reviewList, sessionId, transport, props.readOnly, files, artifacts.items])
  const askOpen = asks.open !== null && !!transport.answersAsks && (!(asks.open.payload as { owner_only?: boolean })?.owner_only || props.canAnswerApprovals === true)

  // What a view beside the chat (useLiveState: the host's side panel) reads about this session:
  // whether a call can be in flight, whether it can run or ask anything, the asks the server
  // closed, how to answer one or open the Files panel, and the Files list as last read.
  const setReport = useChatReportStore(st => st.setReport)
  const clearReport = useChatReportStore(st => st.clearReport)
  const inFlightNow = status === 'running' || status === 'waiting'
  const over = terminal || ended
  // With the Files button off there is no dialog to open: a caller must not be able to set showFiles.
  const filesButton = props.filesButton !== false
  const openFiles = useMemo(() => (files && filesButton ? () => setShowFiles(true) : undefined), [files, filesButton])
  const filesKey = useMemo(() => artifacts.items.map(a => a.id).join(','), [artifacts.items])
  const attachFiles = useCallback(() => attachInput.current?.click(), [])
  const readOnly = !!props.readOnly
  const filesReport = useMemo<ChatFiles | undefined>(() => (files
    ? { ops: files, items: artifacts.items, status: artifacts.status, refresh: refreshArtifacts, attach: readOnly ? undefined : attachFiles }
    : undefined), [files, artifacts.items, artifacts.status, refreshArtifacts, readOnly, attachFiles])
  useEffect(() => {
    setReport(sessionId, { inFlight: inFlightNow, live, ended: over, closedAsks, answer: answerAsk, openFiles, compact, filesKey, files: filesReport })
  }, [setReport, sessionId, inFlightNow, live, over, closedAsks, answerAsk, openFiles, compact, filesKey, filesReport])
  // A chat that goes (another session, the page left) takes its report with it.
  useEffect(() => () => clearReport(sessionId), [clearReport, sessionId])

  const interrupt = s.interrupt ? () => { void s.interrupt!().catch(() => {}) } : undefined
  const busy = status === 'running' || status === 'waiting' || pendingActive

  const placeholder = ended
    ? 'This session has ended — start a new session to keep working'
    : status === 'starting' || status === null
    ? 'The session is starting — you can message it once it is ready'
    : askOpen
      ? 'Answer the agent’s question…'
    : props.placeholder
      ? props.placeholder
      : live && !hasUserMessage
        ? 'Message the agent to get started… (/model, /effort, /<skill>)'
        : 'Message the agent… (/model, /effort, /<skill>)'

  // Everything handed to the transcript is kept stable between renders that do not change it (a
  // keystroke, a tick of the clock), so the memoised timeline is not drawn again for nothing.
  const startedAt = (meta ? Date.parse(meta.started_at) : NaN) || now
  const readyAt = rawAttempt?.readyAt ?? startedAt
  const readyCardSlot = slots.readyCard
  const showReady = !!meta && (live || hasUserMessage)
  const readyCard = useMemo(() => meta && showReady
    ? (readyCardSlot ? readyCardSlot(meta, readyAt) : <ReadyCard meta={meta} readyAt={readyAt} />)
    : null, [meta, showReady, readyCardSlot, readyAt])
  const startAttempt = useMemo(
    () => (starting ? (attempt ?? placeholderAttempt(startedAt)) : null),
    [starting, attempt, startedAt],
  )
  const transcriptSession = useMemo(() => (status ? { id: sessionId, status, runtime: meta?.runtime } : undefined), [sessionId, status, meta?.runtime])

  return (
    <ClockOffsetContext.Provider value={clockOffset}>
    <ChatContext.Provider value={chatContext}>
    <RefContext.Provider value={refContext}>
    <div className={`agent-chat${props.className ? ` ${props.className}` : ''}`} data-testid="agent-chat" style={props.theme ? themeStyle(props.theme) : undefined} onCopy={e => handleChatCopy(e.nativeEvent, e.currentTarget)} {...dropProps}>
      {dragging && <div className="drop-zone" role="status" data-testid="drop-zone">Drop files to attach</div>}
      <div className="agent-toolbar">
        {slots.header}
        {files && props.filesButton !== false && (
          <button
            type="button"
            className="files-toggle"
            data-testid="files-toggle"
            aria-haspopup="dialog"
            aria-label={artifacts.status === 'ready' ? `Files (${fileCount(artifacts.items)})` : 'Files'}
            title="Files"
            onClick={() => setShowFiles(true)}
          >
            <span className="tb-icon" aria-hidden="true">📎</span>
            <span className="tb-label">Files</span>
            {artifacts.status === 'ready' && <span className="tb-count" aria-hidden="true">{fileCount(artifacts.items)}</span>}
          </button>
        )}
        {hostCompact === undefined && (
          <label className="compact-toggle" title="Fold runs of tool calls into one summary row">
            <input
              type="checkbox"
              checked={compact}
              aria-label="Compact tool calls"
              onChange={e => { setOwnCompact(e.target.checked); saveCompact(storage, e.target.checked) }}
            />
            <span className="tb-icon" aria-hidden="true">▤</span>
            <span className="tb-label">Compact tool calls</span>
          </label>
        )}
        {working && streaming && <WorkingIndicator since={since} now={now} waitingOn={waitingOn} activity={activity} reasoning={s.thinking ? reasoning : null} compact />}
        {busy && interrupt && (
          <button className="agent-stop" onClick={interrupt} aria-label="Stop" title="Stop (Esc)">
            <span className="tb-icon" aria-hidden="true">⏹</span>
            <span className="tb-label">Stop</span>
          </button>
        )}
      </div>

      {showFiles && files && props.filesButton !== false && (
        <FilesPanel
          sessionId={sessionId}
          files={files}
          items={artifacts.items}
          status={artifacts.status}
          onClose={() => setShowFiles(false)}
          onView={a => { setDiffAnchor(null); setViewing(a) }}
          onChanged={artifacts.refresh}
          onAttach={() => { setShowFiles(false); attachInput.current?.click() }}
          publishHint={props.publishHint}
        />
      )}
      {openArtifact && files && (
        <ReviewRequestContext.Provider value={reviewContext}>
        <ArtifactViewer
          key={openArtifact.id}
          sessionId={sessionId}
          files={files}
          artifact={openArtifact}
          latest={openLatest}
          autoplay={autoplay}
          commit={viewing ? diffAnchor : props.linkedCommit ?? null}
          review={reviewing === openArtifact.id}
          onOpen={a => { if (linked !== null) consumeLink?.(); setLinked(null); setAutoplay(false); setDiffAnchor(null); setViewing(a) }}
          onClose={closeViewer}
        />
        </ReviewRequestContext.Provider>
      )}
      {workspaceFile && files && canOpenWorkspace && (
        <WorkspaceFileViewer
          key={`${workspaceFile.path}:${workspaceFile.line ?? ''}`}
          sessionId={sessionId}
          files={files}
          path={workspaceFile.path}
          line={workspaceFile.line}
          onClose={() => setWorkspaceFile(null)}
          onSaved={refreshArtifacts}
        />
      )}
      {linkMissing && (
        <div className="artifact-missing" role="status" data-testid="artifact-missing">
          That file no longer exists.
          <button type="button" className="artifact-btn" onClick={closeViewer}>Dismiss</button>
        </div>
      )}

      <AskContext.Provider value={askContext}>
      <DelegationContext.Provider value={delegationContext}>
      <ProposalContext.Provider value={proposalContext}>
      <ReviewRequestContext.Provider value={reviewContext}>
      <Transcript
        sessionId={sessionId}
        events={shownEvents}
        streaming={streaming}
        compact={compact}
        thinking={props.thinking}
        cards={cards}
        messageFooter={slots.messageFooter}
        describeUserMessage={props.describeUserMessage}
        liveIds={liveIds}
        showSkeleton={showSkeleton}
        loadingOlder={loadingOlder}
        hasOlder={hasOlder}
        onLoadOlder={s.loadOlder}
        starting={starting}
        attempt={startAttempt}
        startSession={transcriptSession}
        onStopSession={s.stop}
        clockOffset={clockOffset}
        readyCard={readyCard}
        working={working}
        waiting={waiting}
        since={turnSince}
        waitingOn={waitingOn}
        activity={activity}
        reasoning={s.thinking ? reasoning : null}
        background={background}
        queued={s.queued}
        queueState={queueState}
        steers={steers}
        pin={pin}
      />
      </ReviewRequestContext.Provider>
      </ProposalContext.Provider>
      </DelegationContext.Provider>
      </AskContext.Provider>

      {ended && (
        <div className="agent-offline ended" role="status" data-testid="ended-banner">
          <span className="agent-offline-dot" aria-hidden="true" />
          <span>
            <strong>This session has ended.</strong>{' '}
            {endLine ? `${endLine.text}. ` : ''}It can’t be resumed — start a new session to keep working.
          </span>
        </div>
      )}
      {status === 'disconnected' && !resumeUnderWay && (
        <div className="agent-offline" role="status" data-testid="offline-banner">
          <span className="agent-offline-dot" aria-hidden="true" />
          <span>
            {meta?.runtime === 'cluster' || !meta?.runtime
              ? <><strong>No pod is running for this session.</strong> It timed out or was stopped. Send a message and a new pod will start and pick up where it left off. Work that was committed is restored; uncommitted changes are not.</>
              : <><strong>This session is disconnected from its machine.</strong> Messages are delivered when it reconnects.</>}
          </span>
        </div>
      )}
      {notice && (
        <div className="agent-notice" role="status" data-testid="send-notice">{notice}</div>
      )}
      {askOpen && !props.readOnly && !inputLocked && (
        <div className="agent-composer-hint ask-open" data-testid="ask-composer-hint">
          The agent asked a question — what you send now answers it
        </div>
      )}
      {working && !askOpen && !props.readOnly && (
        <div className="agent-composer-hint" data-testid="composer-hint">
          {steers ? 'Working — Enter sends it to the agent · Esc interrupts' : 'Working — Enter queues your message · Esc interrupts'}
        </div>
      )}
      {slots.aboveComposer}
      {attachments.notice && <div className="agent-notice" role="status" data-testid="attach-notice">{attachments.notice}</div>}
      {files && !props.readOnly && (
        <input
          ref={attachInput}
          type="file"
          multiple
          className="agent-attach-input"
          data-testid="attach-input"
          tabIndex={-1}
          aria-hidden="true"
          onChange={e => {
            attachments.addFiles(e.target.files ?? [])
            e.target.value = ''
          }}
        />
      )}
      {!props.readOnly && <Composer
        draft={draft}
        onDraft={setDraft}
        placeholder={placeholder}
        locked={inputLocked}
        busy={busy}
        onSubmit={() => { void submit() }}
        onInterrupt={interrupt}
        attachments={attachments}
        onAttach={files ? () => attachInput.current?.click() : undefined}
        actions={slots.composerActions}
      />}
    </div>
    </RefContext.Provider>
    </ChatContext.Provider>
    </ClockOffsetContext.Provider>
  )
}
