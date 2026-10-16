import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, renderHook, screen, within } from '@testing-library/react'
import ChatView from '../components/ChatView'
import AskReply from '../components/AskReply'
import { scrollToEvent } from '../components/scrollToCall'
import { useChatReportStore, useLiveState } from './useLiveState'
import { useTranscriptStore } from './useTranscriptStore'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, ingest, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

// useLiveState: the subagents still running and the asks still open, for a view beside the chat,
// agreeing with what the chat itself draws.

const S = 1000
const NOW = new Date(2026, 9, 14, 12, 0, 0).getTime()

let seq = 0
function ev(kind: string, payload: unknown, at = NOW - 60 * S): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, ts: new Date(at).toISOString(), kind, payload })
}
const userMsg = (at = NOW - 90 * S) => ev('user_message', { text: 'survey it', source: 'chat' }, at)
const agentCall = (id: string, description: string, at = NOW - 80 * S) =>
  ev('tool_call', { tool: 'Agent', call_id: id, input: { description, prompt: 'Look around', subagent_type: 'Explore', model: 'haiku' } }, at)
const subStarted = (id: string, at = NOW - 80 * S) => ev('subagent_started', { child_id: id, agent_type: 'Explore', model: '', summary: '' }, at)
const subUpdate = (id: string) => ev('subagent_update', { child_id: id, agent_type: 'Explore', model: 'claude-haiku-5', summary: '', usage: { input_tokens: 12000, output_tokens: 400, cache_read_tokens: 0, cache_write_tokens: 0 } })
const childCall = (id: string, parent: string) => ev('tool_call', { tool: 'Bash', call_id: id, input: { command: 'npm test' }, parent_call_id: parent })
const subDone = (id: string, background = false) => ev('subagent_done', { child_id: id, agent_type: 'Explore', model: '', summary: background ? 'launched' : 'done', ...(background ? { background: true } : {}) })
const result = (id: string) => ev('tool_result', { call_id: id, output: 'ok', is_error: false, duration_ms: 1 })
const ask = (body: string, messageId: string, at = NOW - 30 * S) => ev('ask', { body, message_id: messageId }, at)

function Probe() {
  const { subagents, openAsks, backgroundRunning, answer } = useLiveState(SESSION)
  return (
    <div data-testid="probe" data-bg={backgroundRunning}>
      <ul>
        {subagents.map(s => (
          <li key={s.key} data-testid="sub" data-key={s.key} data-bg={String(s.background)}>
            {[s.title, s.type, s.model, s.tokens, s.doing, s.startedAt].join('|')}
          </li>
        ))}
      </ul>
      <ul>
        {openAsks.map(a => (
          <li key={a.eventId} data-testid="ask" data-event={a.eventId}>
            <span data-testid="ask-text">{a.question}|{a.messageId}|{a.askedAt}</span>
            {answer && <AskReply answer={answer} messageId={a.messageId} label="Answer from the panel" />}
          </li>
        ))}
      </ul>
    </div>
  )
}

function page(status: SessionMeta['status'] = 'running', t: FakeTransport = createFakeTransport({ answersAsks: true })) {
  render(<><Probe /><ChatView session={SESSION} transport={t.transport} meta={meta({ status })} /></>)
  return t
}
const subs = () => screen.queryAllByTestId('sub')
const asks = () => screen.queryAllByTestId('ask')

describe('useLiveState', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })
  afterEach(() => { vi.useRealTimers() })

  it('lists a running subagent with its title, type, model, tokens, what it does and when it started', () => {
    seed(userMsg(), agentCall('A', 'Survey the repo'), subStarted('A'), subUpdate('A'), childCall('k1', 'A'))
    page('running')
    expect(subs()).toHaveLength(1)
    expect(subs()[0].dataset.key).toBe('A')
    expect(subs()[0].textContent).toBe(`Survey the repo|Explore|claude-haiku-5|12400|running Bash · npm test|${NOW - 80 * S}`)
  })

  it('drops a subagent once it is done, and keeps a background one while the session is live', () => {
    seed(userMsg(), agentCall('A', 'Survey the repo'), subStarted('A'), subDone('A'), result('A'),
      agentCall('B', 'Watch the build'), subStarted('B'), subDone('B', true), result('B'), ev('turn_done', { stop_reason: 'end_turn', model: 'm', usage: {} }))
    page('idle')
    expect(subs().map(s => [s.dataset.key, s.dataset.bg])).toEqual([['B', 'true']])
  })

  it('does not list a subagent started 13 hours ago with no end, nor count a stale background task', () => {
    const H = 3600 * S
    seed(userMsg(NOW - 13 * H), agentCall('A', 'Old survey', NOW - 13 * H), subStarted('A', NOW - 13 * H),
      agentCall('B', 'Fresh survey', NOW - H), subStarted('B', NOW - H),
      ev('tool_call', { tool: 'Bash', call_id: 'c1', input: { command: 'old', run_in_background: true } }, NOW - 13 * H),
      ev('background_task_started', { task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'Old', started_at: new Date(NOW - 13 * H).toISOString() }, NOW - 13 * H),
      ev('background_task_started', { task_id: 'b2', call_id: 'c2', kind: 'bash', description: 'Fresh', started_at: new Date(NOW - H).toISOString() }, NOW - H))
    page('running')
    expect(subs().map(s => s.dataset.key)).toEqual(['B'])
    expect(screen.getByTestId('probe').dataset.bg).toBe('1')
  })

  it('does not list a stale background subagent whose launch is its only end', () => {
    const H = 3600 * S
    seed(userMsg(NOW - 13 * H), agentCall('A', 'Old watcher', NOW - 13 * H), subStarted('A', NOW - 13 * H),
      ev('subagent_done', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'launched', background: true }, NOW - 13 * H), result('A'))
    page('idle')
    expect(subs()).toHaveLength(0)
  })

  it('says whether the initial history is loaded, and reads what is held either way', () => {
    function Settled() { return <span data-testid="settled">{String(useLiveState(SESSION).settled)}</span> }
    render(<Settled />)
    expect(screen.getByTestId('settled').textContent).toBe('false')
    act(() => { ingest(userMsg(), ask('Ship it?', 'm1')) })
    expect(screen.getByTestId('settled').textContent).toBe('false')
    act(() => { seed() })
    expect(screen.getByTestId('settled').textContent).toBe('true')
    act(() => { useTranscriptStore.getState().resetEvents(SESSION) })
    expect(screen.getByTestId('settled').textContent).toBe('false')
  })

  it('lists nothing before a chat has reported on the session, nor for an ended one', () => {
    seed(userMsg(), agentCall('A', 'Survey the repo'), subStarted('A'), ask('Ship it?', 'm1'))
    render(<Probe />)
    expect(subs()).toHaveLength(0)
    // The asks are read anyway: nothing has said they closed.
    expect(asks()).toHaveLength(1)
    expect(screen.queryByLabelText('Answer from the panel')).toBeNull()
  })

  it('a stopped session has no open asks and nothing running', () => {
    seed(userMsg(), agentCall('A', 'Survey the repo'), subStarted('A'), ask('Ship it?', 'm1'))
    page('stopped')
    expect(subs()).toHaveLength(0)
    expect(asks()).toHaveLength(0)
  })

  it('lists every open ask, oldest first, and counts the running background tasks', () => {
    seed(userMsg(), ask('Staging first?', 'm1', NOW - 50 * S), ask('Which region?', 'm2', NOW - 20 * S),
      ev('tool_call', { tool: 'Bash', call_id: 'c1', input: { command: 'make deploy', run_in_background: true } }),
      ev('background_task_started', { task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'Deploy', started_at: new Date(NOW - 10 * S).toISOString() }))
    page('waiting')
    expect(screen.getAllByTestId('ask-text').map(a => a.textContent)).toEqual([`Staging first?|m1|${NOW - 50 * S}`, `Which region?|m2|${NOW - 20 * S}`])
    expect(screen.getByTestId('probe').dataset.bg).toBe('1')
  })

  it('answers an ask from beside the chat through the chat, and the ask leaves the list', async () => {
    seed(userMsg(), ask('Staging first?', 'm1'))
    const t = page('waiting')
    const row = asks()[0]
    fireEvent.change(within(row).getByLabelText('Answer from the panel'), { target: { value: 'yes' } })
    await act(async () => { fireEvent.click(within(row).getByRole('button', { name: 'Answer' })) })
    expect(t.sentWith.map(s => [s.text, s.opts?.answerTo])).toEqual([['yes', 'm1']])
    act(() => t.emit(ev('user_message', { text: 'yes', source: 'ask_answer', message_id: 'm1', client_id: t.sentWith[0].opts!.clientId })))
    expect(asks()).toHaveLength(0)
  })

  it('belongs to one session: another session beside a busy one has nothing', () => {
    seed(userMsg(), agentCall('A', 'Survey the repo'), subStarted('A'), ask('Ship it?', 'm1'))
    page('running')
    function Other() {
      const o = useLiveState('s2')
      return <span data-testid="other">{o.subagents.length + o.openAsks.length + o.backgroundRunning}</span>
    }
    render(<Other />)
    expect(subs()).toHaveLength(1)
    expect(screen.getByTestId('other').textContent).toBe('0')
  })

  it('a chat that goes takes its report with it, so its answer can no longer be called', () => {
    seed(userMsg(), ask('Ship it?', 'm1'))
    const t = createFakeTransport({ answersAsks: true })
    const { unmount } = render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'waiting' })} />)
    expect(useChatReportStore.getState().reports[SESSION]?.answer).toBeTypeOf('function')
    unmount()
    expect(useChatReportStore.getState().reports[SESSION]).toBeUndefined()
  })

  it('scrollToEvent finds the ask card in the transcript', () => {
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled as unknown as Element['scrollIntoView']
    try {
      seed(userMsg(), ask('Staging first?', 'm1'))
      page('waiting')
      expect(scrollToEvent(asks()[0].dataset.event!)).toBe(true)
      expect((scrolled.mock.contexts.at(-1) as HTMLElement).dataset.testid).toBe('ask-card')
    } finally {
      delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView
    }
  })

  it('hands on the key of the Files list the chat reports, empty before any does', () => {
    const { result, rerender } = renderHook(() => useLiveState('s1'))
    expect(result.current.filesKey).toBe('')
    act(() => useChatReportStore.getState().setReport('s1', { inFlight: false, live: true, ended: false, closedAsks: new Map(), filesKey: 'a1,a2' }))
    rerender()
    expect(result.current.filesKey).toBe('a1,a2')
  })
})
