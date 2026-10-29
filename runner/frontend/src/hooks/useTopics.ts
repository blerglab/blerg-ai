// useTopics: the session's History (GET /api/sessions/{id}/topics), read only while the History tab is
// on screen with the panel open. It reads when the tab comes on screen, when the page comes back
// (focus, visibility), when the Files list changes (a deletion leaves no event) and when the
// transcript store takes an event that changes the reel (TRIGGER_KINDS; a subagent_update is not one:
// its usage shows at the next done). Every read sends If-None-Match; one runs at a time, reads start
// at least MIN_GAP_MS apart, and a trigger that comes too early or during a read gets one trailing
// read. All of it (ETag, last list, timer, the read in flight) is one session's: a new sessionId
// starts over and a reply for the previous one is dropped.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { onMessage } from '../ws'
import type { MessageAnswered } from '../types'
import { isTrigger, type TopicsReply } from '../lib/topics'
import { sessionView } from '../lib/sessionView'

export const MIN_GAP_MS = 5000

export type TopicsView =
  | { status: 'loading' }
  | { status: 'ready'; reply: TopicsReply }
  /** message: "HTTP <status>" for a reply that was not a 200; absent when the request itself failed. */
  | { status: 'error'; reply: TopicsReply | null; message?: string }
  | { status: 'no-route' }

const NO_EVENTS: AgentEvent[] = []
const LOADING: TopicsView = { status: 'loading' }

export function countTriggers(events: readonly AgentEvent[]): number {
  let n = 0
  for (const ev of events) if (isTrigger(ev)) n++
  return n
}

/** The read state of one session; replaced (and the old one killed) when the sessionId changes. */
interface Flight {
  sessionId: string
  alive: boolean
  active: boolean
  inFlight: boolean
  trailing: boolean
  lastStart: number
  timer: ReturnType<typeof setTimeout> | null
  etag: string
  reply: TopicsReply | null
  /** Aborts the read still out when the flight ends (the session changed or the panel went). */
  aborter: AbortController
}

/** An older server: its app page (a 200 that is not JSON), or an API-only ingress's plain 404/405.
 *  Any other reply that is not JSON (a proxy's 502 during a restart, a 401) is an ordinary error. */
const isNoRoute = (status: number) => status === 200 || status === 404 || status === 405

/** The last History read for a session, kept in the explorer and panel cache. */
interface CachedTopics { etag: string; reply: TopicsReply }

export function useTopics(sessionId: string, active: boolean, filesKey: string): TopicsView {
  // The last list read for this session (the explorer and panel cache) shows at once, and is asked
  // for again with its ETag when the tab is on screen.
  const [shown, setShown] = useState<{ sessionId: string; view: TopicsView }>(() => {
    const cached = sessionView<CachedTopics>(sessionId, 'topics').cached
    return { sessionId, view: cached ? { status: 'ready', reply: cached.reply } : LOADING }
  })
  const flight = useRef<Flight | null>(null)

  // Declared first: the effects below find this session's flight in place.
  useEffect(() => {
    const cached = sessionView<CachedTopics>(sessionId, 'topics').cached
    const f: Flight = { sessionId, alive: true, active: false, inFlight: false, trailing: false, lastStart: -Infinity, timer: null, etag: cached?.etag ?? '', reply: cached?.reply ?? null, aborter: new AbortController() }
    flight.current = f
    return () => {
      f.alive = false
      f.aborter.abort()
      if (f.timer) clearTimeout(f.timer)
      f.timer = null
    }
  }, [sessionId])
  useEffect(() => { if (flight.current) flight.current.active = active }, [active, sessionId])

  const read = useCallback(function run(): void {
    const f = flight.current
    if (!f || !f.alive || !f.active) return
    if (f.inFlight) {
      f.trailing = true
      return
    }
    const wait = f.lastStart + MIN_GAP_MS - Date.now()
    if (wait > 0) {
      f.timer ??= setTimeout(() => { f.timer = null; run() }, wait)
      return
    }
    const show = (view: TopicsView) => { if (f.alive) setShown({ sessionId: f.sessionId, view }) }
    f.inFlight = true
    f.lastStart = Date.now()
    const cache = sessionView<CachedTopics>(f.sessionId, 'topics')
    apiFetch(`/api/sessions/${encodeURIComponent(f.sessionId)}/topics`, { headers: f.etag ? { 'If-None-Match': f.etag } : {}, signal: f.aborter.signal })
      .then(async res => {
        if (!f.alive) return
        if (res.status === 304) {
          if (f.reply) show({ status: 'ready', reply: f.reply })
          return
        }
        if (!(res.headers.get('Content-Type') ?? '').includes('application/json') && isNoRoute(res.status)) {
          show({ status: 'no-route' })
          return
        }
        // Only a 200 is a list to keep and an ETag to send back: an error reply carries an ETag too.
        if (res.status !== 200) {
          show({ status: 'error', reply: f.reply, message: `HTTP ${res.status}` })
          return
        }
        const reply = (await res.json()) as TopicsReply
        if (!f.alive) return
        f.etag = res.headers.get('ETag') ?? ''
        f.reply = reply
        cache.keep({ etag: f.etag, reply })
        show({ status: 'ready', reply })
      })
      .catch(() => show({ status: 'error', reply: f.reply }))
      .finally(() => {
        f.inFlight = false
        if (f.trailing && f.alive) {
          f.trailing = false
          run()
        }
      })
  }, [])

  // Closing an ask without an answer changes the messages table, not the transcript.
  // It still changes History, so follow the same socket notification the chat receives.
  useEffect(() => {
    if (!active) return
    return onMessage<MessageAnswered>('message_answered', msg => {
      if (msg.message.session_id === sessionId && msg.message.kind === 'ask') read()
    })
  }, [active, sessionId, read])

  // A number, so an event that is not a trigger does not render this hook's component again.
  const triggers = useTranscriptStore(s => (active ? countTriggers(s.sessions[sessionId]?.events ?? NO_EVENTS) : 0))
  // On screen, and whenever the session, the Files list or the reel's events change while it is.
  useEffect(() => { if (active) read() }, [active, sessionId, filesKey, triggers, read])
  useEffect(() => {
    if (!active) return
    const onVisible = () => { if (document.visibilityState === 'visible') read() }
    const onFocus = () => read()
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [active, read])
  return shown.sessionId === sessionId ? shown.view : LOADING
}
