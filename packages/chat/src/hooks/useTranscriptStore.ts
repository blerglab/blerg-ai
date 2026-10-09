// Transcript store for sessions: persisted events (seq-ordered, deduped) plus the in-flight
// streaming assistant text (transient deltas). Kept per session for the life of the page, so
// leaving a session and coming back shows it at once and resumes from the last seq held.
import { create } from 'zustand'
import type { OlderPage, ReplayDone } from '../transport/types'
import type { AgentEvent, AssistantTextPayload } from '../types'

export interface SessionTranscript {
  events: AgentEvent[] // sorted by seq; live unseq'd events at the tail
  streaming: string // accumulated transient deltas for the in-flight message
  lastSeq: number
  replayDone: boolean
  hasMore: boolean
  // The transcript is loaded from its end; older events remain to be fetched, and firstSeq is the
  // oldest seq held so far (where the next older page starts).
  hasOlder: boolean
  firstSeq: number
  // client clock − server clock (ms), from the last replay's server_time.
  // Event ts values are server time; subtract this from Date.now() to get
  // "server now" so elapsed times don't inherit the browser's clock skew.
  clockOffset: number
}

interface TranscriptState {
  sessions: Record<string, SessionTranscript>
  ingest: (ev: AgentEvent) => void
  /** A typing delta: appended to the in-flight text. */
  appendStreaming: (sessionId: string, delta: string) => void
  ingestReplayDone: (sessionId: string, done: ReplayDone) => void
  /** An older page, put in front of what is held. */
  ingestOlder: (sessionId: string, page: OlderPage) => void
  clear: (sessionId: string) => void
}

const empty = (): SessionTranscript => ({
  events: [],
  streaming: '',
  lastSeq: 0,
  replayDone: false,
  hasMore: false,
  hasOlder: false,
  firstSeq: 0,
  clockOffset: 0,
})

function offsetFrom(serverTime: string | undefined, fallback: number): number {
  const t = serverTime ? Date.parse(serverTime) : NaN
  return Number.isFinite(t) ? Date.now() - t : fallback
}

const bySeq = (a: AgentEvent, b: AgentEvent) => (a.seq ?? Number.MAX_SAFE_INTEGER) - (b.seq ?? Number.MAX_SAFE_INTEGER)

export const useTranscriptStore = create<TranscriptState>((set) => ({
  sessions: {},

  ingest: (ev) =>
    set((state) => {
      const cur = state.sessions[ev.session_id] ?? empty()

      // Transient streaming deltas accumulate separately and never persist.
      if (ev.transient && ev.kind === 'assistant_text') {
        const p = ev.payload as AssistantTextPayload
        return { sessions: { ...state.sessions, [ev.session_id]: { ...cur, streaming: cur.streaming + p.text } } }
      }

      // Dedup: resends and replay overlaps share client_event_id.
      if (cur.events.some((e) => e.client_event_id === ev.client_event_id)) {
        return state
      }

      const events = [...cur.events, ev].sort(bySeq)
      // A consolidated assistant_text (or turn end) closes the streaming buffer.
      const closesStream =
        (ev.kind === 'assistant_text' && (ev.payload as AssistantTextPayload).done) ||
        ev.kind === 'turn_done' ||
        ev.kind === 'error'
      return {
        sessions: {
          ...state.sessions,
          [ev.session_id]: {
            ...cur,
            events,
            streaming: closesStream ? '' : cur.streaming,
            lastSeq: Math.max(cur.lastSeq, ev.seq ?? 0),
          },
        },
      }
    }),

  appendStreaming: (sessionId, delta) =>
    set((state) => {
      const cur = state.sessions[sessionId] ?? empty()
      return { sessions: { ...state.sessions, [sessionId]: { ...cur, streaming: cur.streaming + delta } } }
    }),

  ingestReplayDone: (sessionId, done) =>
    set((state) => {
      const cur = state.sessions[sessionId] ?? empty()
      if (done.older) {
        // A backward page says nothing about the forward replay, so hasMore stays as it was.
        const first = done.firstSeq ?? 0
        return {
          sessions: {
            ...state.sessions,
            [sessionId]: {
              ...cur,
              replayDone: true,
              hasOlder: done.hasOlder === true,
              firstSeq: first > 0 && (cur.firstSeq === 0 || first < cur.firstSeq) ? first : cur.firstSeq,
              clockOffset: offsetFrom(done.serverTime, cur.clockOffset),
              lastSeq: Math.max(cur.lastSeq, done.lastSeq),
            },
          },
        }
      }
      return {
        sessions: {
          ...state.sessions,
          [sessionId]: {
            ...cur,
            replayDone: true,
            hasMore: done.hasMore,
            clockOffset: offsetFrom(done.serverTime, cur.clockOffset),
            lastSeq: Math.max(cur.lastSeq, done.lastSeq),
          },
        },
      }
    }),

  ingestOlder: (sessionId, page) =>
    set((state) => {
      const cur = state.sessions[sessionId] ?? empty()
      const known = new Set(cur.events.map(e => e.client_event_id))
      const fresh = page.events.filter(e => !known.has(e.client_event_id))
      const events = fresh.length > 0 ? [...cur.events, ...fresh].sort(bySeq) : cur.events
      const first = page.firstSeq > 0 ? page.firstSeq : (fresh[0]?.seq ?? 0)
      return {
        sessions: {
          ...state.sessions,
          [sessionId]: {
            ...cur,
            events,
            replayDone: true,
            hasOlder: page.hasOlder,
            firstSeq: first > 0 && (cur.firstSeq === 0 || first < cur.firstSeq) ? first : cur.firstSeq,
            clockOffset: offsetFrom(page.serverTime, cur.clockOffset),
          },
        },
      }
    }),

  clear: (sessionId) =>
    set((state) => {
      const sessions = { ...state.sessions }
      delete sessions[sessionId]
      return { sessions }
    }),
}))

export const emptyTranscript = empty()
