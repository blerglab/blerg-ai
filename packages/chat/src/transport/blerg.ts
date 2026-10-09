// BlergTransport: the runner's own websocket and routes, for the runner's web app. It sends the
// very messages AgentChatView sent before the package existed (subscribe_agent_events with
// after_seq / tail / before_seq, agent_user_message, set_session_model, unsubscribe_session) and
// reads the same ones back (agent_event, agent_events_replay_done, session_state_changed,
// initial_state). The host supplies getToken (the runner passes its ensureFreshToken) and may
// inject its shared socket (the runner's ws.ts is a module singleton other panels use); without
// one the adapter opens its own to baseUrl, with the bearer subprotocol ws.ts uses.
//
// It works from the runner's origin with a core-issued browser token, the only place such a token
// exists; an outside app never uses it (that app's chat goes through proxy.ts).
import type {
  AgentEvent, AgentEventsReplayDone, AgentUserMessage, AssistantTextPayload, InterruptSession, SessionInfo, SessionStatus,
  SetSessionModel, SubscribeAgentEvents, UnsubscribeSession,
} from '../types'
import { createFiles } from './files'
import { createBrowserSocket } from './socket'
import type { OlderPage, SessionMeta, SubscribeHandlers, SubscribeOptions, Transport } from './types'

/** The messages a chat sends the runner over its socket (a subset of the runner's BrowserMessage). */
export type ChatBrowserMessage = SubscribeAgentEvents | UnsubscribeSession | AgentUserMessage | SetSessionModel | InterruptSession

/** What the adapter needs of a socket: the runner's ws.ts module as it is (send, onMessage,
 *  onOpen), plus two optional members a host can add so the composer locks the moment the link
 *  drops rather than at the next refused send. */
export interface BlergSocket {
  /** false when the socket is not open: the message was dropped. */
  send(msg: ChatBrowserMessage): boolean
  onMessage(type: string, handler: (msg: never) => void): () => void
  /** Fires on every (re)connection — at once when already open. */
  onOpen(handler: () => void): () => void
  onClose?(handler: () => void): () => void
  connected?(): boolean
}

export interface BlergTransportOptions {
  /** The runner's origin; '' (the default) is the page's own. */
  baseUrl?: string
  /** A fresh browser token, or null when there is none. Asked per request. */
  getToken: () => Promise<string | null>
  socket?: BlergSocket
  /** A route answered 401: the token is gone for good and the host should sign the person in. */
  onUnauthorized?: () => void
  fetch?: typeof fetch
}

/** The newest events shown at once on opening a transcript; the rest follow in pages of the same
 *  size (the runner's INITIAL_TAIL / OLDER_PAGE). */
export const INITIAL_TAIL = 200

/** How long a backward page may take before loadOlder gives up. */
const OLDER_TIMEOUT_MS = 30_000

interface PendingOlder {
  beforeSeq: number
  events: AgentEvent[]
}

const enc = encodeURIComponent

function toMeta(s: SessionInfo): SessionMeta {
  return {
    status: s.status,
    engine: s.engine,
    runtime: s.runtime,
    model: s.model,
    effort: s.effort,
    started_at: s.started_at,
    ended_at: s.ended_at,
    end_reason: s.end_reason,
    ended_by: s.ended_by,
    error_reason: s.error_reason,
    message: s.message,
  }
}

export function createBlergTransport(opts: BlergTransportOptions): Transport {
  const baseUrl = (opts.baseUrl ?? '').replace(/\/$/, '')
  const fetchImpl = opts.fetch ?? globalThis.fetch.bind(globalThis)
  const socket: BlergSocket = opts.socket ?? createBrowserSocket({ baseUrl, getToken: opts.getToken })
  // Whether the socket is open, as far as its open/close callbacks have told us — for a socket
  // that cannot be asked.
  let openSeen = false
  // Backward pages in flight, by session: their events are collected for loadOlder rather than
  // handed to the subscription (the socket shows no difference between a replayed and a live
  // agent_event).
  const pendingOlder = new Map<string, PendingOlder>()

  socket.onOpen(() => { openSeen = true })
  socket.onClose?.(() => { openSeen = false })

  const connected = () => socket.connected?.() ?? openSeen

  const bearer = async (): Promise<Record<string, string>> => {
    const token = await opts.getToken()
    // Only with a token: "Bearer null" is a malformed token, not a missing one.
    return token ? { Authorization: `Bearer ${token}` } : {}
  }
  const check = (res: Response) => {
    if (res.status === 401) {
      opts.onUnauthorized?.()
      throw new Error('Not signed in.')
    }
  }
  const request = async (path: string, init?: RequestInit): Promise<Response> => {
    const res = await fetchImpl(`${baseUrl}${path}`, { ...init, headers: { ...(await bearer()), ...(init?.headers as Record<string, string> | undefined) } })
    check(res)
    return res
  }

  const subscribe = (sessionId: string, o: SubscribeOptions, h: SubscribeHandlers): (() => void) => {
    let lastSeq = o.afterSeq ?? 0
    const offs: Array<() => void> = []

    offs.push(socket.onMessage('agent_event', (msg: never) => {
      const ev = msg as AgentEvent
      if (ev.session_id !== sessionId) return
      if (ev.transient && ev.kind === 'assistant_text') {
        // A typing delta: the text so far this turn grows by it; never persisted.
        h.onStreaming((ev.payload as AssistantTextPayload).text)
        return
      }
      const older = pendingOlder.get(sessionId)
      if (older && ev.seq != null && ev.seq < older.beforeSeq) return // loadOlder's page, collected there
      if (ev.seq != null && ev.seq > lastSeq) lastSeq = ev.seq
      h.onEvent(ev)
    }))

    offs.push(socket.onMessage('agent_events_replay_done', (msg: never) => {
      const done = msg as AgentEventsReplayDone
      if (done.session_id !== sessionId) return
      // The answer to a loadOlder in flight is its own to resolve.
      if (done.older && pendingOlder.has(sessionId)) return
      if (done.last_seq > lastSeq) lastSeq = done.last_seq
      h.onReplayDone({
        lastSeq: done.last_seq,
        hasMore: done.has_more,
        older: done.older,
        hasOlder: done.has_older,
        firstSeq: done.first_seq,
        serverTime: done.server_time,
      })
      // The server replays a page at a time. A longer transcript needs the following pages too,
      // or everything between the first page and the live tail is silently missing.
      if (!done.older && done.has_more) {
        socket.send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: done.last_seq })
      }
    }))

    offs.push(socket.onMessage('session_state_changed', (msg: never) => {
      const m = msg as { session_id: string; status: SessionStatus }
      if (m.session_id === sessionId) h.onStatus(m.status)
    }))

    offs.push(socket.onMessage('initial_state', (msg: never) => {
      const s = (msg as { sessions: SessionInfo[] }).sessions.find(x => x.id === sessionId)
      if (s) h.onStatus(s.status)
    }))

    // Subscribe on every (re)connection, resuming from lastSeq. First load: the newest events
    // now, the rest behind them (loadOlder, driven by the hook).
    offs.push(socket.onOpen(() => {
      h.onConnection(true)
      if (lastSeq === 0 && o.tail) {
        socket.send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: 0, tail: INITIAL_TAIL })
        return
      }
      socket.send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: lastSeq })
    }))

    if (socket.onClose) offs.push(socket.onClose(() => h.onConnection(false)))

    return () => {
      for (const off of offs) off()
      socket.send({ type: 'unsubscribe_session', session_id: sessionId })
    }
  }

  const loadOlder = (sessionId: string, beforeSeq: number, limit: number): Promise<OlderPage> =>
    new Promise<OlderPage>((resolve, reject) => {
      const pending: PendingOlder = { beforeSeq, events: [] }
      const offs: Array<() => void> = []
      let timer: ReturnType<typeof setTimeout> | null = null
      const finish = () => {
        for (const off of offs) off()
        if (timer) clearTimeout(timer)
        if (pendingOlder.get(sessionId) === pending) pendingOlder.delete(sessionId)
      }
      offs.push(socket.onMessage('agent_event', (msg: never) => {
        const ev = msg as AgentEvent
        if (ev.session_id !== sessionId || ev.transient || ev.seq == null || ev.seq >= beforeSeq) return
        pending.events.push(ev)
      }))
      offs.push(socket.onMessage('agent_events_replay_done', (msg: never) => {
        const done = msg as AgentEventsReplayDone
        if (done.session_id !== sessionId || !done.older) return
        finish()
        resolve({
          events: pending.events,
          hasOlder: done.has_older === true,
          firstSeq: done.first_seq ?? pending.events[0]?.seq ?? 0,
          serverTime: done.server_time,
        })
      }))
      pendingOlder.set(sessionId, pending)
      if (!socket.send({ type: 'subscribe_agent_events', session_id: sessionId, before_seq: beforeSeq, limit })) {
        finish()
        reject(new Error('Not connected.'))
        return
      }
      timer = setTimeout(() => {
        finish()
        reject(new Error('The older page did not arrive.'))
      }, OLDER_TIMEOUT_MS)
    })

  return {
    subscribe,
    loadOlder,

    // The browser API lists sessions; there is no route for one, so the list is read and the
    // session picked out of it.
    async session(sessionId) {
      const r = await request('/api/sessions')
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const list = (await r.json()) as SessionInfo[]
      const s = Array.isArray(list) ? list.find(x => x.id === sessionId) : undefined
      if (!s) throw new Error('Session not found.')
      return toMeta(s)
    },

    async send(sessionId, text) {
      return { queued: socket.send({ type: 'agent_user_message', session_id: sessionId, text }) }
    },

    // The runner's web app ends a session through DELETE /api/sessions/{id}.
    async stop(sessionId) {
      const r = await request(`/api/sessions/${enc(sessionId)}`, { method: 'DELETE' })
      if (!r.ok && r.status !== 404) throw new Error(`HTTP ${r.status}`)
    },

    // The toolbar's Stop while a turn runs: the turn is cut short, the session goes on.
    async interrupt(sessionId) {
      if (!socket.send({ type: 'interrupt_session', session_id: sessionId })) throw new Error('Not connected.')
    },

    async setModel(sessionId, model, effort) {
      socket.send({
        type: 'set_session_model',
        session_id: sessionId,
        ...(model !== undefined ? { model } : {}),
        ...(effort !== undefined ? { effort } : {}),
      })
    },

    connected,

    files: createFiles({
      base: (sessionId) => `${baseUrl}/api/sessions/${enc(sessionId)}`,
      fetch: fetchImpl,
      headers: bearer,
      check,
    }),
  }
}
