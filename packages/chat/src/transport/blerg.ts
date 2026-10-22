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
  AgentEvent, AgentEventsGap, AgentEventsReplayDone, DelegationDecidedPayload, AgentUserMessage, AssistantTextPayload, InterruptSession, SessionInfo,
  SessionStatus, SetSessionModel, SubscribeAgentEvents, UnsubscribeSession,
} from '../types'
import { readProposalView } from '../model/proposals'
import { readReviewRequest } from '../model/reviewRequests'
import { createFiles } from './files'
import { createBrowserSocket } from './socket'
import type { OlderPage, SessionMeta, SubscribeHandlers, SubscribeOptions, Transport } from './types'

/** A message the session sent the person (the runner's MessageInfo), as initial_state and
 *  message_answered carry it. */
interface MessageInfo { id: string; session_id: string; kind: string; status: string; answer?: string | null; dismissed?: boolean; dismissed_by?: string }

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
  /** The account signed in (its token's subject), so a question someone else dismissed does not
   *  read "dismissed by you"; '' or absent: not known. */
  viewerId?: () => string
}

/** The newest events shown at once on opening a transcript; the rest follow in pages of the same
 *  size (the runner's INITIAL_TAIL / OLDER_PAGE). */
export const INITIAL_TAIL = 200

/** How many full forward pages (the server's 200 events each, so about 1000 events) a subscriber
 *  that can reset is replayed before the transport stops and loads the transcript from its end
 *  instead. A revisit a little behind keeps the transcript it holds and catches up in a few
 *  batches; one far behind would replay its whole gap forward, page after page, before showing
 *  anything new, so it takes the path a reload takes: the newest page at once, the older part
 *  paged in behind it (the events held are dropped and fetched again). */
export const MAX_FORWARD_PAGES = 5

/** How long a hole in the live seqs may stay open before the transport asks the server for it.
 *  Seqs are contiguous per session, but live delivery is not strictly in seq order (the daemon's,
 *  the artifacts' and the start stages' events are fanned out from different places), so an
 *  event a little ahead of its predecessor is waited for briefly rather than fetched at once. */
export const GAP_WAIT_MS = 1000

/** How long a backward page may take before loadOlder gives up. */
const OLDER_TIMEOUT_MS = 30_000

interface PendingOlder {
  beforeSeq: number
  events: AgentEvent[]
}

const enc = encodeURIComponent

/** The server's {error} message, else the status. */
async function errorText(r: Response): Promise<string> {
  try {
    const b = (await r.json()) as { error?: unknown }
    if (typeof b?.error === 'string' && b.error) return b.error
  } catch {
    // not JSON
  }
  return `HTTP ${r.status}`
}

function toMeta(s: SessionInfo): SessionMeta {
  return {
    status: s.status,
    engine: s.engine,
    steering_supported: typeof s.steering_supported === 'boolean' ? s.steering_supported : undefined,
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
  // Who dismissed a question, as this viewer reads it: undefined when either side is not known.
  const dismissedBy = (m: MessageInfo): 'you' | 'other' | undefined => {
    const viewer = opts.viewerId?.() ?? ''
    if (!m.dismissed || !m.dismissed_by || !viewer) return undefined
    return m.dismissed_by === viewer ? 'you' : 'other'
  }
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
    // The events of the replay page in flight, held for one onEvents at its replay_done (a
    // subscriber without onEvents gets them one by one as they come). null: no page in flight,
    // events are live and go to onEvent as they arrive.
    let page: AgentEvent[] | null = null
    // Full forward pages in a row since the last subscribe, for the MAX_FORWARD_PAGES bound.
    let fullPages = 0
    // A tail asked for and not yet answered: a reconnect asks for it again rather than replaying
    // forward from a live event that came in meanwhile.
    let tailPending = false
    // Where an unfinished forward replay has reached (the request's after_seq, then each page's
    // last_seq while has_more); null when none is under way. A live event can come in mid-replay
    // and move lastSeq past the gap still to be replayed, so a reconnect resumes from here.
    let cursor: number | null = null
    // Gap tracking: every persisted seq up to contig has been handed over; ahead holds the seqs
    // handed over above it. A seq left out (a live event the server dropped for this browser, or
    // one still on its way) keeps ahead non-empty: after GAP_WAIT_MS, or at once when the server
    // says it dropped events (agent_events_gap), the transport replays forward from contig, and
    // the transcript dedupes what it already holds.
    let contig = o.afterSeq ?? 0
    const ahead = new Set<number>()
    let gapTimer: ReturnType<typeof setTimeout> | null = null
    // A refill asked for while a replay was under way: sent once that replay is over.
    let refillAfter: number | null = null

    const batching = h.onEvents != null
    const startPage = () => { if (batching && page == null) page = [] }
    const flush = () => {
      const held = page
      page = null
      if (held && held.length > 0) h.onEvents!(held)
    }
    const askTail = () => {
      tailPending = true
      cursor = null
      startPage()
      socket.send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: 0, tail: INITIAL_TAIL })
    }
    const askForward = (afterSeq: number) => {
      cursor = afterSeq
      startPage()
      socket.send({ type: 'subscribe_agent_events', session_id: sessionId, after_seq: afterSeq })
    }
    const replaying = () => cursor != null || tailPending
    const absorb = () => {
      while (ahead.delete(contig + 1)) contig++
      for (const seq of ahead) if (seq <= contig) ahead.delete(seq)
    }
    const noteSeq = (seq: number) => {
      if (seq <= contig) return
      if (seq === contig + 1) {
        contig = seq
        absorb()
      } else {
        ahead.add(seq)
      }
    }
    const clearGapTimer = () => {
      if (gapTimer) clearTimeout(gapTimer)
      gapTimer = null
    }
    // Replays forward from afterSeq to fill a hole; while a replay is under way, once it is over.
    const refill = (afterSeq: number) => {
      clearGapTimer()
      if (replaying()) {
        refillAfter = refillAfter == null ? afterSeq : Math.min(refillAfter, afterSeq)
        return
      }
      // Not connected: the next connection resumes from contig anyway.
      if (!connected()) return
      askForward(afterSeq)
    }
    const armGap = () => {
      if (gapTimer || ahead.size === 0 || replaying()) return
      gapTimer = setTimeout(() => {
        gapTimer = null
        if (ahead.size > 0) refill(contig)
      }, GAP_WAIT_MS)
    }

    offs.push(socket.onMessage('agent_event', (msg: never) => {
      const ev = msg as AgentEvent
      if (ev.session_id !== sessionId) return
      if (ev.transient && ev.kind === 'assistant_text') {
        // A typing delta: the text so far this turn grows by it; never persisted. While a page is
        // held it waits in it, behind the events before it: a closing event there must not
        // clear text that came after it.
        if (page) page.push(ev)
        else if ((ev.payload as AssistantTextPayload).parent_call_id) h.onEvent(ev)
        else h.onStreaming((ev.payload as AssistantTextPayload).text)
        return
      }
      const older = pendingOlder.get(sessionId)
      if (older && ev.seq != null && ev.seq < older.beforeSeq) return // loadOlder's page, collected there
      if (ev.seq != null && ev.seq > lastSeq) lastSeq = ev.seq
      if (ev.seq != null && ev.seq > 0) noteSeq(ev.seq)
      if (page) page.push(ev)
      else h.onEvent(ev)
      armGap()
    }))

    // The server dropped live events for this browser (its buffer was full), the first with seq
    // from_seq: fetched again at once, from wherever the transcript is complete up to.
    offs.push(socket.onMessage('agent_events_gap', (msg: never) => {
      const gap = msg as AgentEventsGap
      if (gap.session_id !== sessionId || typeof gap.from_seq !== 'number' || gap.from_seq <= 0) return
      refill(Math.min(contig, gap.from_seq - 1))
    }))

    offs.push(socket.onMessage('agent_events_replay_done', (msg: never) => {
      const done = msg as AgentEventsReplayDone
      if (done.session_id !== sessionId) return
      // The answer to a loadOlder in flight is its own to resolve.
      if (done.older && pendingOlder.has(sessionId)) return
      // How far the answered request reached back: a forward page from at or below contig, or a
      // tail, covers every seq up to its last_seq, held or not (a seq the server never stored is
      // not waited for again).
      const from = cursor
      if (done.older) tailPending = false
      const more = !done.older && done.has_more
      fullPages = more ? fullPages + 1 : 0
      if (more && h.onReset && fullPages >= MAX_FORWARD_PAGES) {
        // Too far behind to catch up forward: this page goes with the rest of what is held, and
        // the transcript loads again from its end.
        page = null
        fullPages = 0
        lastSeq = 0
        contig = 0
        ahead.clear()
        refillAfter = null
        clearGapTimer()
        h.onReset()
        askTail()
        return
      }
      flush()
      if (!done.older) cursor = more ? done.last_seq : null
      if (done.last_seq > lastSeq) lastSeq = done.last_seq
      if (done.older || (from != null && from <= contig)) {
        if (done.last_seq > contig) contig = done.last_seq
        absorb()
      }
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
      if (more) {
        askForward(done.last_seq)
        return
      }
      if (refillAfter != null) {
        const after = Math.min(refillAfter, contig)
        refillAfter = null
        askForward(after)
        return
      }
      armGap()
    }))

    offs.push(socket.onMessage('session_state_changed', (msg: never) => {
      const m = msg as { session_id: string; status: SessionStatus }
      if (m.session_id === sessionId) h.onStatus(m.status)
    }))

    offs.push(socket.onMessage('initial_state', (msg: never) => {
      const state = msg as { sessions: SessionInfo[]; messages?: MessageInfo[] | null }
      const s = state.sessions.find(x => x.id === sessionId)
      if (s) h.onStatus(s.status)
      // The session's asks that are no longer open (answered elsewhere, expired while away).
      for (const m of state.messages ?? []) {
        if (m && m.session_id === sessionId && m.kind === 'ask' && m.status !== 'open') h.onAskClosed?.(m.id, m.answer ?? '', m.dismissed === true, dismissedBy(m))
      }
    }))

    offs.push(socket.onMessage('message_answered', (msg: never) => {
      const m = (msg as { message?: MessageInfo }).message
      if (m && m.session_id === sessionId && typeof m.id === 'string') h.onAskClosed?.(m.id, m.answer ?? '', m.dismissed === true, dismissedBy(m))
    }))

    // Subscribe on every (re)connection, resuming from lastSeq (or, mid-replay, from where the
    // replay reached). First load: the newest events now, the rest behind them (loadOlder, driven
    // by the hook).
    offs.push(socket.onOpen(() => {
      // Whatever a page cut short had brought is handed over first: lastSeq already counts it.
      flush()
      fullPages = 0
      clearGapTimer()
      h.onConnection(true)
      if (tailPending || (lastSeq === 0 && o.tail)) {
        refillAfter = null
        askTail()
        return
      }
      // From below any hole still open, so the resume fills it.
      let from = cursor ?? lastSeq
      if (ahead.size > 0) from = Math.min(from, contig)
      if (refillAfter != null) from = Math.min(from, refillAfter)
      refillAfter = null
      askForward(from)
    }))

    if (socket.onClose) {
      offs.push(socket.onClose(() => {
        flush()
        clearGapTimer()
        h.onConnection(false)
      }))
    }

    return () => {
      clearGapTimer()
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
        // Only the answer to this request: every event it carries is older than beforeSeq. Another
        // backward replay_done is not, above all the tail's own: the hook asks for the first older
        // page while that one is being handled, and a socket that dispatches to a Set (the runner's
        // ws.ts) calls this handler for it too. Taken as the answer, the page resolved empty at once,
        // the real one came in as live events one at a time with nothing shown as loading, and its
        // replay_done asked for the next page, and so on through the whole history.
        if (done.first_seq != null && done.first_seq > 0 && done.first_seq >= beforeSeq) return
        finish()
        resolve({
          events: pending.events,
          hasOlder: done.has_older === true,
          firstSeq: done.first_seq ?? pending.events[0]?.seq ?? 0,
          serverTime: done.server_time,
        })
      }))
      // A drop cuts the answer off: finish now rather than at the timeout, or the entry left in
      // pendingOlder would keep the subscription from taking the next older replay_done (a tail).
      if (socket.onClose) {
        offs.push(socket.onClose(() => {
          finish()
          reject(new Error('Not connected.'))
        }))
      }
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
    publishedSessionUrl(sessionId) {
      try {
        return new URL(`${baseUrl}/sessions/${enc(sessionId)}`, globalThis.location?.href).href
      } catch {
        return null
      }
    },

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

    async send(sessionId, text, sendOpts) {
      return {
        queued: socket.send({
          type: 'agent_user_message',
          session_id: sessionId,
          text,
          ...(sendOpts?.clientId ? { client_id: sendOpts.clientId } : {}),
          ...(sendOpts?.answerTo ? { answer_message_id: sendOpts.answerTo } : {}),
        }),
      }
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

    // The person's choice on a request to start a session; the server's message rides the error.
    async decideDelegation(requestId, choice) {
      const r = await request(`/api/delegation-requests/${enc(requestId)}/decide`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ choice }),
      })
      // status rides the error so the card can tell "already decided" (409) from a retryable failure.
      if (!r.ok) throw Object.assign(new Error(await errorText(r)), { status: r.status })
      return (await r.json()) as DelegationDecidedPayload
    },

    async delegationRequest(requestId) {
      const r = await request(`/api/delegation-requests/${enc(requestId)}`)
      if (!r.ok) throw new Error(await errorText(r))
      const body = (await r.json()) as { prompt?: unknown }
      return { prompt: typeof body.prompt === 'string' ? body.prompt : '' }
    },

    // A proposal the session made: owner only (404 for anyone else).
    async proposal(proposalId) {
      const r = await request(`/api/proposals/${enc(proposalId)}`)
      if (!r.ok) throw Object.assign(new Error(await errorText(r)), { status: r.status })
      return readProposalView(await r.json())
    },

    async decideProposal(proposalId, action, opts) {
      const body = action === 'approve'
        ? { ...(opts?.expected !== undefined ? { expected_arguments_raw: opts.expected } : {}), ...(opts?.allowForSession ? { allow_for_session: true } : {}) }
        : undefined
      const r = await request(`/api/proposals/${enc(proposalId)}/${action}`, {
        method: 'POST',
        ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
      })
      if (!r.ok) {
        // A refused approval that also asked for a session permission says the permission stands.
        const raw = await r.text().catch(() => '')
        let parsed: { error?: unknown; permission_kept?: unknown } = {}
        try { parsed = JSON.parse(raw) as typeof parsed } catch { /* not JSON: the text is the message */ }
        const message = typeof parsed.error === 'string' && parsed.error ? parsed.error : raw.trim() || `HTTP ${r.status}`
        throw Object.assign(new Error(message), { status: r.status, permissionKept: parsed.permission_kept === true })
      }
      return readProposalView(await r.json())
    },

    // The session's explicit review requests; only its owner may decide them.
    async reviewRequests(sessionId) {
      const r = await request(`/api/sessions/${enc(sessionId)}/review-requests`)
      if (!r.ok) throw Object.assign(new Error(await errorText(r)), { status: r.status })
      const body = (await r.json()) as { review_requests?: unknown; can_decide?: unknown }
      const list = Array.isArray(body.review_requests) ? body.review_requests : []
      return { requests: list.map(readReviewRequest).filter((x): x is NonNullable<typeof x> => x !== null), canDecide: body.can_decide === true }
    },

    async decideReview(requestId, decision, note) {
      const r = await request(`/api/review-requests/${enc(requestId)}/decide`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ decision, ...(note ? { note } : {}) }),
      })
      if (!r.ok) throw Object.assign(new Error(await errorText(r)), { status: r.status })
      const out = readReviewRequest(await r.json())
      if (!out) throw new Error('Invalid review request.')
      return out
    },

    connected,
    answersAsks: true,

    files: createFiles({
      base: (sessionId) => `${baseUrl}/api/sessions/${enc(sessionId)}`,
      fetch: fetchImpl,
      headers: bearer,
      check,
      workspace: true,
      commits: true,
    }),
  }
}
