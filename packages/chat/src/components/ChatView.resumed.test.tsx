import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { AgentEvent } from '../types'

// A resumed subagent: a card of its own with a "resumed" chip, and, while it runs with no call in
// flight, the working line names it.

const S = 1000
const NOW = Date.parse('2026-10-15T12:00:00Z')
let seq = 0
function ev(kind: string, payload: unknown, at: number): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: new Date(at).toISOString(), kind, payload })
}
const ACK = '{"success":true,"message":"Resuming agent a1b2c3d","resumedAgentId":"a1b2c3d4e5f6a7b80"}'
const TITLE = 'Implement the data tasks of phase zero, all five of them'

function resumedRun(at: number, background = false) {
  return [
    ev('user_message', { text: 'go', source: 'chat' }, at - 60 * S),
    ev('status_changed', { status: 'running' }, at - 60 * S),
    ev('tool_call', { tool: 'Agent', call_id: 'A', input: { description: TITLE, prompt: 'Do them', subagent_type: 'general-purpose' } }, at - 50 * S),
    ev('subagent_started', { child_id: 'A', agent_type: 'general-purpose', model: '', summary: TITLE }, at - 50 * S),
    ev('subagent_done', { child_id: 'A', agent_type: 'general-purpose', model: '', summary: 'All in.' }, at - 40 * S),
    ev('tool_result', { call_id: 'A', output: 'All in.', is_error: false, duration_ms: 0 }, at - 40 * S),
    ev('tool_call', { tool: 'SendMessage', call_id: 'S', input: { to: 'a1b2c3d4e5f6a7b80', summary: 'Fix round 1', message: 'Fix both issues.' } }, at - 30 * S),
    ev('subagent_started', { child_id: 'S', agent_type: 'general-purpose', model: '', summary: TITLE, resumed: true, resumed_from: 'A', ...(background ? { background: true } : {}) }, at - 30 * S),
    ev('tool_result', { call_id: 'S', output: ACK, is_error: false, duration_ms: 0 }, at - 30 * S),
  ]
}

describe('resumed subagent', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('draws a second card with a resumed chip; the first stays done', () => {
    seed(...resumedRun(NOW))
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'running' })} />)
    const cards = screen.getAllByTestId('subagent-card')
    expect(cards).toHaveLength(2)
    expect(cards[0].getAttribute('data-status')).toBe('done')
    expect(cards[0].querySelector('[data-testid="subagent-resumed"]')).toBeNull()
    expect(cards[1].getAttribute('data-status')).toBe('running')
    expect(cards[1].getAttribute('data-call-id')).toBe('S')
    expect(cards[1].querySelector('[data-testid="subagent-resumed"]')?.textContent).toBe('resumed')
    expect(cards[1].querySelector('[data-testid="subagent-title"]')?.textContent).toBe(`↳ ${TITLE}`)
    // One row: the title, the small "resumed" marker and the state glyph; the tooltip says it too.
    const row = cards[1].querySelector('[data-testid="subagent-row"]')!
    expect(row.textContent).toBe(`↳ ${TITLE}resumed▸`)
    expect(row.getAttribute('title')).toBe(`${TITLE} · resumed · general-purpose · configured effort: unknown · running`)
    expect(row.getAttribute('aria-label')).toBe(row.getAttribute('title'))
    // the acknowledgement is not shown as its report
    expect(cards[1].textContent).not.toContain('resumedAgentId')
  })

  it('names it on the working line when no call is in flight', () => {
    seed(...resumedRun(NOW))
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'running' })} />)
    expect(screen.getByTestId('waiting-on').textContent).toBe(` · waiting on ${TITLE.slice(0, 39)}… 0:30`)
  })

  it('leaves a background one to the background line', () => {
    seed(...resumedRun(NOW, true))
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'running' })} />)
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    act(() => { vi.advanceTimersByTime(5 * S) })
    expect(screen.queryByTestId('waiting-on')).toBeNull()
  })

  it('names it only once the history is in, like a call', () => {
    const t: FakeTransport = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
    const events = resumedRun(NOW)
    act(() => { for (const e of events.slice(0, 8)) t.emit(e); t.replayDone({ lastSeq: 8, hasMore: true }) })
    act(() => { vi.advanceTimersByTime(5 * S) })
    expect(screen.queryByTestId('waiting-on')).toBeNull()
    act(() => { t.emit(events[8]); t.replayDone({ lastSeq: 9, hasMore: false }) })
    expect(screen.getByTestId('waiting-on').textContent).toContain(`waiting on ${TITLE.slice(0, 39)}…`)
  })

  it('reads stopped, not running, in a session that is over with no end recorded', () => {
    seed(...resumedRun(NOW))
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'stopped' })} />)
    expect(screen.getAllByTestId('subagent-card')[1].getAttribute('data-status')).toBe('stopped')
  })

  it('shows a hand-back\'s report without the CLI\'s JSON head', () => {
    const events = resumedRun(NOW)
    events[8] = ev('tool_result', { call_id: 'S', output: '{"success":true,"message":"Resumed agent. Its final report is not in this message."}\nRenamed the file.', is_error: false, duration_ms: 0 }, NOW)
    seed(...events, ev('subagent_done', { child_id: 'S', agent_type: 'general-purpose', model: '', summary: 'Renamed the file.', resumed: true }, NOW))
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} />)
    const card = screen.getAllByTestId('subagent-card')[1]
    act(() => { (card.querySelector('.subagent-row') as HTMLButtonElement).click() })
    expect(card.textContent).toContain('Renamed the file.')
    expect(card.textContent).not.toContain('Resumed agent.')
  })
})
