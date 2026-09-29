import { beforeEach, describe, expect, it } from 'vitest'
import { useAgentTranscript } from './useAgentTranscript'
import type { AgentEvent } from '../types'

function ev(over: Partial<AgentEvent>): AgentEvent {
  return {
    type: 'agent_event',
    session_id: 's1',
    client_event_id: over.client_event_id ?? Math.random().toString(36),
    ts: '2026-07-26T00:00:00Z',
    kind: 'user_message',
    payload: { text: 'hi', source: 'chat' },
    ...over,
  }
}

describe('useAgentTranscript', () => {
  beforeEach(() => {
    useAgentTranscript.setState({ sessions: {} })
  })

  it('orders events by seq and dedupes by client_event_id', () => {
    const { ingest } = useAgentTranscript.getState()
    ingest(ev({ client_event_id: 'b', seq: 2, kind: 'turn_done', payload: {} }))
    ingest(ev({ client_event_id: 'a', seq: 1 }))
    ingest(ev({ client_event_id: 'a', seq: 1 })) // resend
    const t = useAgentTranscript.getState().sessions['s1']
    expect(t.events.map(e => e.client_event_id)).toEqual(['a', 'b'])
    expect(t.lastSeq).toBe(2)
  })

  it('accumulates transient deltas and clears on consolidated text', () => {
    const { ingest } = useAgentTranscript.getState()
    ingest(ev({ kind: 'assistant_text', transient: true, payload: { text: 'Hel', done: false } }))
    ingest(ev({ kind: 'assistant_text', transient: true, payload: { text: 'lo', done: false } }))
    expect(useAgentTranscript.getState().sessions['s1'].streaming).toBe('Hello')
    ingest(ev({ client_event_id: 'final', seq: 3, kind: 'assistant_text', payload: { text: 'Hello', done: true } }))
    const t = useAgentTranscript.getState().sessions['s1']
    expect(t.streaming).toBe('')
    expect(t.events).toHaveLength(1)
  })

  it('tracks replay_done bookkeeping', () => {
    const { ingestReplayDone } = useAgentTranscript.getState()
    ingestReplayDone({ type: 'agent_events_replay_done', session_id: 's1', last_seq: 42, has_more: true })
    const t = useAgentTranscript.getState().sessions['s1']
    expect(t.replayDone).toBe(true)
    expect(t.hasMore).toBe(true)
    expect(t.lastSeq).toBe(42)
  })

  it('clear removes a session transcript', () => {
    const { ingest, clear } = useAgentTranscript.getState()
    ingest(ev({ seq: 1 }))
    clear('s1')
    expect(useAgentTranscript.getState().sessions['s1']).toBeUndefined()
  })
})
