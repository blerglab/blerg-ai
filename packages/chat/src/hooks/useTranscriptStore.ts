// Transcript store for sessions: persisted events (seq-ordered, deduped) plus the in-flight
// streaming assistant text (transient deltas). Kept per session for the life of the page, so
// leaving a session and coming back shows it at once and resumes from the last seq held.
import { create } from 'zustand'
import type { OlderPage, ReplayDone } from '../transport/types'
import { isTurnError } from '../model/notices'
import { NO_REASONING, nextReasoning, type ReasoningScopes } from '../model/reasoning'
import { nextAnswering, nextThinking } from '../model/workingActivity'
import type { AgentEvent, AssistantTextPayload } from '../types'

export interface SessionTranscript {
  events: AgentEvent[] // sorted by seq; live unseq'd events at the tail
  streaming: string // accumulated transient deltas for the in-flight message
  // The main agent has a thinking block open (transient thinking_started / _done; see
  // model/workingActivity nextThinking). Live only: a transcript loaded later never sets it.
  // Optional so a host's own transcript fixture need not name it (absent: false).
  reasoning?: ReasoningScopes
  thinking?: boolean
  // The model has said something live since the newest retry notice (nextAnswering): the retry got
  // through, and the working line no longer says it is retrying. Optional like thinking.
  answering?: boolean
  lastSeq: number
  replayDone: boolean
  hasMore: boolean
  // The transcript is loaded from its end; older events remain to be fetched, and firstSeq is the
  // oldest seq held so far (where the next older page starts).
  hasOlder: boolean
  firstSeq: number
  // Where an unfinished forward replay has reached (the last forward page's last_seq while
  // hasMore). A live event can be held past the gap still to replay, so a resume starts here
  // rather than at lastSeq (see resumeSeq). Absent when no forward replay is under way.
  replayCursor?: number
  // How far the transcript held is complete: every seq up to it was replayed (a replay_done covers
  // every seq up to its last_seq) or held in order since. A live event past a hole does not move it,
  // so a later visit resumes from here and the hole is replayed (#281). Absent on a transcript a host
  // built itself: resumeSeq then uses lastSeq, as before.
  contigSeq?: number
  // client clock − server clock (ms), from the last replay's server_time.
  // Event ts values are server time; subtract this from Date.now() to get
  // "server now" so elapsed times don't inherit the browser's clock skew.
  clockOffset: number
}

interface TranscriptState {
  sessions: Record<string, SessionTranscript>
  /** One event (a live one, as a rule): appended when it is the newest, put in its place otherwise. */
  ingest: (ev: AgentEvent) => void
  /** A replayed page: the new events among them committed in one update (deduped, sorted once). */
  ingestMany: (sessionId: string, events: AgentEvent[]) => void
  /** A typing delta: appended to the in-flight text. */
  appendStreaming: (sessionId: string, delta: string) => void
  ingestReplayDone: (sessionId: string, done: ReplayDone) => void
  /** An older page, put in front of what is held. */
  ingestOlder: (sessionId: string, page: OlderPage) => void
  clear: (sessionId: string) => void
  /** Forgets the events held and how far they reach (the transcript is about to be loaded again
   *  from its end); the clock offset and the typing text stay. */
  resetEvents: (sessionId: string) => void
}

const empty = (): SessionTranscript => ({
  events: [],
  streaming: '',
  thinking: false,
  lastSeq: 0,
  replayDone: false,
  hasMore: false,
  hasOlder: false,
  firstSeq: 0,
  contigSeq: 0,
  clockOffset: 0,
})

/** The watermark moved over the events held in order after it (`events` sorted by seq, seq-less
 *  last). Found by bisection, then walked only as far as the held seqs run on without a hole, so a
 *  live event appended past a hole costs nothing, and one that fills it walks the run it joins. */
function advanceContig(contig: number | undefined, events: readonly AgentEvent[], newest?: number): number | undefined {
  if (contig == null) return contig
  // A single event that is not the next seq cannot move it.
  if (newest !== undefined && newest !== contig + 1) return contig
  let lo = 0, hi = events.length
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    const seq = events[mid].seq
    if (seq != null && seq <= contig) lo = mid + 1
    else hi = mid
  }
  let at = contig
  for (let i = lo; i < events.length; i++) {
    const seq = events[i].seq
    if (seq == null || seq !== at + 1) break
    at = seq
  }
  return at
}

function offsetFrom(serverTime: string | undefined, fallback: number): number {
  const t = serverTime ? Date.parse(serverTime) : NaN
  return Number.isFinite(t) ? Date.now() - t : fallback
}

const bySeq = (a: AgentEvent, b: AgentEvent) => (a.seq ?? Number.MAX_SAFE_INTEGER) - (b.seq ?? Number.MAX_SAFE_INTEGER)

// The client_event_ids of a session's events, so a duplicate is found without scanning them. Kept
// beside the events array it describes (keyed by that very array) rather than in the state: an
// update that makes a new array hands its set on, and an array put into the store from outside
// (a host or a test calling setState) simply has its set built on first use.
const idSets = new WeakMap<AgentEvent[], Set<string>>()

function idsOf(events: AgentEvent[]): Set<string> {
  let ids = idSets.get(events)
  if (!ids) {
    ids = new Set(events.map(e => e.client_event_id))
    idSets.set(events, ids)
  }
  return ids
}

/** The set grown in place now describes `next`; `prev` no longer has one. */
function handOn(prev: AgentEvent[], next: AgentEvent[], ids: Set<string>) {
  idSets.delete(prev)
  idSets.set(next, ids)
}

// The typing bubble belongs to the main agent. A child's completed message is
// drawn inside its own card and cannot replace any part of that bubble.
const isMainEvent = (ev: AgentEvent) => !(ev.payload as { parent_call_id?: string } | null)?.parent_call_id

/** Only a main-agent consolidated message (or turn end) closes its streaming buffer.
 *  A retry notice or a dropped message's error leaves the turn running. */
const closesStream = (ev: AgentEvent) => isMainEvent(ev) && (
  (ev.kind === 'assistant_text' && (ev.payload as AssistantTextPayload).done) ||
  ev.kind === 'turn_done' ||
  isTurnError(ev)
)

export const useTranscriptStore = create<TranscriptState>((set) => ({
  sessions: {},

  ingest: (ev) =>
    set((state) => {
      const cur = state.sessions[ev.session_id] ?? empty()

      // Transient streaming deltas accumulate separately and never persist; another transient event
      // (a thinking block's start or end) is never held either: it only moves the thinking flag.
      if (ev.transient) {
        const reasoning = nextReasoning(cur.reasoning ?? NO_REASONING, ev)
        const thinking = nextThinking(cur.thinking ?? false, ev)
        const answering = nextAnswering(cur.answering ?? false, ev)
        if (ev.kind === 'assistant_text' && isMainEvent(ev)) {
          const p = ev.payload as AssistantTextPayload
          return { sessions: { ...state.sessions, [ev.session_id]: { ...cur, reasoning, thinking, answering, streaming: cur.streaming + p.text } } }
        }
        return reasoning === (cur.reasoning ?? NO_REASONING) && thinking === (cur.thinking ?? false) && answering === (cur.answering ?? false)
          ? state
          : { sessions: { ...state.sessions, [ev.session_id]: { ...cur, reasoning, thinking, answering } } }
      }

      // Dedup: resends and replay overlaps share client_event_id.
      const ids = idsOf(cur.events)
      if (ids.has(ev.client_event_id)) {
        return state
      }

      // A live event is the newest as a rule: appended as it is. Seq-less events stay at the end
      // (as bySeq orders them), so an event with a seq goes past one only through the sort.
      const last = cur.events[cur.events.length - 1]
      const inOrder = ev.seq == null || last == null || (last.seq != null && ev.seq >= last.seq)
      const events = inOrder ? [...cur.events, ev] : [...cur.events, ev].sort(bySeq)
      ids.add(ev.client_event_id)
      handOn(cur.events, events, ids)
      return {
        sessions: {
          ...state.sessions,
          [ev.session_id]: {
            ...cur,
            events,
            streaming: closesStream(ev) ? '' : cur.streaming,
            reasoning: nextReasoning(cur.reasoning ?? NO_REASONING, ev),
            thinking: nextThinking(cur.thinking ?? false, ev),
            answering: nextAnswering(cur.answering ?? false, ev),
            lastSeq: Math.max(cur.lastSeq, ev.seq ?? 0),
            contigSeq: advanceContig(cur.contigSeq, events, ev.seq ?? -1),
          },
        },
      }
    }),

  ingestMany: (sessionId, batch) =>
    set((state) => {
      const cur = state.sessions[sessionId] ?? empty()
      const ids = idsOf(cur.events)
      const fresh: AgentEvent[] = []
      let streaming = cur.streaming
      let reasoning = cur.reasoning ?? NO_REASONING
      let thinking = cur.thinking ?? false
      let answering = cur.answering ?? false
      let lastSeq = cur.lastSeq
      for (const ev of batch) {
        if (ev.session_id !== sessionId) continue
        if (ev.transient) {
          reasoning = nextReasoning(reasoning, ev)
          thinking = nextThinking(thinking, ev)
          answering = nextAnswering(answering, ev)
          if (ev.kind === 'assistant_text' && isMainEvent(ev)) streaming += (ev.payload as AssistantTextPayload).text
          continue
        }
        if (ids.has(ev.client_event_id)) continue
        ids.add(ev.client_event_id)
        fresh.push(ev)
        if (closesStream(ev)) streaming = ''
        reasoning = nextReasoning(reasoning, ev)
        thinking = nextThinking(thinking, ev)
        answering = nextAnswering(answering, ev)
        lastSeq = Math.max(lastSeq, ev.seq ?? 0)
      }
      if (fresh.length === 0 && reasoning === (cur.reasoning ?? NO_REASONING) && streaming === cur.streaming && thinking === (cur.thinking ?? false) &&
        answering === (cur.answering ?? false)) return state
      let events = cur.events
      if (fresh.length > 0) {
        events = [...cur.events, ...fresh].sort(bySeq)
        handOn(cur.events, events, ids)
      }
      return { sessions: { ...state.sessions, [sessionId]: { ...cur, events, streaming, reasoning, thinking, answering, lastSeq, contigSeq: fresh.length > 0 ? advanceContig(cur.contigSeq, events) : cur.contigSeq } } }
    }),

  appendStreaming: (sessionId, delta) =>
    set((state) => {
      const cur = state.sessions[sessionId] ?? empty()
      // The model is writing its answer: any think is over, and any retry got through.
      return { sessions: { ...state.sessions, [sessionId]: { ...cur, thinking: false, answering: true, streaming: cur.streaming + delta } } }
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
              // The tail covers every seq up to its last_seq.
              contigSeq: cur.contigSeq == null ? undefined : advanceContig(Math.max(cur.contigSeq, done.lastSeq), cur.events),
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
            replayCursor: done.hasMore ? done.lastSeq : undefined,
            clockOffset: offsetFrom(done.serverTime, cur.clockOffset),
            lastSeq: Math.max(cur.lastSeq, done.lastSeq),
            // A forward page covers every seq up to its last_seq.
            contigSeq: cur.contigSeq == null ? undefined : advanceContig(Math.max(cur.contigSeq, done.lastSeq), cur.events),
          },
        },
      }
    }),

  ingestOlder: (sessionId, page) =>
    set((state) => {
      const cur = state.sessions[sessionId] ?? empty()
      const ids = idsOf(cur.events)
      const fresh: AgentEvent[] = []
      for (const e of page.events) {
        if (ids.has(e.client_event_id)) continue
        ids.add(e.client_event_id)
        fresh.push(e)
      }
      let events = cur.events
      if (fresh.length > 0) {
        events = [...cur.events, ...fresh].sort(bySeq)
        handOn(cur.events, events, ids)
      }
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

  resetEvents: (sessionId) =>
    set((state) => {
      const cur = state.sessions[sessionId]
      if (!cur) return state
      return {
        sessions: {
          ...state.sessions,
          [sessionId]: { ...empty(), streaming: cur.streaming, clockOffset: cur.clockOffset },
        },
      }
    }),
}))

export const emptyTranscript = empty()

/** The seq to resume a session's transcript after: the last seq held, or, while a forward replay
 *  is unfinished, where that replay reached (a live event held past the gap must not skip it). */
export function resumeSeq(t: SessionTranscript | undefined): number {
  if (!t) return 0
  const reached = t.hasMore && t.replayCursor != null ? Math.min(t.replayCursor, t.lastSeq) : t.lastSeq
  // A hole a live event left (seq 3 missing behind a held 4): resume before it, so it is replayed.
  // A zero watermark counts too: an empty tail can finish before the first event, and a live seq 2
  // with seq 1 missed must resume from 0.
  return t.contigSeq != null ? Math.min(reached, t.contigSeq) : reached
}
