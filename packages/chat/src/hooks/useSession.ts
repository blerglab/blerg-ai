// useSession: one session over a Transport — its transcript (replayed from the end, then paged
// backwards in the background, then live), its streaming text, its status and link, the messages
// sent and not yet recorded, and its files. ChatView is built on it; an app that wants a chat of
// its own shape can use it alone.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { composeMessage } from '../model/attachments'
import type { SessionMeta, Transport } from '../transport/types'
import type { AgentEvent, SessionStatus, UserMessagePayload } from '../types'
import { useArtifacts, type Artifacts } from './useArtifacts'
import { useTranscriptStore } from './useTranscriptStore'

/** The older part of a long transcript follows the newest page in pages of this many events. */
export const OLDER_PAGE = 200

/** A message sent and not yet recorded: shown at once, dropped as its real event arrives. */
export interface QueuedMessage { id: string; text: string; afterSeq: number }

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
  /** null until the transport has said (a meta read or a status message). */
  status: SessionStatus | null
  connected: boolean
  meta: SessionMeta | null
  /** The first page has arrived and every forward page after it. */
  replayDone: boolean
  hasMore: boolean
  /** Older events are still on their way in behind what is held. */
  hasOlder: boolean
  loadOlder: () => Promise<void>
  /** client clock − server clock (ms). */
  clockOffset: number
  lastSeq: number
  /** Sends the text (with the note naming the files, when any). queued=false: not connected,
   *  nothing was sent. */
  send: (text: string, files?: Array<{ name: string; size: number }>) => Promise<{ queued: boolean }>
  /** Ends the session. */
  stop: () => Promise<void>
  /** Cuts the running turn short; absent when the transport cannot. */
  interrupt?: () => Promise<void>
  setModel?: (model?: string, effort?: string) => Promise<void>
  /** Sent messages the transcript has not recorded yet, oldest first. */
  queued: QueuedMessage[]
  pending: PendingTurn | null
  clearPending: () => void
  artifacts: Artifacts
}

const DEFAULT_NO_TURN = /^\/(model|effort)\s+\S/

// The queued messages whose real user_message event has not arrived yet. Each
// event can satisfy only the oldest waiting message, so two identical texts
// queued back to back resolve one at a time.
export function unmatchedQueued(queued: QueuedMessage[], events: AgentEvent[]): QueuedMessage[] {
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

const NO_EVENTS: AgentEvent[] = []

export function useSession(transport: Transport, sessionId: string, opts: UseSessionOptions = {}): SessionHandle {
  const transcript = useTranscriptStore(s => s.sessions[sessionId])
  const ingest = useTranscriptStore(s => s.ingest)
  const appendStreaming = useTranscriptStore(s => s.appendStreaming)
  const ingestReplayDone = useTranscriptStore(s => s.ingestReplayDone)
  const ingestOlder = useTranscriptStore(s => s.ingestOlder)

  // Keyed by session, so a switch never shows the previous session's status or row.
  const [live, setLive] = useState<{ sessionId: string; status: SessionStatus } | null>(null)
  const [fetched, setFetched] = useState<{ sessionId: string; meta: SessionMeta } | null>(null)
  const [connected, setConnected] = useState(() => transport.connected())
  const [queued, setQueued] = useState<QueuedMessage[]>([])
  const [pending, setPending] = useState<PendingTurn | null>(null)

  const hostMeta = opts.meta ?? null
  const fetchedMeta = fetched?.sessionId === sessionId ? fetched.meta : null
  const liveStatus = live?.sessionId === sessionId ? live.status : null
  const status: SessionStatus | null = hostMeta ? hostMeta.status : liveStatus ?? fetchedMeta?.status ?? null
  const meta: SessionMeta | null = hostMeta ?? (fetchedMeta ? { ...fetchedMeta, status: status ?? fetchedMeta.status } : null)

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

  // Older pages: after the tail is in, and after a reconnect, keep asking for the page before
  // the oldest held until the start is reached. One page in flight at a time.
  const olderInFlight = useRef(false)
  const alive = useRef(true)
  const pumpOlder = useCallback(async (): Promise<void> => {
    if (olderInFlight.current) return
    olderInFlight.current = true
    try {
      for (;;) {
        const t = useTranscriptStore.getState().sessions[sessionId]
        if (!alive.current || !t || !t.hasOlder || t.firstSeq <= 0) return
        let page
        try {
          page = await transport.loadOlder(sessionId, t.firstSeq, OLDER_PAGE)
        } catch {
          // A drop cut the page short: the next connection picks it up where it stopped.
          return
        }
        if (!alive.current) return
        ingestOlder(sessionId, page)
      }
    } finally {
      olderInFlight.current = false
    }
  }, [transport, sessionId, ingestOlder])

  // Subscribe on mount, resuming from the last seq held (the store keeps the transcript across
  // visits); the transport resubscribes on every reconnect by itself.
  useEffect(() => {
    alive.current = true
    olderInFlight.current = false
    const t = useTranscriptStore.getState().sessions[sessionId]
    const lastSeq = t?.lastSeq ?? 0
    const unsubscribe = transport.subscribe(sessionId, { afterSeq: lastSeq, tail: lastSeq === 0 }, {
      onEvent: ev => { if (ev.session_id === sessionId) ingest(ev) },
      onStreaming: delta => appendStreaming(sessionId, delta),
      onReplayDone: done => {
        ingestReplayDone(sessionId, done)
        if (done.older) void pumpOlder()
      },
      onStatus: s => setLive({ sessionId, status: s }),
      onConnection: c => {
        setConnected(c)
        if (c) void pumpOlder()
      },
    })
    // A visit after the first: the older pages may still be owed.
    if (lastSeq > 0) void pumpOlder()
    return () => {
      alive.current = false
      unsubscribe()
    }
  }, [transport, sessionId, ingest, appendStreaming, ingestReplayDone, pumpOlder])

  // Another session: nothing queued or pending carries over.
  const [queuedFor, setQueuedFor] = useState(sessionId)
  if (queuedFor !== sessionId) {
    setQueuedFor(sessionId)
    setQueued([])
    setPending(null)
  }

  const events = transcript?.events ?? NO_EVENTS
  const clockOffset = transcript?.clockOffset ?? 0
  const noTurn = opts.noTurnCommands ?? DEFAULT_NO_TURN

  const send = useCallback(async (typed: string, files: Array<{ name: string; size: number }> = []) => {
    const text = composeMessage(typed, files)
    const r = await transport.send(sessionId, text)
    if (!r.queued) return r
    if (!noTurn.test(text)) {
      // Show the message at once. The transcript only records it when the session picks it up:
      // after the running turn, or once a stopped or disconnected session has woken up.
      const t = useTranscriptStore.getState().sessions[sessionId]
      const afterSeq = t?.lastSeq ?? 0
      setQueued(q => [...q, { id: `q${Date.now()}-${q.length}`, text, afterSeq }])
      if (status !== 'running') setPending({ since: Date.now() - clockOffset, afterSeq })
    }
    return r
  }, [transport, sessionId, noTurn, status, clockOffset])

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

  const visibleQueued = useMemo(() => unmatchedQueued(queued, events), [queued, events])
  const artifacts = useArtifacts(sessionId, transport.files ?? null, events)

  return {
    sessionId,
    events,
    streaming: transcript?.streaming ?? '',
    status,
    connected,
    meta,
    replayDone: transcript?.replayDone ?? false,
    hasMore: transcript?.hasMore ?? false,
    hasOlder: transcript?.hasOlder ?? false,
    loadOlder: pumpOlder,
    clockOffset,
    lastSeq: transcript?.lastSeq ?? 0,
    send,
    stop,
    interrupt,
    setModel,
    queued: visibleQueued,
    pending,
    clearPending,
    artifacts,
  }
}
