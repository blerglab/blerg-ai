// Chat/timeline view for agent-kind sessions: structured transcript cards,
// streaming assistant text, composer, model/effort controls, interrupt.
import { Fragment, memo, useEffect, useMemo, useRef, useState } from 'react'
import { ToolGroup } from './AgentToolCalls'
import { HIDDEN_KINDS, buildTimeline, isVisibleEvent } from '../lib/toolGroups'
import { DayDivider, TimeLabel } from './TimeLabel'
import { ClockOffsetContext } from '../lib/clockOffset'
import { dayKey, parseTs, turnDuration } from '../lib/timeLabel'
import { send, onMessage, onOpen } from '../ws'
import { useAgentTranscript } from './../hooks/useAgentTranscript'
import { apiFetch } from '../apiFetch'
import { useEngineModels } from '../lib/engineModels'
import { ENGINE_LABELS } from '../lib/runtimes'
import { latestStartAttempt, placeholderAttempt, startPanelVisible, withSessionOutcome } from '../lib/startStages'
import { useSessionStore } from '../hooks/useSessionStore'
import type {
  AgentEvent,
  AgentEventsReplayDone,
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

// A card for an event that is neither a tool call nor a group of them. Memoized
// on the event so appending to the transcript re-renders only what is new.
const EventRow = memo(function EventRow({ ev, turnStart }: { ev: AgentEvent; turnStart?: number }) {
  return renderEvent(ev, NO_RESULTS, turnStart)
})
const NO_RESULTS = new Map<string, ToolResultPayload>()

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
// the latest real user message before it. Absent when unknown.
function turnStartTimes(events: AgentEvent[]): Map<string, number> {
  const out = new Map<string, number>()
  let start: number | null = null
  for (const ev of events) {
    if (ev.kind === 'user_message') {
      if ((ev.payload as UserMessagePayload | undefined)?.source === 'system') continue
      start = parseTs(ev.ts)
    } else if (ev.kind === 'turn_done' && start !== null) {
      out.set(ev.client_event_id, start)
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
  const [draft, setDraft] = useState('')
  const [showRules, setShowRules] = useState(false)
  const [showCaps, setShowCaps] = useState(false)
  const [pending, setPending] = useState<PendingTurn | null>(null)
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
      if (msg.session_id === sessionId) ingestReplayDone(msg)
    })
    const offOpen = onOpen(() => {
      const lastSeq = useAgentTranscript.getState().sessions[sessionId]?.lastSeq ?? 0
      send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: lastSeq })
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

  // ─── Start progress ───────────────────────────────────────────────────────
  const rawAttempt = useMemo(() => latestStartAttempt(events), [events])
  const attempt = withSessionOutcome(session, rawAttempt)
  const starting = startPanelVisible(session, rawAttempt)
  const hasUserMessage = events.some(ev => ev.kind === 'user_message' && (ev.payload as UserMessagePayload)?.source !== 'system')
  const live = session.status === 'running' || session.status === 'idle' || session.status === 'waiting'

  // ─── Working / waiting ────────────────────────────────────────────────────
  const terminal = session.status === 'error' || session.status === 'stopped'
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

  useEffect(() => {
    if (stickRef.current) bottomRef.current?.scrollIntoView?.({ block: 'end' })
  }, [events.length, streaming, working, waiting])

  function onTimelineScroll(e: React.UIEvent<HTMLDivElement>) {
    const el = e.currentTarget
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  }

  function submit() {
    const text = draft.trim()
    if (!text || session.status === 'starting') return
    if (send({ type: 'agent_user_message', session_id: sessionId, text }) === false) {
      // Not connected: nothing was sent. Keep the text; don't pretend.
      setNotice('Not connected — your message was not sent. It is still in the box; send it again once reconnected.')
      return
    }
    setNotice(null)
    setDraft('')
    stickRef.current = true
    if (!NO_TURN_COMMAND.test(text)) {
      setPending({ since: Date.now() - clockOffset, afterSeq: transcript?.lastSeq ?? 0 })
    }
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

  const placeholder = session.status === 'starting'
    ? 'The session is starting — you can message it once it is ready'
    : live && !hasUserMessage
      ? 'Message the agent to get started… (/model, /effort, /<skill>)'
      : 'Message the agent… (/model, /effort, /<skill>)'

  const startedAt = Date.parse(session.started_at) || now
  const readyAt = rawAttempt?.readyAt ?? startedAt

  return (
    <ClockOffsetContext.Provider value={clockOffset}>
    <div className="agent-chat" data-testid="agent-chat">
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
          onClick={() => setShowRules(s => !s)}
        >
          ⚖ Rules
        </button>
        <button
          type="button"
          className="caps-toggle"
          data-testid="capabilities-toggle"
          aria-haspopup="dialog"
          aria-label={capabilities ? `Skills & plugins (${capsCount} loaded)` : 'Skills & plugins'}
          onClick={() => setShowCaps(true)}
        >
          🧩 Skills &amp; plugins
          {capabilities && <span className="caps-badge" data-testid="capabilities-badge" aria-hidden="true">{capsCount}</span>}
        </button>
        <label className="compact-toggle" title="Fold runs of tool calls into one summary row">
          <input
            type="checkbox"
            checked={compact}
            onChange={e => { setCompact(e.target.checked); saveCompact(e.target.checked) }}
          />
          Compact tool calls
        </label>
        {working && streaming && <WorkingIndicator since={since} now={now} waitingOn={waitingOn} compact />}
        {busy && (
          <button className="agent-stop" onClick={interrupt}>
            ⏹ Stop
          </button>
        )}
      </div>

      {showRules && <RulesPanel project={session.repo} />}
      {showCaps && <CapabilitiesPanel report={capabilities} onClose={() => setShowCaps(false)} />}

      <div className="agent-timeline" onScroll={onTimelineScroll}>
        {starting && attempt && <StartProgress attempt={attempt} session={session} clockOffset={clockOffset} />}
        {starting && !attempt && <StartProgress attempt={placeholderAttempt(startedAt)} session={session} clockOffset={clockOffset} />}
        {!starting && (live || hasUserMessage) && <ReadyCard session={session} readyAt={readyAt} />}
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
              return (
                <Fragment key={k}>
                  {dayBreaks.has(k) && <DayDivider ts={dayBreaks.get(k)!} />}
                  <EventRow ev={item.ev} turnStart={turnStarts.get(k)} />
                </Fragment>
              )
            })
          : events.filter(ev => !HIDDEN_KINDS.has(ev.kind)).map(ev => (
              <Fragment key={ev.client_event_id}>
                {dayBreaks.has(ev.client_event_id) && <DayDivider ts={dayBreaks.get(ev.client_event_id)!} />}
                {renderEvent(ev, resultsByCall, turnStarts.get(ev.client_event_id))}
              </Fragment>
            ))}
        {streaming && (
          <div className="agent-card assistant streaming" data-testid="streaming">
            <Markdown text={streaming} />
          </div>
        )}
        {working && !streaming && <WorkingIndicator since={since} now={now} waitingOn={waitingOn} />}
        {waiting && (
          <div className="agent-waiting" role="status" aria-live="polite" data-testid="waiting">
            <span className="agent-waiting-dot" aria-hidden="true" />
            Waiting for your answer
          </div>
        )}
        <div ref={bottomRef} />
      </div>

      {notice && (
        <div className="agent-notice" role="status" data-testid="send-notice">{notice}</div>
      )}
      <div className="agent-composer">
        <textarea
          value={draft}
          placeholder={placeholder}
          disabled={session.status === 'starting'}
          onChange={e => setDraft(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              submit()
            }
          }}
        />
        <button onClick={submit} disabled={!draft.trim() || session.status === 'starting'}>Send</button>
      </div>
    </div>
    </ClockOffsetContext.Provider>
  )
}

function renderEvent(ev: AgentEvent, results: Map<string, ToolResultPayload>, turnStart?: number) {
  const key = ev.client_event_id
  switch (ev.kind) {
    case 'user_message': {
      const p = ev.payload as UserMessagePayload
      if (p.source === 'system') return null // harness-injected reminders
      return (
        <div key={key} className={`agent-card user source-${p.source}`}>
          <Markdown text={p.text} />
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
      const p = ev.payload as TurnDonePayload
      return (
        <div key={key} className="agent-card marker turn-done" data-testid="turn-done">
          {p.stop_reason} · {p.model} · {usageCost(p)}
          {p.check_in_missing ? ' · ⚠ no completion check-in' : ''}
          {' · '}<TimeLabel ts={ev.ts} />
          {(() => { const d = turnDuration(turnStart ?? null, parseTs(ev.ts)); return d ? ` · ${d}` : '' })()}
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
    default: {
      const s = ev.payload as StatusPayload
      void s
      return null
    }
  }
}
