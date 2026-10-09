// ChatView: a whole chat with one session over a Transport — the toolbar, the transcript, the
// composer, the Files panel and viewer. The host adds what is its own through slots (a model
// picker, a rules panel) and themes it through the --chat-* tokens.
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { CardRegistry } from '../cards/registry'
import { useAttachments } from '../hooks/useAttachments'
import { useSession, type PendingTurn, type SessionHandle } from '../hooks/useSession'
import { useTicker } from '../hooks/useTicker'
import { findLatest, groupArtifacts, fileCount } from '../model/artifacts'
import { ClockOffsetContext } from '../model/clockOffset'
import { liveCalls, oldestLive } from '../model/liveCalls'
import { mergeReviews, parseReviewFile, reviewFileName, type ReviewFile } from '../model/review'
import { describeSessionEnd } from '../model/sessionEnd'
import { latestStartAttempt, placeholderAttempt, startPanelVisible, withSessionOutcome } from '../model/startStages'
import { getDraft, loadCompact, localStorageChatStorage, saveCompact, setDraft as saveDraft, type ChatStorage } from '../model/storage'
import { themeStyle, type ThemeTokens } from '../theme'
import type { ArtifactInfo, SessionMeta, Transport } from '../transport/types'
import type { AgentEvent, ArtifactPayload, StatusPayload, UserMessagePayload } from '../types'
import ArtifactViewer from './ArtifactViewer'
import Composer from './Composer'
import { ChatContext, type ChatContextValue } from './context'
import FilesPanel from './FilesPanel'
import Transcript from './Transcript'
import WorkingIndicator from './WorkingIndicator'
import type { DescribeUserMessage, MessageFooter } from './cards/EventCards'
import './chat.css'

// How long a sent message may go unanswered — no status change, no event —
// before the optimistic "Working…" gives up on it (the message was dropped:
// no daemon, a socket that died between the check and the write).
const PENDING_ACK_MS = 20_000

export interface ChatSlots {
  /** Leads the toolbar (the runner's model and effort pickers, its Rules and Skills buttons). */
  header?: ReactNode
  /** Beside Send. */
  composerActions?: ReactNode
  /** Under a user or assistant card. */
  messageFooter?: MessageFooter
  /** Heads a live transcript in place of the default "Session ready" card. */
  readyCard?: (meta: SessionMeta, readyAt: number) => ReactNode
}

export interface ChatViewProps {
  session: string
  transport: Transport
  cards?: CardRegistry
  /** Token values set on the chat's root, over whatever the stylesheets say. */
  theme?: ThemeTokens
  slots?: ChatSlots
  /** Drafts and the compact-tools choice; default localStorage behind try/catch. */
  storage?: ChatStorage
  /** Commands that change a setting without starting a turn (default /model <x> and /effort <x>). */
  noTurnCommands?: RegExp
  /** A file to open in the viewer as soon as the list is read (the runner's ?artifact= link). */
  linkedArtifact?: string | null
  /** The linked file was opened or dismissed: the host may take it out of its address. */
  onLinkedArtifactConsumed?: () => void
  /** The session as the host knows it, when it has a live row (see useSession). */
  meta?: SessionMeta | null
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
  className?: string
}

// pendingSettled: something after the send says the turn is over (or never
// started): a turn end, an error, or the loop going back to idle.
function pendingSettled(p: PendingTurn, events: AgentEvent[]): boolean {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if ((ev.seq ?? 0) <= p.afterSeq) break
    if (ev.kind === 'turn_done' || ev.kind === 'error') return true
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
  const model = meta.model ? meta.model.replace(/^claude-/, '') : 'engine default'
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
        <div><dt>Model</dt><dd>{model}{meta.effort ? ` · ${meta.effort}` : ''}</dd></div>
        {children}
        {meta.runtime && <div><dt>Runs on</dt><dd>{meta.runtime}</dd></div>}
      </dl>
    </div>
  )
}

export default function ChatView(props: ChatViewProps) {
  const { transport, cards, slots = {}, storage = localStorageChatStorage } = props
  const sessionId = props.session
  const s: SessionHandle = useSession(transport, sessionId, { meta: props.meta, noTurnCommands: props.noTurnCommands })
  const { events, streaming, status, meta } = s
  const files = transport.files ?? null

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
  const attachments = useAttachments(sessionId, files)
  const attachInput = useRef<HTMLInputElement | null>(null)
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  const [showFiles, setShowFiles] = useState(false)
  // The file open in the viewer (from a card or the panel), and the one a link names, which
  // opens as soon as the list is read.
  const [viewing, setViewing] = useState<ArtifactInfo | null>(null)
  const linkedProp = props.linkedArtifact ?? null
  const [linked, setLinked] = useState<string | null>(linkedProp)
  const [seenLink, setSeenLink] = useState(linkedProp)
  if (seenLink !== linkedProp) {
    setSeenLink(linkedProp)
    setLinked(linkedProp)
  }
  const [notice, setNotice] = useState<string | null>(null)
  const [compact, setCompact] = useState(() => loadCompact(storage))
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
  const chatContext = useMemo<ChatContextValue>(() => ({
    sessionId,
    files,
    view: (a: ArtifactInfo, opts?: { autoplay?: boolean }) => { setAutoplay(!!opts?.autoplay); setViewing(a) },
    latestVersion: (a: ArtifactPayload) => latestVersions.get(`${a.origin === 'user' ? 'user' : 'agent'}\0${a.name}`),
    byName: (name: string) => findLatest(artifacts.items, { name, origin: 'user' }),
    // The review and mark-up modes send the way the composer does: every file uploaded first, then
    // the message with the attachment note, then the list read again for the new versions.
    submitFeedback: async (text: string, toUpload: File[]) => {
      if (!files) return false
      const uploaded: Array<{ name: string; size: number }> = []
      for (const f of toUpload) {
        const up = await files.upload(sessionId, f)
        uploaded.push({ name: up.name, size: up.size })
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
  }), [sessionId, files, latestVersions, artifacts.items, send, refreshArtifacts])
  const consumeLink = props.onLinkedArtifactConsumed
  function closeViewer() {
    setAutoplay(false)
    setViewing(null)
    if (linked !== null) consumeLink?.()
    setLinked(null)
  }

  // ─── Start progress ───────────────────────────────────────────────────────
  const startSession = useMemo(() => ({ status: status ?? 'starting', error_reason: meta?.error_reason, message: meta?.message }), [status, meta?.error_reason, meta?.message])
  const rawAttempt = useMemo(() => latestStartAttempt(events), [events])
  const attempt = withSessionOutcome(startSession, rawAttempt)
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
  const waiting = status === 'waiting' && !pendingActive
  const working = !waiting && (pendingActive || status === 'running')
  // Times below are server time: event ts values are the server's clock, and
  // clockOffset (from the replay's server_time) maps the browser's onto it.
  const clockOffset = s.clockOffset
  const turnSince = pendingActive ? pending!.since : lastRunningSince(events)
  const now = useTicker(working) - clockOffset
  // Tool calls in flight: only while the session runs or waits, and only in
  // the turn now running, so a replayed orphan never shows a ticking clock.
  const inFlight = useMemo(
    () => liveCalls(events, status === 'running' || status === 'waiting'),
    [events, status],
  )
  const liveIds = useMemo(() => new Set(inFlight.map(c => c.callId)), [inFlight])
  const waitingOn = working ? oldestLive(inFlight) : null
  const since = turnSince ?? now

  // Give up on a sent message nothing has answered.
  const answered = pending !== null && (
    events.some(ev => (ev.seq ?? 0) > pending.afterSeq) ||
    status !== 'idle'
  )
  const clearPending = s.clearPending
  useEffect(() => {
    if (!pending || answered) return
    const t = setTimeout(() => {
      clearPending()
      setNotice('No response from the session — it may not have received your message. Try sending it again.')
    }, PENDING_ACK_MS)
    return () => clearTimeout(t)
  }, [pending, answered, clearPending])

  // Sent messages the transcript has not recorded yet: each is replaced by its
  // real event, in order. If the session ended or failed first, the message is
  // kept on screen, marked as not delivered, so it never just vanishes.
  const queueState: 'failed' | 'queued' | 'sending' = terminal
    ? 'failed'
    : status === 'running'
      ? 'queued'
      : 'sending'

  // The Claude Code (and native) engines take a message while a turn runs; the other engines hold it until
  // the turn ends, and the wording says which.
  const steers = !meta?.engine || meta.engine === 'claude'
  const loadingHistory = !s.replayDone || s.hasMore
  // Older events still on their way in behind what is already on screen.
  const loadingOlder = !loadingHistory && s.hasOlder
  // Until the transcript is complete, show placeholder bubbles instead of an empty or filling-in
  // area (a session that is still starting has its own progress panel).
  const showSkeleton = loadingHistory && !starting

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

  const interrupt = s.interrupt ? () => { void s.interrupt!().catch(() => {}) } : undefined
  const busy = status === 'running' || status === 'waiting' || pendingActive

  const placeholder = ended
    ? 'This session has ended — start a new session to keep working'
    : status === 'starting' || status === null
    ? 'The session is starting — you can message it once it is ready'
    : props.placeholder
      ? props.placeholder
      : live && !hasUserMessage
        ? 'Message the agent to get started… (/model, /effort, /<skill>)'
        : 'Message the agent… (/model, /effort, /<skill>)'

  const startedAt = (meta ? Date.parse(meta.started_at) : NaN) || now
  const readyAt = rawAttempt?.readyAt ?? startedAt
  const readyCard = meta && (live || hasUserMessage)
    ? (slots.readyCard ? slots.readyCard(meta, readyAt) : <ReadyCard meta={meta} readyAt={readyAt} />)
    : null
  const startAttempt = starting ? (attempt ?? placeholderAttempt(startedAt)) : null

  return (
    <ClockOffsetContext.Provider value={clockOffset}>
    <ChatContext.Provider value={chatContext}>
    <div className={`agent-chat${props.className ? ` ${props.className}` : ''}`} data-testid="agent-chat" style={props.theme ? themeStyle(props.theme) : undefined} {...dropProps}>
      {dragging && <div className="drop-zone" role="status" data-testid="drop-zone">Drop files to attach</div>}
      <div className="agent-toolbar">
        {slots.header}
        {files && (
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
        <label className="compact-toggle" title="Fold runs of tool calls into one summary row">
          <input
            type="checkbox"
            checked={compact}
            aria-label="Compact tool calls"
            onChange={e => { setCompact(e.target.checked); saveCompact(storage, e.target.checked) }}
          />
          <span className="tb-icon" aria-hidden="true">▤</span>
          <span className="tb-label">Compact tool calls</span>
        </label>
        {working && streaming && <WorkingIndicator since={since} now={now} waitingOn={waitingOn} compact />}
        {busy && interrupt && (
          <button className="agent-stop" onClick={interrupt} aria-label="Stop" title="Stop (Esc)">
            <span className="tb-icon" aria-hidden="true">⏹</span>
            <span className="tb-label">Stop</span>
          </button>
        )}
      </div>

      {showFiles && files && (
        <FilesPanel
          sessionId={sessionId}
          files={files}
          items={artifacts.items}
          status={artifacts.status}
          onClose={() => setShowFiles(false)}
          onView={setViewing}
          onChanged={artifacts.refresh}
          onAttach={() => { setShowFiles(false); attachInput.current?.click() }}
          publishHint={props.publishHint}
        />
      )}
      {openArtifact && files && (
        <ArtifactViewer
          key={openArtifact.id}
          sessionId={sessionId}
          files={files}
          artifact={openArtifact}
          latest={openLatest}
          autoplay={autoplay}
          onOpen={a => { if (linked !== null) consumeLink?.(); setLinked(null); setAutoplay(false); setViewing(a) }}
          onClose={closeViewer}
        />
      )}
      {linkMissing && (
        <div className="artifact-missing" role="status" data-testid="artifact-missing">
          That file no longer exists.
          <button type="button" className="artifact-btn" onClick={closeViewer}>Dismiss</button>
        </div>
      )}

      <Transcript
        events={events}
        streaming={streaming}
        compact={compact}
        cards={cards}
        messageFooter={slots.messageFooter}
        describeUserMessage={props.describeUserMessage}
        liveIds={liveIds}
        showSkeleton={showSkeleton}
        loadingOlder={loadingOlder}
        starting={starting}
        attempt={startAttempt}
        startSession={status ? { id: sessionId, status } : undefined}
        onStopSession={s.stop}
        clockOffset={clockOffset}
        readyCard={readyCard}
        working={working}
        waiting={waiting}
        since={since}
        now={now}
        waitingOn={waitingOn}
        queued={s.queued}
        queueState={queueState}
        steers={steers}
        pin={pin}
      />

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
      {working && !props.readOnly && (
        <div className="agent-composer-hint" data-testid="composer-hint">
          {steers ? 'Working — Enter sends it to the agent · Esc interrupts' : 'Working — Enter queues your message · Esc interrupts'}
        </div>
      )}
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
    </ChatContext.Provider>
    </ClockOffsetContext.Provider>
  )
}
