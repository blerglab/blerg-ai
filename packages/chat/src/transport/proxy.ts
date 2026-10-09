// ProxyTransport: an app's own backend over the proxy contract, which mirrors the runner's v1
// operations one to one (docs/design/chat-package.md, "The transport contract"):
//
//   GET    {base}/sessions/{id}                               SessionMeta
//   GET    {base}/sessions/{id}/events/live?after_seq=&tail=  SSE (B's frames, verbatim)
//   GET    {base}/sessions/{id}/events?before_seq=&limit=     older pages
//   POST   {base}/sessions/{id}/messages                      {text}
//   POST   {base}/sessions/{id}/stop
//   POST   {base}/sessions/{id}/interrupt                     (optional: 404/501)
//   POST   {base}/sessions/{id}/model                         {model, effort}   (optional: 501)
//   GET    {base}/sessions/{id}/artifacts, /{aid}/raw, /{aid}/download, DELETE /{aid}
//   POST   {base}/sessions/{id}/uploads                       raw bytes, X-Artifact-Name
//
// The app authenticates the person its own way (its cookie travels with every request) and
// forwards to the runner with its agent token; nothing here knows a token.
//
// Shapes assumed of the app's answers, where the runner's contract leaves room:
// - A live-stream `agent_event` frame is the runner's AgentEvent JSON. A bare runner event
//   ({seq, ts, kind, payload}, as the polling route pages them) is accepted too: type, session_id
//   and a client_event_id derived from seq are filled in.
// - The older page is `{events, has_older?, first_seq?, server_time?}`; without `has_older` the
//   polling route's `has_more` stands in, without `first_seq` the first event's seq does, and
//   without `server_time` the response's Date header.
// - GET /sessions/{id} is a SessionMeta; a runner status body ({lifecycle, runtime, ...}) is
//   mapped instead when it has no `status`.
// - The `end` frame's data may carry `status` (a SessionStatus) or the runner's result body
//   ({lifecycle}); `error` is reported as 'error', anything else as 'ended'.
import type { AgentEvent, AgentEventsReplayDone, AssistantTextPayload, EndedBy, SessionStatus } from '../types'
import { createFiles } from './files'
import { openSseStream, type SseStream } from './sse'
import type { OlderPage, SessionMeta, SubscribeHandlers, SubscribeOptions, Transport } from './types'

export interface ProxyTransportOptions {
  /** The proxy's prefix on the app's origin, e.g. '/blerg' (no trailing slash needed). */
  baseUrl: string
  fetch?: typeof fetch
  /** How requests carry the app's own session: 'same-origin' (the default) or 'include' for a
   *  proxy on another origin. */
  credentials?: RequestCredentials
  /** The live stream's reconnection backoff (ms). */
  backoff?: { initial?: number; max?: number }
  /** Headers every request carries, for an app whose own sign-in is a bearer token rather than a
   *  cookie. Asked for per request (and per reconnect of the live stream): a token is renewed
   *  silently and must be read fresh. */
  headers?: () => Promise<Record<string, string>>
  /** A route answered 401: the app's sign-in is gone and the host should renew it. */
  onUnauthorized?: () => void
}

/** The newest events asked for first on opening a transcript (the runner's INITIAL_TAIL). */
const INITIAL_TAIL = 200

const STATUSES: ReadonlySet<string> = new Set<SessionStatus>(['starting', 'running', 'idle', 'waiting', 'stopped', 'error', 'disconnected', 'ended'])

const enc = encodeURIComponent

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}

/** A frame's or a page's event as the transcript wants it: every field present. */
function normalizeEvent(raw: unknown, sessionId: string): AgentEvent | null {
  if (!raw || typeof raw !== 'object') return null
  const ev = raw as Partial<AgentEvent>
  if (typeof ev.kind !== 'string') return null
  const seq = typeof ev.seq === 'number' ? ev.seq : undefined
  return {
    ...ev,
    type: 'agent_event',
    session_id: typeof ev.session_id === 'string' ? ev.session_id : sessionId,
    client_event_id: typeof ev.client_event_id === 'string' ? ev.client_event_id : seq !== undefined ? `seq:${seq}` : `tmp:${Math.random().toString(36).slice(2)}`,
    ts: typeof ev.ts === 'string' ? ev.ts : '',
    kind: ev.kind,
    payload: ev.payload,
  }
}

/** The runner's lifecycle (starting, running, disconnected, ended, error) as a SessionStatus. */
function statusFromLifecycle(lifecycle: unknown): SessionStatus {
  switch (lifecycle) {
    case 'starting': return 'starting'
    case 'disconnected': return 'disconnected'
    case 'ended': return 'ended'
    case 'error': return 'error'
    default: return 'running'
  }
}

function toMeta(body: Record<string, unknown>): SessionMeta {
  const str = (k: string) => (typeof body[k] === 'string' ? (body[k] as string) : undefined)
  const status = typeof body.status === 'string' && STATUSES.has(body.status) ? (body.status as SessionStatus) : statusFromLifecycle(body.lifecycle)
  const endedBy = body.ended_by
  return {
    status,
    engine: str('engine'),
    runtime: str('runtime'),
    model: str('model'),
    effort: str('effort'),
    started_at: str('started_at') ?? '',
    ended_at: body.ended_at === null ? null : str('ended_at'),
    // The runner's status body carries these too ('' when there is nothing to say).
    end_reason: str('end_reason') || undefined,
    ended_by: endedBy && typeof endedBy === 'object' && typeof (endedBy as EndedBy).kind === 'string' ? (endedBy as EndedBy) : endedBy === null ? null : undefined,
    error_reason: str('error_reason') || undefined,
    message: body.message === null ? null : str('message'),
  }
}

export function createProxyTransport(opts: ProxyTransportOptions): Transport {
  const base = opts.baseUrl.replace(/\/$/, '')
  // The global fetch is looked up per call, not captured: a host may wrap it after this runs.
  const rawFetch: typeof fetch = (input, init) => (opts.fetch ?? globalThis.fetch)(input, init)
  const credentials = opts.credentials ?? 'same-origin'
  // Every request (the live stream's attempts among them) goes through here: the host's headers
  // are read fresh each time, and a 401 is reported once per response.
  const fetchImpl: typeof fetch = async (input, init) => {
    const extra = opts.headers ? await opts.headers() : undefined
    const res = await rawFetch(input, extra ? { ...init, headers: { ...extra, ...(init?.headers as Record<string, string> | undefined) } } : init)
    if (res.status === 401) opts.onUnauthorized?.()
    return res
  }
  const session = (id: string) => `${base}/sessions/${enc(id)}`
  // The live streams open right now, one per subscription.
  const streams = new Set<SseStream>()

  const request = (url: string, init?: RequestInit) => fetchImpl(url, { credentials, ...init })
  const post = (url: string, body?: unknown) =>
    request(url, body === undefined
      ? { method: 'POST' }
      : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })

  const subscribe = (sessionId: string, o: SubscribeOptions, h: SubscribeHandlers): (() => void) => {
    let up = false
    const setUp = (v: boolean) => {
      if (up === v) return
      up = v
      h.onConnection(v)
    }
    const stream = openSseStream({
      fetch: fetchImpl,
      afterSeq: o.afterSeq ?? 0,
      backoff: opts.backoff,
      // The tail is asked for once, on the first attempt from nothing; a reconnect resumes.
      url: (after, first) => `${session(sessionId)}/events/live?after_seq=${after}${first && o.tail && after === 0 ? '&tail=1' : ''}&limit=${INITIAL_TAIL}`,
      onOpen: () => setUp(true),
      onDrop: () => setUp(false),
      onEnd: (e) => {
        streams.delete(stream)
        // The app said no (401/403/404): the link is down for good, whether or not it was ever up.
        if (e.reason === 'refused') {
          up = false
          h.onConnection(false)
        }
      },
      onFrame: (frame) => {
        const data = parseJson(frame.data)
        switch (frame.event) {
          case 'agent_event': {
            const ev = normalizeEvent(data, sessionId)
            if (!ev || ev.session_id !== sessionId) return
            if (ev.transient && ev.kind === 'assistant_text') {
              h.onStreaming((ev.payload as AssistantTextPayload).text)
              return
            }
            h.onEvent(ev)
            return
          }
          case 'replay_done': {
            const done = data as Partial<AgentEventsReplayDone> | undefined
            if (!done || typeof done.last_seq !== 'number') return
            h.onReplayDone({
              lastSeq: done.last_seq,
              hasMore: done.has_more === true,
              older: done.older,
              hasOlder: done.has_older,
              firstSeq: done.first_seq,
              serverTime: done.server_time,
            })
            return
          }
          case 'status': {
            const s = (data as { status?: unknown } | undefined)?.status
            if (typeof s === 'string' && STATUSES.has(s)) h.onStatus(s as SessionStatus)
            return
          }
          case 'end': {
            // The session is over: no reconnection, and its final state is the last word.
            const d = (data ?? {}) as { status?: unknown; lifecycle?: unknown }
            const final: SessionStatus = typeof d.status === 'string' && STATUSES.has(d.status)
              ? (d.status as SessionStatus)
              : d.lifecycle === 'error' ? 'error' : 'ended'
            stream.close()
            h.onStatus(final)
            return
          }
          default:
            return // a frame this version does not know
        }
      },
      // The app's cookie is what authenticates the stream.
      headers: {},
    })
    streams.add(stream)
    return () => {
      streams.delete(stream)
      stream.close()
    }
  }

  return {
    subscribe,

    async loadOlder(sessionId, beforeSeq, limit): Promise<OlderPage> {
      const r = await request(`${session(sessionId)}/events?before_seq=${beforeSeq}&limit=${limit}`)
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const body = (await r.json()) as { events?: unknown; has_older?: unknown; has_more?: unknown; first_seq?: unknown; server_time?: unknown }
      const events = (Array.isArray(body.events) ? body.events : [])
        .map(e => normalizeEvent(e, sessionId))
        .filter((e): e is AgentEvent => e !== null)
      const hasOlder = typeof body.has_older === 'boolean' ? body.has_older : body.has_more === true
      const firstSeq = typeof body.first_seq === 'number' ? body.first_seq : events.find(e => e.seq != null)?.seq ?? 0
      const serverTime = typeof body.server_time === 'string' ? body.server_time : r.headers.get('Date') ?? undefined
      return { events, hasOlder, firstSeq, serverTime }
    },

    async session(sessionId) {
      const r = await request(session(sessionId))
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const body: unknown = await r.json()
      if (!body || typeof body !== 'object') throw new Error('Unexpected reply from the server.')
      return toMeta(body as Record<string, unknown>)
    },

    async send(sessionId, text) {
      const r = await post(`${session(sessionId)}/messages`, { text })
      return { queued: r.ok }
    },

    async stop(sessionId) {
      const r = await post(`${session(sessionId)}/stop`)
      if (!r.ok && r.status !== 404) throw new Error(`HTTP ${r.status}`)
    },

    // Cuts the running turn short (the runner's /interrupt, mirrored). An app without the route
    // (404 or 501) is not an error: the turn simply runs on.
    async interrupt(sessionId) {
      const r = await post(`${session(sessionId)}/interrupt`)
      if (!r.ok && r.status !== 404 && r.status !== 501) throw new Error(`HTTP ${r.status}`)
    },

    // Optional on the app's side: 501 means it offers no model switch, which is not an error.
    async setModel(sessionId, model, effort) {
      const r = await post(`${session(sessionId)}/model`, {
        ...(model !== undefined ? { model } : {}),
        ...(effort !== undefined ? { effort } : {}),
      })
      if (!r.ok && r.status !== 501) throw new Error(`HTTP ${r.status}`)
    },

    /** Whether a live stream is open right now (false before subscribe, and between attempts). */
    connected: () => [...streams].some(s => s.connected()),

    // The upload goes out by XMLHttpRequest (for its progress), which fetchImpl cannot wrap: it
    // gets the host's headers and the 401 check directly. Everything else is already wrapped.
    files: createFiles({
      base: session,
      fetch: fetchImpl,
      credentials,
      uploadHeaders: opts.headers,
      checkUpload: res => { if (res.status === 401) opts.onUnauthorized?.() },
    }),
  }
}
