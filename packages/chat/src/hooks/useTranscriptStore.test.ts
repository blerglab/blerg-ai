import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { resumeSeq, useTranscriptStore } from './useTranscriptStore'
import type { AgentEvent } from '../types'

const ev = (seq: number | undefined, id = `c${seq}`, extra: Partial<AgentEvent> = {}): AgentEvent => ({
  type: 'agent_event', session_id: 's1', client_event_id: id, seq, ts: '2026-10-13T00:00:00.000Z',
  kind: 'user_message', payload: { text: `m${seq}`, source: 'chat' }, ...extra,
})

const store = () => useTranscriptStore.getState()
const held = (sessionId = 's1') => store().sessions[sessionId]?.events.map(e => e.client_event_id) ?? []

/** Counts store notifications from here on. */
function countUpdates() {
  const n = { count: 0 }
  const off = useTranscriptStore.subscribe(() => { n.count++ })
  return { n, off }
}

beforeEach(() => {
  useTranscriptStore.setState({ sessions: {} })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('transcript store ingestMany', () => {
  it('commits a replay page of three events in one store update', () => {
    const { n, off } = countUpdates()
    store().ingestMany('s1', [ev(1), ev(2), ev(3)])
    off()
    expect(n.count).toBe(1)
    expect(held()).toEqual(['c1', 'c2', 'c3'])
    expect(store().sessions.s1.lastSeq).toBe(3)
  })

  it('drops events already held and repeats within the batch, and sorts by seq once', () => {
    store().ingestMany('s1', [ev(2), ev(4)])
    const sort = vi.spyOn(Array.prototype, 'sort')
    store().ingestMany('s1', [ev(5), ev(3), ev(4), ev(3), ev(1), ev(undefined, 'live1')])
    expect(sort).toHaveBeenCalledTimes(1)
    expect(held()).toEqual(['c1', 'c2', 'c3', 'c4', 'c5', 'live1'])
    expect(store().sessions.s1.lastSeq).toBe(5)
  })

  it('a batch with nothing new leaves the state as it was', () => {
    store().ingestMany('s1', [ev(1), ev(2)])
    const before = store().sessions
    const { n, off } = countUpdates()
    store().ingestMany('s1', [ev(2), ev(1)])
    store().ingestMany('s1', [])
    off()
    expect(n.count).toBe(0)
    expect(store().sessions).toBe(before)
  })

  it('keeps the events of a different session out and closes the streaming text on a turn end', () => {
    store().appendStreaming('s1', 'Hel')
    store().ingestMany('s1', [ev(1), { ...ev(2), session_id: 's2' }])
    expect(store().sessions.s1.streaming).toBe('Hel')
    expect(store().sessions.s2).toBeUndefined()
    store().ingestMany('s1', [ev(3, 'c3', { kind: 'turn_done', payload: {} })])
    expect(store().sessions.s1.streaming).toBe('')
    expect(held()).toEqual(['c1', 'c3'])
  })

  it('dedupes against events put in the store from outside it (a seeded snapshot)', () => {
    useTranscriptStore.setState({ sessions: { s1: { ...store().sessions.s1, ...emptyWith([ev(1), ev(2)]) } } })
    store().ingestMany('s1', [ev(2), ev(3)])
    store().ingest(ev(1))
    expect(held()).toEqual(['c1', 'c2', 'c3'])
  })
})

describe('transcript store ingest (live events)', () => {
  it('appends an event at or past the last seq without re-sorting, and drops a repeat', () => {
    store().ingestMany('s1', [ev(1), ev(2)])
    const sort = vi.spyOn(Array.prototype, 'sort')
    const { n, off } = countUpdates()
    store().ingest(ev(3))
    store().ingest(ev(4))
    store().ingest(ev(4)) // the same event again (a replay/live overlap)
    store().ingest(ev(5))
    off()
    expect(sort).not.toHaveBeenCalled()
    expect(n.count).toBe(3)
    expect(held()).toEqual(['c1', 'c2', 'c3', 'c4', 'c5'])
    expect(store().sessions.s1.lastSeq).toBe(5)
  })

  it('still puts an event that arrives out of order in its place, and seq-less events stay last', () => {
    store().ingestMany('s1', [ev(1), ev(3)])
    store().ingest(ev(undefined, 'live1'))
    store().ingest(ev(2))
    store().ingest(ev(4))
    store().ingest(ev(undefined, 'live2'))
    expect(held()).toEqual(['c1', 'c2', 'c3', 'c4', 'live1', 'live2'])
  })
})

describe('transcript store resetEvents', () => {
  it('drops the events and where they reach, keeping the clock offset and the typing text', () => {
    store().ingestMany('s1', [ev(5), ev(6)])
    store().ingestReplayDone('s1', { lastSeq: 6, hasMore: false, older: true, hasOlder: true, firstSeq: 5, serverTime: new Date(Date.now() - 4000).toISOString() })
    store().appendStreaming('s1', 'typing')
    const offset = store().sessions.s1.clockOffset
    expect(offset).toBeGreaterThan(3000)
    store().resetEvents('s1')
    const t = store().sessions.s1
    expect(t).toMatchObject({ events: [], lastSeq: 0, firstSeq: 0, hasOlder: false, hasMore: false, replayDone: false, streaming: 'typing', clockOffset: offset })
    // The ids went with the events: the same event is taken again.
    store().ingest(ev(5))
    expect(held()).toEqual(['c5'])
  })
})

describe('transcript store resumeSeq', () => {
  it('resumes from the replay cursor while a forward replay is unfinished, from the last seq after it', () => {
    expect(resumeSeq(undefined)).toBe(0)
    store().ingestMany('s1', [ev(1), ev(2)])
    store().ingestReplayDone('s1', { lastSeq: 2, hasMore: true })
    store().ingestMany('s1', [ev(900, 'live900')])
    expect(store().sessions.s1.lastSeq).toBe(900)
    expect(resumeSeq(store().sessions.s1)).toBe(2)
    // A tail page says nothing about the forward replay: the cursor stays.
    store().ingestReplayDone('s1', { lastSeq: 900, hasMore: false, older: true, hasOlder: false, firstSeq: 1 })
    expect(resumeSeq(store().sessions.s1)).toBe(2)
    store().ingestMany('s1', [ev(3)])
    store().ingestReplayDone('s1', { lastSeq: 3, hasMore: false })
    expect(resumeSeq(store().sessions.s1)).toBe(900)
    store().ingestReplayDone('s1', { lastSeq: 950, hasMore: true })
    store().resetEvents('s1')
    expect(resumeSeq(store().sessions.s1)).toBe(0)
  })
})

function emptyWith(events: AgentEvent[]) {
  return { events, streaming: '', lastSeq: Math.max(0, ...events.map(e => e.seq ?? 0)), replayDone: true, hasMore: false, hasOlder: false, firstSeq: 0, clockOffset: 0 }
}

describe('transcript store thinking flag', () => {
  const live = (kind: string, id: string, payload: unknown = {}) =>
    ev(undefined, id, { kind: kind as AgentEvent['kind'], payload, transient: true })
  const thinking = () => store().sessions.s1?.thinking ?? false

  it('opens on a live thinking_started and closes on thinking_done, holding neither', () => {
    store().ingest(ev(1))
    store().ingest(live('thinking_started', 't1'))
    expect(thinking()).toBe(true)
    store().ingest(live('thinking_done', 't2'))
    expect(thinking()).toBe(false)
    expect(held()).toEqual(['c1'])
  })

  it('closes on the thinking itself, a tool call or the turn end when the done is lost', () => {
    for (const close of [
      ev(2, 'x2', { kind: 'assistant_thinking', payload: { text: 'hmm' } }),
      ev(2, 'x3', { kind: 'tool_call', payload: { tool: 'Bash', call_id: 'a', input: {} } }),
      ev(2, 'x4', { kind: 'turn_done', payload: {} }),
    ]) {
      useTranscriptStore.setState({ sessions: {} })
      store().ingest(live('thinking_started', 't1'))
      store().ingest(close)
      expect(thinking(), close.kind).toBe(false)
    }
  })

  it('ignores a subagent thinking', () => {
    store().ingest(live('thinking_started', 't1', { parent_call_id: 'toolu_a' }))
    expect(thinking()).toBe(false)
    store().ingest(live('thinking_started', 't2'))
    store().ingest(live('thinking_done', 't3', { parent_call_id: 'toolu_a' }))
    expect(thinking()).toBe(true)
  })

  it('reads the live events of a held page in order, never holding them', () => {
    store().ingestMany('s1', [ev(1), live('thinking_started', 't1'), live('thinking_done', 't2'), live('thinking_started', 't3')])
    expect(thinking()).toBe(true)
    expect(held()).toEqual(['c1'])
  })
})
