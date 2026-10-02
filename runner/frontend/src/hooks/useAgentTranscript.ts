// Transcript store for agent-kind sessions: persisted events (seq-ordered,
// deduped) plus the in-flight streaming assistant text (transient deltas).
import { create } from 'zustand'
import type { AgentEvent, AgentEventsReplayDone, AssistantTextPayload } from '../types'

interface SessionTranscript {
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

interface AgentTranscriptState {
  sessions: Record<string, SessionTranscript>
  ingest: (ev: AgentEvent) => void
  ingestReplayDone: (msg: AgentEventsReplayDone) => void
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

export const useAgentTranscript = create<AgentTranscriptState>((set) => ({
  sessions: {},

  ingest: (ev) =>
    set((state) => {
      const cur = state.sessions[ev.session_id] ?? empty()

      // Transient streaming deltas accumulate separately and never persist.
      if (ev.transient && ev.kind === 'assistant_text') {
        const p = ev.payload as AssistantTextPayload
        return {
          sessions: {
            ...state.sessions,
            [ev.session_id]: { ...cur, streaming: cur.streaming + p.text },
          },
        }
      }

      // Dedup: resends and replay overlaps share client_event_id.
      if (cur.events.some((e) => e.client_event_id === ev.client_event_id)) {
        return state
      }

      const events = [...cur.events, ev].sort(
        (a, b) => (a.seq ?? Number.MAX_SAFE_INTEGER) - (b.seq ?? Number.MAX_SAFE_INTEGER),
      )
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

  ingestReplayDone: (msg) =>
    set((state) => {
      const cur = state.sessions[msg.session_id] ?? empty()
      if (msg.older) {
        // A backward page says nothing about the forward replay, so hasMore stays as it was.
        const first = msg.first_seq ?? 0
        return {
          sessions: {
            ...state.sessions,
            [msg.session_id]: {
              ...cur,
              replayDone: true,
              hasOlder: msg.has_older === true,
              firstSeq: first > 0 && (cur.firstSeq === 0 || first < cur.firstSeq) ? first : cur.firstSeq,
              clockOffset: offsetFrom(msg.server_time, cur.clockOffset),
              lastSeq: Math.max(cur.lastSeq, msg.last_seq),
            },
          },
        }
      }
      return {
        sessions: {
          ...state.sessions,
          [msg.session_id]: {
            ...cur,
            replayDone: true,
            hasMore: msg.has_more,
            clockOffset: offsetFrom(msg.server_time, cur.clockOffset),
            lastSeq: Math.max(cur.lastSeq, msg.last_seq),
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
