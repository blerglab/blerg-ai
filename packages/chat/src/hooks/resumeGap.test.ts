// #281 review 8: leaving a session with a live hole (seq 3 missing behind a held 4) must not skip the
// hole on return: the transcript resumes from where it is complete, and the replay fills it.
import { beforeEach, expect, it } from 'vitest'
import { resumeSeq, useTranscriptStore } from './useTranscriptStore'
import type { AgentEvent } from '../types'

const ev = (seq: number): AgentEvent => ({ session_id: 's', client_event_id: `e${seq}`, seq, ts: '2026-10-27T00:00:00Z', kind: 'user_message', payload: { text: `m${seq}` } } as AgentEvent)
const held = () => useTranscriptStore.getState().sessions.s
beforeEach(() => useTranscriptStore.setState({ sessions: {} }))

it('resumes before a live hole, and past it once it is filled', () => {
  const store = useTranscriptStore.getState()
  store.ingestMany('s', [ev(1), ev(2)])
  store.ingestReplayDone('s', { lastSeq: 2, hasMore: false, older: true, firstSeq: 1, hasOlder: false })
  store.ingest(ev(4))
  expect(held().events.map(e => e.seq)).toEqual([1, 2, 4])
  expect(resumeSeq(held())).toBe(2)
  // The replay from 2 brings 3 (and 4 again, deduplicated): complete again.
  store.ingestMany('s', [ev(3), ev(4)])
  store.ingestReplayDone('s', { lastSeq: 4, hasMore: false })
  expect(held().events.map(e => e.seq)).toEqual([1, 2, 3, 4])
  expect(resumeSeq(held())).toBe(4)
})

it('live events in order move the watermark with them', () => {
  const store = useTranscriptStore.getState()
  store.ingestMany('s', [ev(1)])
  store.ingestReplayDone('s', { lastSeq: 1, hasMore: false })
  for (const seq of [2, 3, 4]) store.ingest(ev(seq))
  expect(resumeSeq(held())).toBe(4)
})

it('a replay that covers seqs the server never stored still completes the transcript', () => {
  const store = useTranscriptStore.getState()
  store.ingestMany('s', [ev(1), ev(5)])
  store.ingestReplayDone('s', { lastSeq: 5, hasMore: false })
  expect(resumeSeq(held())).toBe(5)
})

it('a zero watermark counts: a live seq 2 after an empty tail resumes from 0, forward, not as a new tail (re-review 1)', async () => {
  const { renderHook } = await import('@testing-library/react')
  const { useSession } = await import('./useSession')
  const { createFakeTransport } = await import('../test/fakeTransport')
  const store = useTranscriptStore.getState()
  store.ingestReplayDone('s', { lastSeq: 0, hasMore: false, older: true, firstSeq: 0, hasOlder: false })
  store.ingest(ev(2))
  expect(held().events.map(e => e.seq)).toEqual([2])
  expect(resumeSeq(held())).toBe(0)
  const t = createFakeTransport()
  const view = renderHook(() => useSession(t.transport, 's'))
  expect(t.subscriptions.at(-1)).toEqual({ sessionId: 's', afterSeq: 0, tail: false })
  view.unmount()
})

it('a first visit still asks for the newest events', async () => {
  const { renderHook } = await import('@testing-library/react')
  const { useSession } = await import('./useSession')
  const { createFakeTransport } = await import('../test/fakeTransport')
  const t = createFakeTransport()
  const view = renderHook(() => useSession(t.transport, 'fresh'))
  expect(t.subscriptions.at(-1)).toEqual({ sessionId: 'fresh', afterSeq: 0, tail: true })
  view.unmount()
})
