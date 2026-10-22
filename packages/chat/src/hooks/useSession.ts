// useSession: one session over a Transport — its transcript (replayed from the end, then live;
// older pages are fetched one at a time, when the view asks for them), its streaming text, its status and link, the messages
// sent and not yet recorded, and its files. ChatView is built on it; an app that wants a chat of
// its own shape can use it alone.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { create } from 'zustand'
import { composeMessage, type MessageAttachment } from '../model/attachments'
import type { SessionMeta, Transport } from '../transport/types'
import type { AgentEvent, SessionStatus, UserMessagePayload } from '../types'
import { useArtifacts, type Artifacts } from './useArtifacts'
import { resumeSeq, useTranscriptStore } from './useTranscriptStore'
import { dismissedAsk } from '../model/asks'
import { isRelayInputSource } from '../model/userMessages'

/** The older part of a long transcript follows the newest page in pages of this many events. */
export const OLDER_PAGE = 200

/** A message sent and not yet recorded: shown at once, dropped as its real event arrives. id is
 *  also the client id it was sent with, which the daemon echoes on that event. stale: nothing
 *  recorded it in time (see QUEUED_IDLE_MS); it says it may not have been delivered and goes after
 *  QUEUED_LINGER_MS. */
export interface QueuedMessage { id: string; text: string; afterSeq: number; stale?: boolean; accepted?: boolean }

/** How long a session may sit idle after a turn that ended past a queued message, with the message
 *  still unrecorded, before its bubble gives up (the message was lost on the way: a daemon that
 *  dropped it, a send buffer that was full). */
export const QUEUED_IDLE_MS = 15_000
/** How long a bubble that gave up stays, saying so, before it goes. */
export const QUEUED_LINGER_MS = 8_000

/** A turn the person has just asked for, before the server has said anything: the working
 *  indicator starts on send, not a round trip later. */
export interface PendingTurn {
  /** Server time (ms) of the send. */
  since: number
  afterSeq: number
}

export interface UseSessionOptions {
  /** The session as the host knows it, when it has a live row of its own (the runner's session
   *  list). Given, it is used in place of what the transport's `session` answers and its status
   *  is the one that counts. */
  meta?: SessionMeta | null
  /** Commands that change a setting without starting a turn (default: /model <x>, /effort <x>);
   *  sending one shows no queued bubble and starts no working indicator. */
  noTurnCommands?: RegExp
}

export interface SessionHandle {
  sessionId: string
  events: AgentEvent[]
  streaming: string
  /** The main agent has a thinking block open (live only; model/workingActivity). */
  thinking: boolean
  /** The model has said something live since the newest retry notice (model/workingActivity). */
  answering: boolean
  /** null until the transport has said (a meta read or a status message). */
  status: SessionStatus | null
  connected: boolean
  meta: SessionMeta | null
  /** The first page has arrived and every forward page after it. */
  replayDone: boolean
  hasMore: boolean
  /** The server holds older events than the oldest held (fetched with loadOlder). */
  hasOlder: boolean
  /** An older page is being fetched right now. */
  loadingOlder: boolean
  /** Fetches the page before the oldest event held: one page, and nothing while one is in flight
   *  or when there is nothing older. */
  loadOlder: () => Promise<void>
  /** client clock − server clock (ms). */
  clockOffset: number
  lastSeq: number
  /** Sends the text (with the note naming the files, when any). queued=false: not connected,
   *  nothing was sent. opts.answerTo: the message answers that ask (see Transport.answersAsks). */
  send: (text: string, files?: MessageAttachment[], opts?: { answerTo?: string }) => Promise<{ queued: boolean }>
  /** Asks the server reported answered or closed (message_answered), by message id, with the
   *  answer ('' when none). */
  closedAsks: ReadonlyMap<string, string>
  /** Ends the session. */
  stop: () => Promise<void>
  /** Cuts the running turn short; absent when the transport cannot. */
  interrupt?: () => Promise<void>
  setModel?: (model?: string, effort?: string) => Promise<void>
  /** Sent messages the transcript has not recorded yet, oldest first. */
  queued: QueuedMessage[]
  /** Gives up on every message still queued: each says it may not have been delivered, then goes
   *  (ChatView calls it when a send went unanswered). */
  giveUpQueued: () => void
  pending: PendingTurn | null
  clearPending: () => void
  artifacts: Artifacts
}

const DEFAULT_NO_TURN = /^\/(model|effort)\s+\S/

// A client id for a message: the id of its queued bubble, which the daemon echoes on its event.
function newClientId(): string {
  const c = globalThis.crypto
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  return `q${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

/** The client ids the events echo: on the user_messages that recorded them, and on the errors that
 *  reported them dropped. */
export function echoedClientIds(events: AgentEvent[]): Set<string> {
  const ids = new Set<string>()
  for (const ev of events) {
    if (ev.kind !== 'error' && ev.kind !== 'user_message') continue
    const id = (ev.payload as { client_id?: unknown } | null)?.client_id
    if (typeof id === 'string' && id) ids.add(id)
  }
  return ids
}

// The queued messages whose real event has not arrived yet. A message is matched by its client id
// first: the user_message that echoes it (whatever its text: a slash skill, an answer to an ask),
// or the error of a session that dropped it (its queue was full). Events with no client id (a
// daemon older than the echo, a transport that cannot send one) are matched by text instead: each
// satisfies the oldest message still waiting with that text, sent before it was recorded, so two
// identical texts resolve one at a time, and a message that is never recorded holds up none after it.
export function unmatchedQueued(queued: QueuedMessage[], events: AgentEvent[]): QueuedMessage[] {
  if (queued.length === 0) return queued
  const echoed = echoedClientIds(events)
  const byText: AgentEvent[] = []
  for (const ev of events) {
    if (ev.kind !== 'user_message') continue
    const p = ev.payload as UserMessagePayload | null
    if (!p || p.source === 'system') continue
    if (!(typeof p.client_id === 'string' && p.client_id)) byText.push(ev)
  }
  const left = queued.filter(q => !echoed.has(q.id))
  if (left.length === 0 || byText.length === 0) return left.length === queued.length ? queued : left
  const done = new Set<QueuedMessage>()
  for (const ev of byText) {
    const p = ev.payload as UserMessagePayload
    const text = typeof p.text === 'string' ? p.text.trim() : ''
    const q = left.find(m => !done.has(m) && (ev.seq ?? 0) > m.afterSeq && m.text === text)
    if (q) done.add(q)
  }
  if (done.size === 0) return left.length === queued.length ? queued : left
  return left.filter(m => !done.has(m))
}

const NO_EVENTS: AgentEvent[] = []
const NO_CLOSED: ReadonlyMap<string, string> = new Map()

// A transport owns its pending messages for the life of the page. Keeping this outside the
// mounted view preserves them across navigation without sharing messages between connections.
const outboxes = new WeakMap<Transport, ReturnType<typeof makeOutbox>>()
const EMPTY_QUEUE: QueuedMessage[] = []
function makeOutbox() {
  return create<{ sessions: Record<string, QueuedMessage[]> }>(() => ({ sessions: {} }))
}
function outboxFor(transport: Transport) {
  let outbox = outboxes.get(transport)
  if (!outbox) { outbox = makeOutbox(); outboxes.set(transport, outbox) }
  return outbox
}

export function useSession(transport: Transport, sessionId: string, opts: UseSessionOptions = {}): SessionHandle {
  const transcript = useTranscriptStore(s => s.sessions[sessionId])
  const ingest = useTranscriptStore(s => s.ingest)
  const ingestMany = useTranscriptStore(s => s.ingestMany)
  const resetEvents = useTranscriptStore(s => s.resetEvents)
  const appendStreaming = useTranscriptStore(s => s.appendStreaming)
  const ingestReplayDone = useTranscriptStore(s => s.ingestReplayDone)
  const ingestOlder = useTranscriptStore(s => s.ingestOlder)

  // Keyed by session, so a switch never shows the previous session's status or row.
  const [live, setLive] = useState<{ sessionId: string; status: SessionStatus } | null>(null)
  const [fetched, setFetched] = useState<{ sessionId: string; meta: SessionMeta } | null>(null)
  const [connected, setConnected] = useState(() => transport.connected())
  const outbox = outboxFor(transport)
  const queued = outbox(s => s.sessions[sessionId] ?? EMPTY_QUEUE)
  const setQueued = useCallback((update: QueuedMessage[] | ((q: QueuedMessage[]) => QueuedMessage[])) => {
    outbox.setState(s => {
      const current = s.sessions[sessionId] ?? EMPTY_QUEUE
      const next = typeof update === 'function' ? update(current) : update
      return next === current ? s : { sessions: { ...s.sessions, [sessionId]: next } }
    })
  }, [outbox, sessionId])
  const [closed, setClosed] = useState<{ sessionId: string; asks: ReadonlyMap<string, string> } | null>(null)
  const [pendingState, setPending] = useState<{ sessionId: string; turn: PendingTurn } | null>(null)
  const pending = pendingState?.sessionId === sessionId ? pendingState.turn : null
  // The older page in flight (keyed by session and request, so a page left over from before a
  // switch or a reset does not show as loading, or stop showing, for the one asked for now).
  const [olderFetch, setOlderFetch] = useState<{ sessionId: string; token: number } | null>(null)

  const hostMeta = opts.meta ?? null
  const fetchedMeta = fetched?.sessionId === sessionId ? fetched.meta : null
  const liveStatus = live?.sessionId === sessionId ? live.status : null
  const status: SessionStatus | null = hostMeta ? hostMeta.status : liveStatus ?? fetchedMeta?.status ?? null
  // The same object until something in it changes, so what is drawn from it is not redrawn.
  const meta: SessionMeta | null = useMemo(
    () => hostMeta ?? (fetchedMeta ? { ...fetchedMeta, status: status ?? fetchedMeta.status } : null),
    [hostMeta, fetchedMeta, status],
  )

  // The session row, read once per session and again when it ends (that is when the end reason
  // and who ended it are known). Not asked for when the host has its own row.
  const terminal = status === 'stopped' || status === 'ended' || status === 'error'
  useEffect(() => {
    if (hostMeta) return
    let alive = true
    transport.session(sessionId).then(
      m => { if (alive) setFetched({ sessionId, meta: m }) },
      () => {},
    )
    return () => { alive = false }
  }, [transport, sessionId, hostMeta, terminal])

  // Older pages, one at a time and only one per call: one after the tail is in (so the view has
  // something to scroll into), one after a reconnect or on a later visit (a page a drop cut short
  // is asked for again), and otherwise only when the view asks (the reader scrolled to the top of
  // what is held). Never the whole history up front.
  const olderInFlight = useRef(false)
  const olderToken = useRef(0)
  const alive = useRef(true)
  // Bumped when the held transcript is dropped (a reset) or the subscription starts again: a page
  // asked for before then belongs to what was dropped and is not put in.
  const generation = useRef(0)
  const pumpOlder = useCallback(async (): Promise<void> => {
    if (olderInFlight.current || !alive.current) return
    const t = useTranscriptStore.getState().sessions[sessionId]
    if (!t || !t.hasOlder || t.firstSeq <= 0) return
    olderInFlight.current = true
    const gen = generation.current
    const token = ++olderToken.current
    setOlderFetch({ sessionId, token })
    try {
      let page
      try {
        page = await transport.loadOlder(sessionId, t.firstSeq, OLDER_PAGE)
      } catch {
        // A drop cut the page short: the next connection asks for it again.
        return
      }
      if (!alive.current || gen !== generation.current) return
      ingestOlder(sessionId, page)
    } finally {
      if (gen === generation.current) olderInFlight.current = false
      setOlderFetch(cur => (cur?.token === token ? null : cur))
    }
  }, [transport, sessionId, ingestOlder])

  // Subscribe on mount, resuming from the last seq held (the store keeps the transcript across
  // visits); the transport resubscribes on every reconnect by itself. A replay page comes as one
  // batch (one store update); a transport that finds the gap too long to replay forward resets,
  // and the transcript loads from its end as on a first visit.
  useEffect(() => {
    alive.current = true
    generation.current++
    olderInFlight.current = false
    // Resume from where the transcript held is complete: the last seq, or where an unfinished
    // forward replay had reached.
    const lastSeq = resumeSeq(useTranscriptStore.getState().sessions[sessionId])
    // While the replay is coming in, the statuses it passes through are not the session's status
    // (a session idle for hours was running, waiting and idle again on its way here): only the
    // last is kept, and applied once when the replay is done. After that, each status is applied as it comes.
    // The replay is also over when the link drops or this subscription ends (no replay_done is coming
    // then), and a status that ends the session is never held.
    let replaying = true
    let held: SessionStatus | null = null
    const applyStatus = (s: SessionStatus) => setLive({ sessionId, status: s })
    const release = () => {
      replaying = false
      if (held !== null) { applyStatus(held); held = null }
    }
    const unsubscribe = transport.subscribe(sessionId, { afterSeq: lastSeq, tail: lastSeq === 0 }, {
      onEvent: ev => { if (ev.session_id === sessionId) ingest(ev) },
      onEvents: evs => ingestMany(sessionId, evs),
      onReset: () => {
        replaying = true
        held = null
        generation.current++
        olderInFlight.current = false
        setOlderFetch(null)
        resetEvents(sessionId)
      },
      onStreaming: delta => appendStreaming(sessionId, delta),
      onReplayDone: done => {
        ingestReplayDone(sessionId, done)
        // The newest page of a tail (older) or the last forward page ends the replay; a page with more behind it does not.
        if (replaying && (done.older || !done.hasMore)) {
          release()
        }
        if (done.older) void pumpOlder()
      },
      onStatus: s => {
        if (replaying && s !== 'stopped' && s !== 'ended' && s !== 'error') { held = s; return }
        held = null
        applyStatus(s)
      },
      onAskClosed: (id, answer, dismissed, dismissedBy) => setClosed(cur => {
        const asks = new Map(cur?.sessionId === sessionId ? cur.asks : NO_CLOSED)
        asks.set(id, dismissed ? dismissedAsk(dismissedBy) : answer)
        return { sessionId, asks }
      }),
      onConnection: c => {
        setConnected(c)
        if (!c) release()
        if (c) void pumpOlder()
      },
    })
    // A visit after the first: one older page, as after the tail (the rest when the view asks).
    if (lastSeq > 0) void pumpOlder()
    return () => {
      alive.current = false
      release()
      unsubscribe()
    }
  }, [transport, sessionId, ingest, ingestMany, resetEvents, appendStreaming, ingestReplayDone, pumpOlder])

  // A working indicator belongs to the mounted session; messages stay in its outbox.
  const [queuedFor, setQueuedFor] = useState(sessionId)
  if (queuedFor !== sessionId) {
    setQueuedFor(sessionId)
    setPending(null)
  }

  const events = transcript?.events ?? NO_EVENTS
  const clockOffset = transcript?.clockOffset ?? 0
  const noTurn = opts.noTurnCommands ?? DEFAULT_NO_TURN

  const send = useCallback(async (typed: string, files: MessageAttachment[] = [], opts: { answerTo?: string } = {}) => {
    const text = composeMessage(typed, files)
    const id = newClientId()
    const r = await transport.send(sessionId, text, { clientId: id, ...(opts.answerTo ? { answerTo: opts.answerTo } : {}) })
    if (!r.queued) return r
    if (!noTurn.test(text)) {
      // Show the message at once. The transcript only records it when the session picks it up:
      // after the running turn, or once a stopped or disconnected session has woken up.
      const t = useTranscriptStore.getState().sessions[sessionId]
      const afterSeq = t?.lastSeq ?? 0
      setQueued(q => [...q, { id, text, afterSeq }])
      if (status !== 'running') setPending({ sessionId, turn: { since: Date.now() - clockOffset, afterSeq } })
    }
    return r
  }, [transport, sessionId, noTurn, status, clockOffset, setQueued])

  const stop = useCallback(() => transport.stop(sessionId), [transport, sessionId])
  const interrupt = useMemo(() => transport.interrupt
    ? async () => {
        await transport.interrupt!(sessionId)
        setPending(null)
      }
    : undefined, [transport, sessionId])
  const setModel = useMemo(() => transport.setModel
    ? (model?: string, effort?: string) => transport.setModel!(sessionId, model, effort)
    : undefined, [transport, sessionId])
  const clearPending = useCallback(() => setPending(null), [])

  const visibleQueued = useMemo(() => {
    const byId = new Map(queued.map(q => [q.id, q]))
    for (const ev of events) {
      if (ev.kind !== 'input_accepted') continue
      const p = ev.payload as { client_id?: string; text?: string; source?: string }
      if (typeof p.client_id !== 'string' || typeof p.text !== 'string') continue
      // Blerg's own message to the agent (a question's answer, a relayed decision) is never the
      // person's queued bubble. An older server's event has no source: its client ids say it.
      if (isRelayInputSource(p.source) || (p.source === undefined && /^(question|review)-/.test(p.client_id))) continue
      byId.set(p.client_id, { id: p.client_id, text: p.text, afterSeq: ev.seq ?? 0, accepted: true })
    }
    return unmatchedQueued([...byId.values()], events)
  }, [queued, events])
  // A message whose id an event echoed is forgotten, so a reset (the events dropped and loaded again
  // from the end) cannot bring its bubble back. (One matched by text stays held: the same event
  // would match the next bubble of that text.)
  const echoed = useMemo(() => echoedClientIds(events), [events])
  useEffect(() => {
    setQueued(q => q.some(m => echoed.has(m.id)) ? q.filter(m => !echoed.has(m.id)) : q)
  }, [echoed, setQueued])

  const giveUpQueued = useCallback(() => {
    setQueued(q => (q.some(m => !m.stale && !m.accepted) ? q.map(m => (m.stale || m.accepted ? m : { ...m, stale: true })) : q))
  }, [setQueued])

  // A turn ended past a message and the session has been idle since, without recording it: after
  // QUEUED_IDLE_MS the message is taken to be lost.
  const idleSince = useMemo(() => {
    if (status !== 'idle' || visibleQueued.every(m => m.stale || m.accepted)) return null
    let ended = -1
    for (let i = events.length - 1; i >= 0; i--) {
      const ev = events[i]
      if (ev.kind === 'turn_done' || (ev.kind === 'status_changed' && (ev.payload as { status?: unknown } | null)?.status === 'idle')) {
        ended = ev.seq ?? -1
        break
      }
    }
    const lost = visibleQueued.filter(m => !m.stale && !m.accepted && ended > m.afterSeq).map(m => m.id)
    return lost.length > 0 ? lost.join(' ') : null
  }, [status, events, visibleQueued])
  useEffect(() => {
    if (!idleSince) return
    const ids = new Set(idleSince.split(' '))
    const t = setTimeout(() => {
      setQueued(q => q.map(m => (ids.has(m.id) && !m.stale ? { ...m, stale: true } : m)))
    }, QUEUED_IDLE_MS)
    return () => clearTimeout(t)
  }, [idleSince, setQueued])

  // A bubble that gave up says so for a moment, then goes.
  const staleIds = visibleQueued.filter(m => m.stale).map(m => m.id).join(' ')
  useEffect(() => {
    if (!staleIds) return
    const ids = new Set(staleIds.split(' '))
    const t = setTimeout(() => setQueued(q => q.filter(m => !ids.has(m.id))), QUEUED_LINGER_MS)
    return () => clearTimeout(t)
  }, [staleIds, setQueued])
  const artifacts = useArtifacts(sessionId, transport.files ?? null, events)

  return {
    sessionId,
    events,
    streaming: transcript?.streaming ?? '',
    thinking: transcript?.thinking ?? false,
    answering: transcript?.answering ?? false,
    status,
    connected,
    meta,
    replayDone: transcript?.replayDone ?? false,
    hasMore: transcript?.hasMore ?? false,
    hasOlder: transcript?.hasOlder ?? false,
    loadingOlder: olderFetch?.sessionId === sessionId,
    loadOlder: pumpOlder,
    clockOffset,
    lastSeq: transcript?.lastSeq ?? 0,
    send,
    closedAsks: closed?.sessionId === sessionId ? closed.asks : NO_CLOSED,
    stop,
    interrupt,
    setModel,
    queued: visibleQueued,
    giveUpQueued,
    pending,
    clearPending,
    artifacts,
  }
}
