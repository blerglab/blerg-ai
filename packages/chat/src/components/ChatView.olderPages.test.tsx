import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Profiler } from 'react'
import { act, render } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { installIntersectionObserver, intersect } from '../test/intersection'
import { SESSION, ev as mk, meta, resetChatState } from '../test/chat'
import { OLDER_PAGE } from '../hooks/useSession'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

// A long session opened from its end, its history coming in page by page as the transcript asks for
// it (the sentinel above the first row stays in view: the pages are mostly a subagent's own steps,
// which draw no row of their own). Each older page moves the edge of what is held into the middle
// of some subagent's work from hours ago: its steps are held, its start (and its launch into the
// background) are not yet. Whatever sits at the bottom of the chat must not change page by page.

const S = 1000
const H = 3600 * S
const NOW = Date.parse('2026-10-15T12:00:00Z')
const TAIL = 200
// Each subagent's events fill two pages, so the edge of what is held falls on a launch on one page
// and in the middle of a subagent's steps on the next. The turn now running fills out the tail.
const BLOCK = 2 * OLDER_PAGE
const TURN = 7

let seq = 0
function ev(kind: string, payload: unknown, at: number): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: new Date(at).toISOString(), kind, payload })
}

// Background subagents from ten hours ago, one after another, each launched and then at work for a
// long while (no end recorded: the daemon was restarted under them), then the turn now running.
function history(): AgentEvent[] {
  const out: AgentEvent[] = []
  for (let b = 0; b < 6; b++) {
    const id = `A${b}`
    let at = NOW - 10 * H + b * 400 * S
    out.push(
      ev('tool_call', { tool: 'Agent', call_id: id, input: { description: `Old job ${b}`, prompt: 'Do it', subagent_type: 'general-purpose' } }, at),
      ev('subagent_started', { child_id: id, agent_type: 'general-purpose', model: '', summary: `Old job ${b}` }, at),
      ev('subagent_done', { child_id: id, agent_type: 'general-purpose', model: '', summary: 'running in the background', background: true }, at),
      ev('tool_result', { call_id: id, output: 'Async agent launched successfully.', is_error: false, duration_ms: 0 }, at),
    )
    const end = (b + 1) * BLOCK - (b === 5 ? TURN : 0)
    for (let i = 0; out.length < end; i++) {
      at += S
      const c = `${id}-c${i}`
      out.push(
        ev('tool_call', { tool: 'Bash', call_id: c, input: { command: 'make' }, parent_call_id: id }, at),
        ev('tool_result', { call_id: c, output: 'ok', is_error: false, duration_ms: 1, parent_call_id: id }, at),
      )
    }
    out.length = end
  }
  out.push(
    ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: {} }, NOW - 2 * H),
    ev('status_changed', { status: 'idle', reason: 'turn_done' }, NOW - 2 * H),
    ev('user_message', { text: 'go on', source: 'chat' }, NOW - 60 * S),
    ev('status_changed', { status: 'running', reason: 'turn' }, NOW - 60 * S),
    ev('tool_call', { tool: 'Read', call_id: 'r1', input: { file_path: 'a.ts' } }, NOW - 50 * S),
    ev('tool_result', { call_id: 'r1', output: 'x', is_error: false, duration_ms: 1 }, NOW - 49 * S),
    ev('assistant_text', { text: 'Reading done.', done: true }, NOW - 40 * S),
  )
  return out
}

// What the bottom of the chat says: the working line (or the waiting line, or the background line in
// its place), the toolbar's compact line, the "New output" pill. Clocks are left out.
function bottom(): string {
  const parts: string[] = []
  for (const id of ['working', 'working-compact', 'waiting', 'new-output']) {
    for (const el of document.querySelectorAll(`[data-testid="${id}"]`)) {
      parts.push(`${id}: ${(el.textContent ?? '').replace(/\d+:\d\d(:\d\d)?/g, '').replace(/\s+/g, ' ').trim()}`)
    }
  }
  return parts.join(' | ') || '(nothing)'
}

// Mounts the chat, feeds the tail as the server sends it, then every older page the transcript asks
// for; after every render, what the bottom says. Returns each distinct text with the page it came on
// (0: the tail).
async function load(status: SessionMeta['status'], events: AgentEvent[]) {
  const t: FakeTransport = createFakeTransport()
  let page = 0
  const seen: Array<{ page: number; text: string }> = []
  const look = () => {
    const text = bottom()
    if (seen.length === 0 || seen[seen.length - 1].text !== text) seen.push({ page, text })
  }
  render(
    <Profiler id="chat" onRender={look}>
      <ChatView session={SESSION} transport={t.transport} meta={meta({ status })} />
    </Profiler>,
  )
  const tail = events.slice(-TAIL)
  act(() => {
    for (const e of tail) t.emit(e)
    t.replayDone({ lastSeq: tail[tail.length - 1].seq, hasMore: false, older: true, hasOlder: true, firstSeq: tail[0].seq })
  })
  let first = events.length - TAIL
  while (first > 0) {
    // The sentinel is in view: it reveals more rows or asks for the next page.
    for (let k = 0; k < 4 && t.pendingOlder === 0; k++) act(() => intersect(true))
    expect(t.pendingOlder).toBe(1)
    const pageEvents = events.slice(Math.max(0, first - OLDER_PAGE), first)
    first -= pageEvents.length
    page++
    act(() => { vi.advanceTimersByTime(1500) })
    await act(async () => { t.answerOlder({ events: pageEvents, hasOlder: first > 0, firstSeq: pageEvents[0].seq ?? 0 }) })
  }
  look()
  return { t, seen, pages: page }
}

describe('the bottom of the chat while older pages come in', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    installIntersectionObserver()
    seq = 0
    Element.prototype.scrollIntoView = vi.fn()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('a running session: "Working…" from the tail to the last page, then the live call once it runs', async () => {
    const { t, seen, pages } = await load('running', history())
    expect(pages).toBeGreaterThan(5)
    expect(seen).toEqual([{ page: 0, text: '(nothing)' }, { page: 0, text: 'working: Working…' }])
    // Live again: a call starts and is named once it has run a while; its result clears it.
    act(() => { t.emit(ev('tool_call', { tool: 'Bash', call_id: 'live', input: { command: 'npm test' } }, NOW)) })
    act(() => { vi.advanceTimersByTime(4 * S) })
    expect(bottom()).toBe('working: Working… · waiting on Bash · npm test')
    act(() => { t.emit(ev('tool_result', { call_id: 'live', output: 'ok', is_error: false, duration_ms: 1 }, NOW + 4 * S)) })
    act(() => { vi.advanceTimersByTime(4 * S) })
    expect(bottom()).toBe('working: Working…')
  })

  it('an idle session: nothing at the bottom, from the tail to the last page', async () => {
    const { seen } = await load('idle', history())
    expect(seen).toEqual([{ page: 0, text: '(nothing)' }])
  })
})
