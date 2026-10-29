// useLiveStateServer: the session panel's Live tab read from the server (GET /api/sessions/{id}/live:
// the whole event log's open asks, running subagents and background tasks), with the events that came
// after the reply folded in (foldLive), so the panel moves at once between two reads and a page that
// has loaded only the end of a long transcript still lists what started long ago. It reads while the
// session page shows an agent session (the header badge counts from it, open panel or not): at once,
// when the transcript is loaded again (a reconnect), when `refresh` changes (the session's status,
// the side panel opening or its Capabilities tab coming on screen), when the page comes back (focus, visibility) and when the transcript
// store takes an event that changes the live state (isLiveTrigger). Every read sends
// If-None-Match; one runs at a time, reads start at least LIVE_MIN_GAP_MS apart, and a trigger that
// comes too early or during a read gets one trailing read (useTopics' pattern). All of it is one
// session's: a new sessionId starts over and a reply for the previous one is dropped, but a session
// read before shows its last reply at once (and is asked with its ETag). A server without the route
// answers 'no-route' and is not asked again for that session: the page falls back to the chat's
// useLiveState.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { subscribeChildren } from './useChildrenSignal'
import { apiFetch } from '../apiFetch'
import { LIVE_FOLD_KINDS, foldLive, isLiveTrigger, type LiveReply, type LiveView } from '../lib/liveState'

export const LIVE_MIN_GAP_MS = 5000

export type LiveServerState =
  | { status: 'loading' }
  /** clockOffset: client clock − server clock (ms) when the reply came. */
  | { status: 'ready'; view: LiveView; reply: LiveReply; clockOffset: number }
  /** message: "HTTP <status>" for a reply that was not a 200; absent when the request itself failed. */
  | { status: 'error'; view: LiveView | null; reply: LiveReply | null; clockOffset: number; message?: string }
  | { status: 'no-route' }

const LOADING: LiveServerState = { status: 'loading' }
const NOT_READ: Shown = { status: 'loading' }
const NO_EVENTS: AgentEvent[] = []

/** A reply as read: with the server clock's offset when it came (client − server, ms). */
interface Held { reply: LiveReply; offset: number }

type Shown =
  | { status: 'loading' }
  | { status: 'ready'; held: Held }
  | { status: 'error'; held: Held | null; message?: string }
  | { status: 'no-route' }

interface Flight {
  sessionId: string
  alive: boolean
  active: boolean
  inFlight: boolean
  trailing: boolean
  lastStart: number
  timer: ReturnType<typeof setTimeout> | null
  etag: string
  held: Held | null
  /** The server answered that it has no such route: it is not asked again for this session. */
  noRoute: boolean
}

/** How many sessions' last replies a page keeps, to show one at once when it comes back to it. */
const KEEP = 20

/** An older server: its app page (a 200 that is not JSON), or an API-only ingress's plain 404/405. */
const isNoRoute = (status: number) => status === 200 || status === 404 || status === 405

function countTriggers(events: readonly AgentEvent[]): number {
  let n = 0
  for (const ev of events) if (isLiveTrigger(ev)) n++
  return n
}

/** The events foldLive would read after a reply through `through` (newest at the end). */
function foldable(events: readonly AgentEvent[], through: number): AgentEvent[] {
  let i = events.length
  while (i > 0) {
    const s = events[i - 1].seq
    if (s !== undefined && s <= through) break
    i--
  }
  const out: AgentEvent[] = []
  for (; i < events.length; i++) if (LIVE_FOLD_KINDS.has(events[i].kind)) out.push(events[i])
  return out
}

/** Server time now, moving by the minute (what has had no sign of life for 12 h stops counting). */
function useMinute(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(t)
  }, [])
  return now
}

// A child refresh changes the panel, but a freshly parsed copy of the parent's unchanged
// checklist/hold must not invalidate the memoized chat. Keep only those shared chat inputs.
// Only content-equal inputs share references, so advancing this memo during a discarded render is safe.
function liveViewSelector() {
  let previous: { sessionId: string; view: LiveView } | null = null
  return (sessionId: string, next: LiveView): LiveView => {
    const prior = previous?.sessionId === sessionId ? previous.view : null
    if (prior?.checklist && next.checklist && JSON.stringify(prior.checklist.items) === JSON.stringify(next.checklist.items)) {
      next = { ...next, checklist: { ...next.checklist, items: prior.checklist.items } }
    }
    if (prior?.mcpHold && next.mcpHold && prior.mcpHold.since === next.mcpHold.since && prior.mcpHold.until === next.mcpHold.until) {
      next = { ...next, mcpHold: prior.mcpHold }
    }
    previous = { sessionId, view: next }
    return next
  }
}

export function useLiveStateServer(sessionId: string, enabled: boolean, refresh: unknown): LiveServerState {
  const [shown, setShown] = useState<{ sessionId: string; s: Shown }>({ sessionId, s: NOT_READ })
  const flight = useRef<Flight | null>(null)
  // Each session's last reply and its ETag, while this page is mounted: coming back to a session
  // shows what it last read at once (folded with what came since) and asks with its ETag.
  const [known, setKnown] = useState<ReadonlyMap<string, Held>>(() => new Map())
  const etags = useRef(new Map<string, { etag: string; held: Held }>())

  useEffect(() => {
    const last = etags.current.get(sessionId)
    const f: Flight = {
      sessionId, alive: true, active: false, inFlight: false, trailing: false, lastStart: -Infinity, timer: null,
      etag: last?.etag ?? '', held: last?.held ?? null, noRoute: false,
    }
    flight.current = f
    return () => {
      f.alive = false
      if (f.timer) clearTimeout(f.timer)
      f.timer = null
    }
  }, [sessionId])
  useEffect(() => { if (flight.current) flight.current.active = enabled }, [enabled, sessionId])

  const read = useCallback(function run(): void {
    const f = flight.current
    if (!f || !f.alive || !f.active || f.noRoute) return
    if (f.inFlight) {
      f.trailing = true
      return
    }
    const wait = f.lastStart + LIVE_MIN_GAP_MS - Date.now()
    if (wait > 0) {
      f.timer ??= setTimeout(() => { f.timer = null; run() }, wait)
      return
    }
    const show = (s: Shown) => {
      if (f.alive) setShown(previous => previous.sessionId === f.sessionId && previous.s.status === 'ready' && s.status === 'ready' && previous.s.held === s.held
        ? previous : { sessionId: f.sessionId, s })
    }
    f.inFlight = true
    f.lastStart = Date.now()
    apiFetch(`/api/sessions/${encodeURIComponent(f.sessionId)}/live`, { headers: f.etag ? { 'If-None-Match': f.etag } : {} })
      .then(async res => {
        if (!f.alive) return
        if (res.status === 304) {
          if (f.held) show({ status: 'ready', held: f.held })
          return
        }
        if (!(res.headers.get('Content-Type') ?? '').includes('application/json') && isNoRoute(res.status)) {
          f.noRoute = true
          f.trailing = false
          show({ status: 'no-route' })
          return
        }
        // Only a 200 is a state to keep and an ETag to send back: an error reply carries an ETag too.
        if (res.status !== 200) {
          show({ status: 'error', held: f.held, message: `HTTP ${res.status}` })
          return
        }
        const reply = (await res.json()) as LiveReply
        if (!f.alive) return
        if (typeof reply?.through_seq !== 'number') {
          show({ status: 'error', held: f.held, message: 'not a live state' })
          return
        }
        const at = Date.parse(reply.generated_at)
        f.etag = res.headers.get('ETag') ?? ''
        const held: Held = { reply, offset: Number.isFinite(at) ? Date.now() - at : 0 }
        f.held = held
        etags.current.delete(f.sessionId)
        etags.current.set(f.sessionId, { etag: f.etag, held })
        if (etags.current.size > KEEP) etags.current.delete(etags.current.keys().next().value!)
        show({ status: 'ready', held })
        setKnown(k => {
          const next = new Map(k)
          next.delete(f.sessionId)
          next.set(f.sessionId, held)
          if (next.size > KEEP) next.delete(next.keys().next().value!)
          return next
        })
      })
      .catch(() => show({ status: 'error', held: f.held }))
      .finally(() => {
        f.inFlight = false
        if (f.trailing && f.alive) {
          f.trailing = false
          run()
        }
      })
  }, [])

  // Numbers and flags, so an event that is not a trigger does not render the page again.
  const triggers = useTranscriptStore(s => (enabled ? countTriggers(s.sessions[sessionId]?.events ?? NO_EVENTS) : 0))
  const replayDone = useTranscriptStore(s => !!s.sessions[sessionId]?.replayDone)
  useEffect(() => { if (enabled) read() }, [enabled, sessionId, triggers, replayDone, refresh, read])
  useEffect(() => enabled ? subscribeChildren(sessionId, read) : undefined, [enabled, sessionId, read])
  useEffect(() => {
    if (!enabled) return
    const onVisible = () => { if (document.visibilityState === 'visible') read() }
    const onFocus = () => read()
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [enabled, read])

  const before = known.get(sessionId)
  const cur: Shown = shown.sessionId === sessionId ? shown.s : before ? { status: 'ready', held: before } : NOT_READ
  const held = cur.status === 'ready' || cur.status === 'error' ? cur.held : null
  const through = held?.reply.through_seq ?? Infinity
  // The events after the reply that foldLive reads, compared item by item: a tool call or a message
  // of the agent does not render the page again.
  const tail = useTranscriptStore(useShallow(s => (held ? foldable(s.sessions[sessionId]?.events ?? NO_EVENTS, through) : NO_EVENTS)))
  const minute = useMinute()
  const selectView = useMemo(liveViewSelector, [])
  const view = useMemo(() => (held ? selectView(sessionId, foldLive(held.reply, tail, minute - held.offset)) : null), [selectView, sessionId, held, tail, minute])

  if (cur.status === 'ready' && view) return { status: 'ready', view, reply: cur.held.reply, clockOffset: cur.held.offset }
  if (cur.status === 'error') return { status: 'error', view, reply: held?.reply ?? null, clockOffset: held?.offset ?? 0, message: cur.message }
  return cur.status === 'no-route' ? { status: 'no-route' } : LOADING
}
