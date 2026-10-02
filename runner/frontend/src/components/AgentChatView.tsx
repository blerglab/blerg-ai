// Chat/timeline view for agent-kind sessions: structured transcript cards,
// streaming assistant text, composer, model/effort controls, interrupt.
import { Fragment, memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ToolGroup } from './AgentToolCalls'
import { HIDDEN_KINDS, buildTimeline, isVisibleEvent } from '../lib/toolGroups'
import { DayDivider, TimeLabel } from './TimeLabel'
import { ClockOffsetContext } from '../lib/clockOffset'
import { getDraft, setDraft as saveDraft } from '../lib/drafts'
import { dayKey, parseTs, turnDuration } from '../lib/timeLabel'
import { send, onMessage, onOpen } from '../ws'
import { useAgentTranscript } from './../hooks/useAgentTranscript'
import { apiFetch } from '../apiFetch'

// A long transcript is loaded from its end: this many of the newest events come first, so the
// conversation shows at once, and the older ones follow in pages of OLDER_PAGE in the background.
const INITIAL_TAIL = 200
const OLDER_PAGE = 200
import { useEngineModels } from '../lib/engineModels'
import { ENGINE_LABELS } from '../lib/runtimes'
import { latestStartAttempt, placeholderAttempt, startPanelVisible, withSessionOutcome } from '../lib/startStages'
import { useSessionStore } from '../hooks/useSessionStore'
import type {
  AgentEvent,
  AgentEventsReplayDone,
  ArtifactPayload,
  AssistantTextPayload,
  CheckInPayload,
  CompactionPayload,
  ErrorPayload,
  MessagingPayload,
  ModelChangedPayload,
  SessionInfo,
  StatusPayload,
  SubagentPayload,
  ToolCallPayload,
  ToolResultPayload,
  TurnDonePayload,
  UserMessagePayload,
} from '../types'
import Markdown from './Markdown'
import StartProgress from './StartProgress'
import WorkingIndicator from './WorkingIndicator'
import { liveCalls, oldestLive } from '../lib/liveCalls'
import CapabilitiesPanel from './CapabilitiesPanel'
import { useCapabilities } from '../hooks/useCapabilities'
import { useArtifacts } from '../hooks/useArtifacts'
import { ArtifactActionsContext } from '../lib/artifactActions'
import { fileCount, findLatest, groupArtifacts, type ArtifactInfo } from '../lib/artifacts'
import ArtifactCard from './ArtifactCard'
import ArtifactsPanel from './ArtifactsPanel'
import ArtifactViewer from './ArtifactViewer'
import { ComposerChips, MessageText } from './AttachmentChips'
import { describeSessionEnd } from '../lib/sessionEnd'
import { useAttachments } from '../hooks/useAttachments'
import { composeMessage } from '../lib/attachments'
import { capabilityCount } from '../lib/capabilities'
import { useTicker } from '../hooks/useTicker'
import './AgentChatView.css'
import { repoDetail } from '../lib/noRepo'


const COMPACT_KEY = 'blerg.agent.compactTools'

// Compact tool calls are on unless the person turned them off. Storage can be
// missing or throw (private windows, blocked site data): the default stands.
function loadCompact(): boolean {
  try {
    return localStorage.getItem(COMPACT_KEY) !== '0'
  } catch {
    return true
  }
}

function saveCompact(on: boolean) {
  try {
    localStorage.setItem(COMPACT_KEY, on ? '1' : '0')
  } catch {
    // Not persisted; the choice still holds for this view.
  }
}

// The ?artifact=<id> of the address: the link `blerg-runner publish` prints.
function artifactParam(): string | null {
  return new URLSearchParams(window.location.search).get('artifact')
}

// Takes ?artifact= out of the address (history.replaceState: no navigation, no new entry), so a
// reload does not open the viewer again.
function clearArtifactParam() {
  const u = new URL(window.location.href)
  if (!u.searchParams.has('artifact')) return
  u.searchParams.delete('artifact')
  window.history.replaceState(window.history.state, '', u.pathname + u.search + u.hash)
}

interface QueuedMessage { id: string; text: string; afterSeq: number }

// Placeholder bubbles shown while a session's transcript loads: the rough shape of a conversation
// (your message, a longer reply, a tool row, another exchange) so the page does not start blank.
function HistorySkeleton() {
  const bars = (widths: number[]) => widths.map((w, i) => <span key={i} className="sk-bar" style={{ width: `${w}%` }} />)
  return (
    <div className="agent-skeleton" role="status" aria-label="Loading conversation" data-testid="history-loading">
      <div className="sk-bubble user">{bars([55, 30])}</div>
      <div className="sk-bubble assistant">{bars([92, 88, 70])}</div>
      <div className="sk-bubble tool">{bars([48])}</div>
      <div className="sk-bubble assistant">{bars([85, 95, 60])}</div>
      <div className="sk-bubble user">{bars([40])}</div>
      <div className="sk-bubble assistant">{bars([90, 75])}</div>
      <div className="sk-bubble tool">{bars([60])}</div>
      <div className="sk-bubble assistant">{bars([95, 82, 90, 55])}</div>
      <div className="sk-bubble user">{bars([65, 25])}</div>
      <div className="sk-bubble assistant">{bars([88, 92, 70])}</div>
      <div className="sk-bubble tool">{bars([42])}</div>
      <div className="sk-bubble assistant">{bars([90, 80])}</div>
    </div>
  )
}

// The queued messages whose real user_message event has not arrived yet. Each
// event can satisfy only the oldest waiting message, so two identical texts
// queued back to back resolve one at a time.
function unmatchedQueued(queued: QueuedMessage[], events: AgentEvent[]): QueuedMessage[] {
  let head = 0
  for (const ev of events) {
    if (head >= queued.length) break
    if (ev.kind !== 'user_message') continue
    const p = ev.payload as UserMessagePayload
    if (p?.source === 'system') continue
    if ((ev.seq ?? 0) > queued[head].afterSeq && p.text.trim() === queued[head].text) head++
  }
  return queued.slice(head)
}

// A card for an event that is neither a tool call nor a group of them. Memoized
// on the event so appending to the transcript re-renders only what is new.
const EventRow = memo(function EventRow({ ev, turnStart, footer, footerStart }: { ev: AgentEvent; turnStart?: number; footer?: AgentEvent; footerStart?: number }) {
  return renderEvent(ev, NO_RESULTS, turnStart, footer, footerStart)
})
const NO_RESULTS = new Map<string, ToolResultPayload>()

// A turn's footer (stop reason, model, tokens, time taken) is shown inside the
// assistant bubble it closes, not as a floating line between bubbles. seq is the
// rendered sequence with null wherever a non-event (a tool card or group) sits:
// only an assistant reply IMMEDIATELY followed by its turn_done is paired.
function pairFooters(seq: (AgentEvent | null)[]): { footers: Map<string, AgentEvent>; absorbed: Set<string> } {
  const footers = new Map<string, AgentEvent>()
  const absorbed = new Set<string>()
  for (let i = 0; i + 1 < seq.length; i++) {
    const a = seq[i]
    const b = seq[i + 1]
    if (a && b && a.kind === 'assistant_text' && b.kind === 'turn_done') {
      footers.set(a.client_event_id, b)
      absorbed.add(b.client_event_id)
    }
  }
  return { footers, absorbed }
}

// publishedURL extracts the URL from a push_mockup / push_screenshot result.
function publishedURL(result?: ToolResultPayload): string | null {
  if (!result || result.is_error) return null
  const m = result.output.match(/published:\s*(\S+)/i)
  return m ? m[1] : null
}

function ToolCard({ call, result, ts }: { call: ToolCallPayload; result?: ToolResultPayload; ts?: string }) {
  const [open, setOpen] = useState(false)
  const ok = result ? !result.is_error : undefined
  const url = (call.tool === 'push_mockup' || call.tool === 'push_screenshot') ? publishedURL(result) : null
  return (
    <div className={`agent-card tool ${ok === false ? 'tool-error' : ''}`} data-testid="tool-card">
      <button className="tool-header" onClick={() => setOpen(o => !o)}>
        <span className="tool-name">{call.tool}</span>
        <span className="tool-summary">{summarizeInput(call.input)}</span>
        <TimeLabel ts={ts} />
        <span className="tool-status">{result ? (ok ? '✓' : '✗') : '…'}</span>
      </button>
      {url && call.tool === 'push_mockup' && (
        <a className="publish-link" href={url} target="_blank" rel="noreferrer noopener" data-testid="mockup-link">
          🖼 Open mockup ↗
        </a>
      )}
      {url && call.tool === 'push_screenshot' && (
        <a href={url} target="_blank" rel="noreferrer noopener" data-testid="screenshot-link">
          <img className="publish-screenshot" src={url} alt="screenshot" loading="lazy" />
        </a>
      )}
      {open && (
        <pre className="tool-detail">
          {JSON.stringify(call.input, null, 2)}
          {result ? `\n─── result (${result.duration_ms}ms) ───\n${result.output}` : ''}
        </pre>
      )}
    </div>
  )
}

function summarizeInput(input: unknown): string {
  if (input && typeof input === 'object') {
    const o = input as Record<string, unknown>
    const v = o.command ?? o.path ?? o.pattern ?? o.name ?? o.prompt ?? o.question ?? o.body ?? ''
    if (typeof v === 'string') return v.length > 80 ? v.slice(0, 80) + '…' : v
  }
  return ''
}

function usageCost(p: TurnDonePayload): string {
  const u = p.usage
  return `${u.input_tokens} in · ${u.output_tokens} out${u.cache_read_tokens ? ` · ${u.cache_read_tokens} cached` : ''}`
}

interface AgentRule {
  id: string
  project: string
  content: string
  enabled: boolean
}

// RulesPanel: the human approval surface for agent-proposed standing rules.
function RulesPanel({ project }: { project: string }) {
  const [rules, setRules] = useState<AgentRule[]>([])
  const load = () => {
    void apiFetch(`/api/agent/rules?project=${encodeURIComponent(project)}`)
      .then(r => r.json())
      .then((data: AgentRule[]) => setRules(Array.isArray(data) ? data : []))
      .catch(() => {})
  }
  useEffect(load, [project])
  async function setEnabled(id: string, enabled: boolean) {
    await apiFetch(`/api/agent/rules/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    }).catch(() => {})
    load()
  }
  async function remove(id: string) {
    await apiFetch(`/api/agent/rules/${id}`, { method: 'DELETE' }).catch(() => {})
    load()
  }
  return (
    <div className="rules-panel" data-testid="rules-panel">
      {rules.length === 0 && <div className="rules-empty">No rules for {project}. The agent can propose rules with rule_propose; they take effect once you approve them here.</div>}
      {rules.map(r => (
        <div key={r.id} className={`rule-row ${r.enabled ? 'enabled' : 'pending'}`}>
          <span className="rule-status">{r.enabled ? '✓ active' : '⏳ pending'}</span>
          <span className="rule-content">{r.content}</span>
          <button onClick={() => void setEnabled(r.id, !r.enabled)}>
            {r.enabled ? 'Disable' : 'Approve'}
          </button>
          <button className="rule-delete" onClick={() => void remove(r.id)}>✕</button>
        </div>
      ))}
    </div>
  )
}

// A turn the user has just asked for, before the server has said anything:
// the working indicator starts on send, not a round trip later.
interface PendingTurn {
  since: number
  afterSeq: number
}

// Harness commands that change settings without starting a turn (the host
// intercepts "/model <x>" and "/effort <x>"; a bare "/model" is a prompt).
const NO_TURN_COMMAND = /^\/(model|effort)\s+\S/

// How long a sent message may go unanswered — no status change, no event —
// before the optimistic "Working…" gives up on it (the message was dropped:
// no daemon, a socket that died between the check and the write).
const PENDING_ACK_MS = 20_000

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

const RUNTIME_LABELS: Record<string, string> = {
  cluster: 'Cluster pod',
  docker: 'Local sandbox',
  daemon: 'This machine',
}

// ReadyCard heads the timeline once a session is live: what it is running,
// where, and since when — the answer to "did it start?".
function ReadyCard({ session, readyAt }: { session: SessionInfo; readyAt: number }) {
  const engine = ENGINE_LABELS[session.engine || 'claude'] ?? session.engine
  const model = session.model ? session.model.replace(/^claude-/, '') : 'engine default'
  const runtime = RUNTIME_LABELS[session.runtime || 'daemon'] ?? session.runtime
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
        <div><dt>Engine</dt><dd>{engine}</dd></div>
        <div><dt>Model</dt><dd>{model}{session.effort ? ` · ${session.effort}` : ''}</dd></div>
        <div><dt>Repo</dt><dd data-testid="ready-repo">{repoDetail(session.repo)}</dd></div>
        <div><dt>Runs on</dt><dd>{runtime}</dd></div>
      </dl>
    </div>
  )
}

// turnStartTimes maps each turn_done event to when its turn began: the time of
// the first real user message since the previous turn ended (messages sent
// while a turn runs are folded into it). Absent when unknown.
function turnStartTimes(events: AgentEvent[]): Map<string, number> {
  const out = new Map<string, number>()
  let start: number | null = null
  for (const ev of events) {
    if (ev.kind === 'user_message') {
      if ((ev.payload as UserMessagePayload | undefined)?.source === 'system') continue
      // A message sent mid-turn is part of the turn already under way: the clock runs from the first.
      if (start === null) start = parseTs(ev.ts)
    } else if (ev.kind === 'turn_done') {
      if (start !== null) out.set(ev.client_event_id, start)
      start = null
    } else if (ev.kind === 'status_changed' && (ev.payload as { status?: unknown } | undefined)?.status === 'idle') {
      // Idle without a turn_done (a message that never started a turn): the next turn starts afresh.
      start = null
    }
  }
  return out
}

// dayBreaksOf: for consecutive [key, time] stamps, the keys whose local date
// differs from the previous dated item, mapped to their time. Undated items
// neither start a break nor reset the date.
function dayBreaksOf(stamps: Array<[string, number | null]>): Map<string, number> {
  const out = new Map<string, number>()
  let prev: number | null = null
  for (const [key, ms] of stamps) {
    if (ms === null) continue
    const day = dayKey(ms)
    if (prev !== null && day !== prev) out.set(key, ms)
    prev = day
  }
  return out
}

export default function AgentChatView({ session }: { session: SessionInfo }) {
  const sessionId = session.id
  const transcript = useAgentTranscript(s => s.sessions[sessionId])
  const ingest = useAgentTranscript(s => s.ingest)
  const ingestReplayDone = useAgentTranscript(s => s.ingestReplayDone)
  const statusChangedAt = useSessionStore(s => s.statusChangedAt[sessionId])
  const [draft, setDraftState] = useState(() => getDraft(sessionId))
  function setDraft(text: string) {
    setDraftState(text)
    saveDraft(sessionId, text)
  }
  // Files attached to the message being written (uploaded as soon as they are added).
  const attachments = useAttachments(sessionId)
  const attachInput = useRef<HTMLInputElement | null>(null)
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  const [showRules, setShowRules] = useState(false)
  const [showCaps, setShowCaps] = useState(false)
  const [showFiles, setShowFiles] = useState(false)
  // The file open in the viewer (from a card or the panel), and the one a ?artifact= link names,
  // which opens as soon as the list is read.
  const [viewing, setViewing] = useState<ArtifactInfo | null>(null)
  const [linked, setLinked] = useState<string | null>(artifactParam)
  const [pending, setPending] = useState<PendingTurn | null>(null)
  // Messages sent while a turn is running. The backend queues them and only
  // records them in the transcript when the turn picks them up, so they are
  // shown here at once, dimmed, and dropped as their real event arrives.
  const [queued, setQueued] = useState<QueuedMessage[]>([])
  const [notice, setNotice] = useState<string | null>(null)
  const [compact, setCompact] = useState(loadCompact)
  const bottomRef = useRef<HTMLDivElement | null>(null)
  // Follow new output only while the reader is at the bottom.
  const stickRef = useRef(true)
  // The in-session switcher offers the same list the launch sheet does for
  // this session's engine, and the efforts of the model it is on now. A model
  // the list doesn't know (an alias, an older id) gets every level any model
  // of the engine offers; one that takes no effort (Haiku) gets no effort
  // control at all, and neither does an engine with no model list. A list a
  // daemon reports (Codex, Hermes) is asked of the session's own daemon.
  const engineModels = useEngineModels(session.engine ?? '', session.daemon_id || undefined)
  const currentModel = engineModels.models.find(m => m.id === session.model)
  const sessionEfforts: readonly string[] = currentModel
    ? currentModel.efforts
    : Array.from(new Set(engineModels.models.flatMap(m => m.efforts)))

  // Subscribe on mount and on every reconnect, resuming from lastSeq.
  useEffect(() => {
    const offEvent = onMessage<AgentEvent>('agent_event', ev => {
      if (ev.session_id === sessionId) ingest(ev)
    })
    const offDone = onMessage<AgentEventsReplayDone>('agent_events_replay_done', msg => {
      if (msg.session_id !== sessionId) return
      ingestReplayDone(msg)
      // The transcript is loaded from its end so it can be shown at once; the older part follows
      // in the background, a page at a time, until the start is reached.
      if (msg.older) {
        if (msg.has_older && msg.first_seq) {
          send({ type: 'subscribe_agent_events', session_id: sessionId, before_seq: msg.first_seq, limit: OLDER_PAGE })
        }
        return
      }
      // The server replays a page (200 events) at a time. A longer transcript
      // needs the following pages too, or everything between the first page
      // and the live tail is silently missing.
      if (msg.has_more) {
        send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: msg.last_seq })
      }
    })
    const offOpen = onOpen(() => {
      const t = useAgentTranscript.getState().sessions[sessionId]
      const lastSeq = t?.lastSeq ?? 0
      if (lastSeq === 0) {
        // First load: the newest events now, the rest behind them.
        send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: 0, tail: INITIAL_TAIL })
        return
      }
      send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: lastSeq })
      // A drop can have cut the background load short: pick it up where it stopped.
      if (t?.hasOlder && t.firstSeq > 0) {
        send({ type: 'subscribe_agent_events', session_id: sessionId, before_seq: t.firstSeq, limit: OLDER_PAGE })
      }
    })
    return () => {
      offEvent()
      offDone()
      offOpen()
      send({ type: 'unsubscribe_session', session_id: sessionId })
    }
  }, [sessionId, ingest, ingestReplayDone])

  const events = useMemo(() => transcript?.events ?? [], [transcript?.events])
  const streaming = transcript?.streaming ?? ''
  const capabilities = useCapabilities(sessionId, events)
  const capsCount = capabilityCount(capabilities?.payload)
  // Uploads leave no transcript event, so read the list again when one finishes or is removed, and
  // whenever the panel is opened or closed.
  const artifacts = useArtifacts(sessionId, events, `${attachments.done.length}:${showFiles}`)
  const linkedArtifact = linked ? artifacts.items.find(a => a.id === linked) ?? null : null
  const linkMissing = linked !== null && artifacts.status === 'ready' && !linkedArtifact
  // The viewer shows the list's own entry for the file when there is one: a card carries only its
  // own version, the list also knows how many versions the file has now.
  const picked = viewing ?? linkedArtifact
  const openArtifact = picked ? artifacts.items.find(a => a.id === picked.id) ?? picked : null
  const openLatest = openArtifact ? findLatest(artifacts.items, openArtifact) : undefined
  const latestVersions = useMemo(
    () => new Map(groupArtifacts(artifacts.items).map(g => [`${g.origin}\0${g.name}`, g.latest.version ?? 1])),
    [artifacts.items],
  )
  const artifactActions = useMemo(() => ({
    sessionId,
    view: setViewing,
    latestVersion: (a: ArtifactPayload) => latestVersions.get(`${a.origin === 'user' ? 'user' : 'agent'}\0${a.name}`),
  }), [sessionId, latestVersions])
  function closeViewer() {
    setViewing(null)
    setLinked(null)
    clearArtifactParam()
  }

  // ─── Start progress ───────────────────────────────────────────────────────
  const rawAttempt = useMemo(() => latestStartAttempt(events), [events])
  const attempt = withSessionOutcome(session, rawAttempt)
  const starting = startPanelVisible(session, rawAttempt)
  const hasUserMessage = events.some(ev => ev.kind === 'user_message' && (ev.payload as UserMessagePayload)?.source !== 'system')
  const live = session.status === 'running' || session.status === 'idle' || session.status === 'waiting'

  // ─── Working / waiting ────────────────────────────────────────────────────
  const terminal = session.status === 'error' || session.status === 'stopped'
  // A stopped or ended session has no process and cannot be resumed: say so and lock the composer.
  const ended = session.status === 'stopped' || session.status === 'ended'
  const inputLocked = session.status === 'starting' || ended
  const endLine = ended ? describeSessionEnd(session) : null
  const pendingActive = pending !== null && !terminal && !pendingSettled(pending, events)
  const waiting = session.status === 'waiting' && !pendingActive
  const working = !waiting && (pendingActive || session.status === 'running')
  // Times below are server time: event ts values are the server's clock, and
  // clockOffset (from the replay's server_time) maps the browser's onto it.
  const clockOffset = transcript?.clockOffset ?? 0
  const turnSince = pendingActive
    ? pending!.since
    : lastRunningSince(events) ?? (statusChangedAt !== undefined ? statusChangedAt - clockOffset : null)
  const now = useTicker(working) - clockOffset
  // Tool calls in flight: only while the session runs or waits, and only in
  // the turn now running, so a replayed orphan never shows a ticking clock.
  const inFlight = useMemo(
    () => liveCalls(events, session.status === 'running' || session.status === 'waiting'),
    [events, session.status],
  )
  const liveIds = useMemo(() => new Set(inFlight.map(c => c.callId)), [inFlight])
  const waitingOn = working ? oldestLive(inFlight) : null
  const since = turnSince ?? now

  // Give up on a sent message nothing has answered.
  const answered = pending !== null && (
    events.some(ev => (ev.seq ?? 0) > pending.afterSeq) ||
    session.status !== 'idle'
  )
  useEffect(() => {
    if (!pending || answered) return
    const t = setTimeout(() => {
      setPending(null)
      setNotice('No response from the session — it may not have received your message. Try sending it again.')
    }, PENDING_ACK_MS)
    return () => clearTimeout(t)
  }, [pending, answered])

  // Sent messages the transcript has not recorded yet: each is replaced by its
  // real event, in order. If the session ended or failed first, the message is
  // kept on screen, marked as not delivered, so it never just vanishes.
  const visibleQueued = useMemo(() => unmatchedQueued(queued, events), [queued, events])
  const queueState: 'failed' | 'queued' | 'sending' = terminal
    ? 'failed'
    : session.status === 'running'
      ? 'queued'
      : 'sending'

  // The transcript arrives in pages. While it is still loading, do not paint or
  // scroll it page by page (that is a visible storm of jumps); show it once,
  // already at the bottom.
  // The Claude Code (and native) engines take a message while a turn runs; the other engines hold it until
  // the turn ends, and the wording says which.
  const steers = !session.engine || session.engine === 'claude'
  const loadingHistory = !(transcript?.replayDone) || transcript?.hasMore === true
  // Older events still on their way in behind what is already on screen.
  const loadingOlder = !loadingHistory && transcript?.hasOlder === true
  // Until the transcript is complete, show placeholder bubbles instead of an empty or filling-in
  // area (a session that is still starting has its own progress panel).
  const showSkeleton = loadingHistory && !starting
  useEffect(() => {
    if (loadingHistory) return
    if (stickRef.current) bottomRef.current?.scrollIntoView?.({ block: 'end' })
  }, [events.length, streaming, working, waiting, loadingHistory])

  // Older events arrive above what the reader is looking at. When they are not at the bottom, move
  // the view down by exactly what was added so the text under their eyes does not jump.
  const timelineRef = useRef<HTMLDivElement | null>(null)
  const heldFirstSeq = useRef(0)
  const heldHeight = useRef(0)
  const firstSeq = events.find(e => e.seq != null)?.seq ?? 0
  useLayoutEffect(() => {
    const el = timelineRef.current
    if (!el) return
    const grewUpward = heldFirstSeq.current > 0 && firstSeq > 0 && firstSeq < heldFirstSeq.current
    if (grewUpward && !stickRef.current) el.scrollTop += el.scrollHeight - heldHeight.current
    heldFirstSeq.current = firstSeq
    heldHeight.current = el.scrollHeight
  })

  function onTimelineScroll(e: React.UIEvent<HTMLDivElement>) {
    const el = e.currentTarget
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  }

  function submit() {
    const typed = draft.trim()
    const files = attachments.done
    if ((!typed && files.length === 0) || attachments.uploading || inputLocked) return
    // The note naming the files is what makes the agent fetch them.
    const text = composeMessage(typed, files)
    if (send({ type: 'agent_user_message', session_id: sessionId, text }) === false) {
      // Not connected: nothing was sent. Keep the text; don't pretend.
      setNotice('Not connected — your message was not sent. It is still in the box; send it again once reconnected.')
      return
    }
    setNotice(null)
    setDraft('')
    attachments.clear()
    stickRef.current = true
    if (!NO_TURN_COMMAND.test(text)) {
      // Show the message at once. The transcript only records it when the
      // session picks it up: after the running turn, or once a stopped or
      // disconnected session has woken up.
      const afterSeq = transcript?.lastSeq ?? 0
      setQueued(q => [...q, { id: `q${Date.now()}-${q.length}`, text, afterSeq }])
      if (!working) setPending({ since: Date.now() - clockOffset, afterSeq })
    }
  }

  const hasFiles = (e: React.DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes('Files')
  const dropProps = inputLocked ? {} : {
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

  function interrupt() {
    send({ type: 'interrupt_session', session_id: sessionId })
    setPending(null)
  }

  const busy = session.status === 'running' || session.status === 'waiting' || pendingActive

  // Pair tool_results with their calls so each renders as one card (legacy
  // view), or fold runs of calls into groups (compact view).
  const resultsByCall = useMemo(() => {
    const m = new Map<string, ToolResultPayload>()
    if (compact) return m
    for (const ev of events) {
      if (ev.kind === 'tool_result') {
        const p = ev.payload as ToolResultPayload
        m.set(p.call_id, p)
      }
    }
    return m
  }, [events, compact])
  const timelineItems = useMemo(() => (compact ? buildTimeline(events) : []), [events, compact])
  // When each turn began (the user message before its turn_done), for "took 42 s".
  const turnStarts = useMemo(() => turnStartTimes(events), [events])
  // Turn footers folded into the assistant bubble that closes each turn.
  const footerPairs = useMemo(() => pairFooters(compact
    ? timelineItems.map(item => (item.type === 'event' ? item.ev : null))
    : events.filter(ev => !HIDDEN_KINDS.has(ev.kind))), [events, timelineItems, compact])
  // Where the local date changes between consecutive visible items: the
  // divider goes before the item, keyed like it, carrying that item's time.
  const dayBreaks = useMemo(() => {
    const stamps: Array<[string, number | null]> = compact
      ? timelineItems.map(item => item.type === 'tools'
          ? [`tools:${item.key}`, parseTs(item.entries[0].ev.ts)]
          : item.type === 'card'
            ? [item.entry.ev.client_event_id, parseTs(item.entry.ev.ts)]
            : [item.ev.client_event_id, parseTs(item.ev.ts)])
      : events.filter(isVisibleEvent).map(ev => [ev.client_event_id, parseTs(ev.ts)])
    return dayBreaksOf(stamps)
  }, [events, timelineItems, compact])

  const placeholder = ended
    ? 'This session has ended — start a new session to keep working'
    : session.status === 'starting'
    ? 'The session is starting — you can message it once it is ready'
    : live && !hasUserMessage
      ? 'Message the agent to get started… (/model, /effort, /<skill>)'
      : 'Message the agent… (/model, /effort, /<skill>)'

  const startedAt = Date.parse(session.started_at) || now
  const readyAt = rawAttempt?.readyAt ?? startedAt

  return (
    <ClockOffsetContext.Provider value={clockOffset}>
    <ArtifactActionsContext.Provider value={artifactActions}>
    <div className="agent-chat" data-testid="agent-chat" {...dropProps}>
      {dragging && <div className="drop-zone" role="status" data-testid="drop-zone">Drop files to attach</div>}
      <div className="agent-toolbar">
        <select
          aria-label="Model"
          value={session.model ?? ''}
          onChange={e => send({ type: 'set_session_model', session_id: sessionId, model: e.target.value })}
        >
          {!session.model && <option value="">model…</option>}
          {/* A session launched on an id or alias the list doesn't have
              (an older id, "sonnet") still shows what it is running. */}
          {session.model && !currentModel && <option value={session.model}>{session.model}</option>}
          {engineModels.models.map(m => (
            <option key={m.id} value={m.id} title={m.description || m.id}>{m.name}</option>
          ))}
        </select>
        {sessionEfforts.length > 0 && (
          <select
            aria-label="Effort"
            value={session.effort ?? ''}
            onChange={e => send({ type: 'set_session_model', session_id: sessionId, effort: e.target.value })}
          >
            {!session.effort && <option value="">effort…</option>}
            {session.effort && !sessionEfforts.includes(session.effort) && (
              <option value={session.effort}>{session.effort}</option>
            )}
            {sessionEfforts.map(m => (
              <option key={m} value={m}>{m}</option>
            ))}
          </select>
        )}
        <button
          className="agent-rules-toggle"
          data-testid="rules-toggle"
          aria-label="Rules"
          title="Rules"
          onClick={() => setShowRules(s => !s)}
        >
          <span className="tb-icon" aria-hidden="true">⚖</span>
          <span className="tb-label">Rules</span>
        </button>
        <button
          type="button"
          className="caps-toggle"
          data-testid="capabilities-toggle"
          aria-haspopup="dialog"
          aria-label={capabilities ? `Skills & plugins (${capsCount} loaded)` : 'Skills & plugins'}
          onClick={() => setShowCaps(true)}
        >
          <span className="tb-icon" aria-hidden="true">🧩</span>
          <span className="tb-label">Skills &amp; plugins</span>
          {capabilities && <span className="caps-badge" data-testid="capabilities-badge" aria-hidden="true">{capsCount}</span>}
        </button>
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
        <label className="compact-toggle" title="Fold runs of tool calls into one summary row">
          <input
            type="checkbox"
            checked={compact}
            aria-label="Compact tool calls"
            onChange={e => { setCompact(e.target.checked); saveCompact(e.target.checked) }}
          />
          <span className="tb-icon" aria-hidden="true">▤</span>
          <span className="tb-label">Compact tool calls</span>
        </label>
        {working && streaming && <WorkingIndicator since={since} now={now} waitingOn={waitingOn} compact />}
        {busy && (
          <button className="agent-stop" onClick={interrupt} aria-label="Stop" title="Stop (Esc)">
            <span className="tb-icon" aria-hidden="true">⏹</span>
            <span className="tb-label">Stop</span>
          </button>
        )}
      </div>

      {showRules && <RulesPanel project={session.repo} />}
      {showCaps && <CapabilitiesPanel report={capabilities} onClose={() => setShowCaps(false)} />}
      {showFiles && (
        <ArtifactsPanel
          sessionId={sessionId}
          items={artifacts.items}
          status={artifacts.status}
          onClose={() => setShowFiles(false)}
          onView={setViewing}
          onChanged={artifacts.refresh}
          onAttach={() => { setShowFiles(false); attachInput.current?.click() }}
        />
      )}
      {openArtifact && (
        <ArtifactViewer
          key={openArtifact.id}
          sessionId={sessionId}
          artifact={openArtifact}
          latest={openLatest}
          onOpen={a => { setLinked(null); clearArtifactParam(); setViewing(a) }}
          onClose={closeViewer}
        />
      )}
      {linkMissing && (
        <div className="artifact-missing" role="status" data-testid="artifact-missing">
          That file no longer exists.
          <button type="button" className="artifact-btn" onClick={closeViewer}>Dismiss</button>
        </div>
      )}

      {showSkeleton && <HistorySkeleton />}
      <div ref={timelineRef} className={`agent-timeline${showSkeleton ? ' loading' : ''}${loadingOlder ? ' backfilling' : ''}`} onScroll={onTimelineScroll}>
        {loadingOlder && (
          <div className="older-loading" data-testid="older-loading" role="status">
            <span className="queue-spinner" aria-hidden="true" /> Loading earlier messages…
          </div>
        )}
        {starting && attempt && <StartProgress attempt={attempt} session={session} clockOffset={clockOffset} />}
        {starting && !attempt && <StartProgress attempt={placeholderAttempt(startedAt)} session={session} clockOffset={clockOffset} />}
        {!starting && !loadingOlder && (live || hasUserMessage) && <ReadyCard session={session} readyAt={readyAt} />}
        {compact
          ? timelineItems.map(item => {
              if (item.type === 'tools') {
                const k = `tools:${item.key}`
                return (
                  <Fragment key={k}>
                    {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                    <ToolGroup entries={item.entries} live={liveIds} />
                  </Fragment>
                )
              }
              if (item.type === 'card') {
                const k = item.entry.ev.client_event_id
                return (
                  <Fragment key={k}>
                    {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                    <ToolCard call={item.entry.call} result={item.entry.result} ts={item.entry.ev.ts} />
                  </Fragment>
                )
              }
              const k = item.ev.client_event_id
              if (footerPairs.absorbed.has(k)) return null
              const footer = footerPairs.footers.get(k)
              return (
                <Fragment key={k}>
                  {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                  <EventRow
                    ev={item.ev}
                    turnStart={turnStarts.get(k)}
                    footer={footer}
                    footerStart={footer ? turnStarts.get(footer.client_event_id) : undefined}
                  />
                </Fragment>
              )
            })
          : events.filter(ev => !HIDDEN_KINDS.has(ev.kind) && !footerPairs.absorbed.has(ev.client_event_id)).map(ev => {
              const footer = footerPairs.footers.get(ev.client_event_id)
              return (
                <Fragment key={ev.client_event_id}>
                  {dayBreaks.has(ev.client_event_id) && <DayDivider ts={dayBreaks.get(ev.client_event_id)!} />}
                  {renderEvent(ev, resultsByCall, turnStarts.get(ev.client_event_id), footer,
                    footer ? turnStarts.get(footer.client_event_id) : undefined)}
                </Fragment>
              )
            })}
        {streaming && (
          <div className="agent-card assistant streaming" data-testid="streaming">
            <Markdown text={streaming} />
          </div>
        )}
        {visibleQueued.map((q, i) => (
          <div key={q.id} className={`agent-card user queued ${queueState}`} data-testid="queued-message">
            <span className="card-author">You</span>
            <MessageText text={q.text} />
            <div className="queue-status" role="status">
              {queueState === 'failed'
                ? <><span aria-hidden="true">⚠</span> Not delivered</>
                : <>
                    <span className="queue-spinner" aria-hidden="true" />
                    {queueState === 'sending'
                      ? 'Sending…'
                      : visibleQueued.length === 1
                        ? (steers ? 'Queued — the agent picks it up at its next step' : 'Queued')
                        : `Queued · ${i + 1} of ${visibleQueued.length}`}
                  </>}
            </div>
          </div>
        ))}
        {working && !streaming && <WorkingIndicator since={since} now={now} waitingOn={waitingOn} />}
        {waiting && (
          <div className="agent-waiting" role="status" aria-live="polite" data-testid="waiting">
            <span className="agent-waiting-dot" aria-hidden="true" />
            Waiting for your answer
          </div>
        )}
        <div ref={bottomRef} />
      </div>

      {ended && (
        <div className="agent-offline ended" role="status" data-testid="ended-banner">
          <span className="agent-offline-dot" aria-hidden="true" />
          <span>
            <strong>This session has ended.</strong>{' '}
            {endLine ? `${endLine.text}. ` : ''}It can’t be resumed — start a new session to keep working.
          </span>
        </div>
      )}
      {session.status === 'disconnected' && (
        <div className="agent-offline" role="status" data-testid="offline-banner">
          <span className="agent-offline-dot" aria-hidden="true" />
          <span>
            {session.runtime === 'cluster' || !session.runtime
              ? <><strong>No pod is running for this session.</strong> It timed out or was stopped. Send a message and a new pod will start and pick up where it left off. Work that was committed is restored; uncommitted changes are not.</>
              : <><strong>This session is disconnected from its machine.</strong> Messages are delivered when it reconnects.</>}
          </span>
        </div>
      )}
      {notice && (
        <div className="agent-notice" role="status" data-testid="send-notice">{notice}</div>
      )}
      {working && (
        <div className="agent-composer-hint" data-testid="composer-hint">
          {steers ? 'Working — Enter sends it to the agent · Esc interrupts' : 'Working — Enter queues your message · Esc interrupts'}
        </div>
      )}
      {attachments.notice && <div className="agent-notice" role="status" data-testid="attach-notice">{attachments.notice}</div>}
      <div className="agent-composer">
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
        <textarea
          value={draft}
          placeholder={placeholder}
          disabled={inputLocked}
          onChange={e => setDraft(e.target.value)}
          onPaste={e => {
            const files = Array.from(e.clipboardData?.files ?? [])
            if (files.length === 0) return
            // A copied file brings no text; a page with an image brings both, and keeps its text.
            if (!e.clipboardData.getData('text/plain')) e.preventDefault()
            attachments.addFiles(files)
          }}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              submit()
            } else if (e.key === 'Escape' && busy) {
              // Like the terminal: Esc stops the turn that is running.
              e.preventDefault()
              interrupt()
            }
          }}
        />
        <div className="agent-composer-actions">
          <button
            type="button"
            className="agent-attach"
            aria-label="Attach files"
            title="Attach files"
            disabled={inputLocked}
            onClick={() => attachInput.current?.click()}
          >
            📎
          </button>
          <button onClick={submit} disabled={(!draft.trim() && attachments.done.length === 0) || attachments.uploading || inputLocked}>Send</button>
        </div>
      </div>
      <ComposerChips items={attachments.items} onRemove={attachments.remove} />
    </div>
    </ArtifactActionsContext.Provider>
    </ClockOffsetContext.Provider>
  )
}

// The turn footer line: stop reason, model, usage, when, and how long it took.
function turnDoneText(ev: AgentEvent, turnStart?: number) {
  const p = ev.payload as TurnDonePayload
  const d = turnDuration(turnStart ?? null, parseTs(ev.ts))
  return (
    <>
      {p.stop_reason} · {p.model} · {usageCost(p)}
      {p.check_in_missing ? ' · ⚠ no completion check-in' : ''}
      {' · '}<TimeLabel ts={ev.ts} />
      {d ? ` · ${d}` : ''}
    </>
  )
}

function renderEvent(ev: AgentEvent, results: Map<string, ToolResultPayload>, turnStart?: number, footer?: AgentEvent, footerStart?: number) {
  const key = ev.client_event_id
  switch (ev.kind) {
    case 'user_message': {
      const p = ev.payload as UserMessagePayload
      if (p.source === 'system') return null // harness-injected reminders
      return (
        <div key={key} className={`agent-card user source-${p.source}`}>
          <span className="card-author">You</span>
          <MessageText text={p.text} />
          <div className="card-foot"><TimeLabel ts={ev.ts} /></div>
        </div>
      )
    }
    case 'assistant_text': {
      const p = ev.payload as AssistantTextPayload
      return (
        <div key={key} className="agent-card assistant">
          <Markdown text={p.text} />
          <div className="card-foot"><TimeLabel ts={ev.ts} /></div>
          {footer && <div className="turn-foot" data-testid="turn-done">{turnDoneText(footer, footerStart)}</div>}
        </div>
      )
    }
    case 'tool_call': {
      const p = ev.payload as ToolCallPayload
      return <ToolCard key={key} call={p} result={results.get(p.call_id)} ts={ev.ts} />
    }
    case 'tool_result':
      return null // rendered inside its ToolCard
    case 'check_in': {
      const p = ev.payload as CheckInPayload
      return (
        <div key={key} className={`agent-card checkin phase-${p.phase}`} data-testid="checkin">
          <span className="checkin-phase">{p.phase.replace('_', ' ')}</span>
          <span>{p.summary}</span>
          <TimeLabel ts={ev.ts} className="card-time-end" />
        </div>
      )
    }
    case 'ask':
    case 'update':
    case 'note': {
      const p = ev.payload as MessagingPayload
      return (
        <div key={key} className={`agent-card message kind-${ev.kind}`}>
          <span className="message-kind">{ev.kind}</span>
          <Markdown text={p.body} className="message-body" />
          {ev.kind === 'ask' && <span className="ask-hint">answer in the composer or Chat tab</span>}
          <TimeLabel ts={ev.ts} className="card-time-end" />
        </div>
      )
    }
    case 'model_changed': {
      const p = ev.payload as ModelChangedPayload
      return (
        <div key={key} className="agent-card marker">
          model → {p.model}{p.effort ? ` (${p.effort})` : ''}
        </div>
      )
    }
    case 'subagent_started': {
      const p = ev.payload as SubagentPayload
      return (
        <div key={key} className="agent-card subagent">
          ↳ {p.agent_type} [{p.model}]: {p.summary}
        </div>
      )
    }
    case 'subagent_done': {
      const p = ev.payload as SubagentPayload
      return (
        <div key={key} className="agent-card subagent done">
          ↲ {p.agent_type}: {p.summary}
        </div>
      )
    }
    case 'turn_done': {
      return (
        <div key={key} className="agent-card marker turn-done" data-testid="turn-done">
          {turnDoneText(ev, turnStart)}
        </div>
      )
    }
    case 'error': {
      const p = ev.payload as ErrorPayload
      return (
        <div key={key} className="agent-card error">⚠ {p.message} <TimeLabel ts={ev.ts} className="card-time-end" /></div>
      )
    }
    case 'compaction': {
      const p = ev.payload as CompactionPayload
      return (
        <div key={key} className="agent-card marker">✂ context compacted ({p.summary.length} char summary)</div>
      )
    }
    case 'artifact':
      return <ArtifactCard key={key} payload={ev.payload as ArtifactPayload} ts={ev.ts} />
    default: {
      const s = ev.payload as StatusPayload
      void s
      return null
    }
  }
}
