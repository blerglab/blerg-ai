// BLG-757: runner's real chat/transport; the server processes socket requests serially.
// Keep the history query unresolved to prove return catch-up does not wait behind it.
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import { ChatView, useTranscriptStore } from '@blerglab/chat'
import { blergTransport, invalidateChatTransport } from '../lib/chatTransport'
import * as ws from '../test/wsMock'
import { ev, meta, resetChatState, SESSION } from '../../../../packages/chat/src/test/chat'
import { installIntersectionObserver, intersect } from '../../../../packages/chat/src/test/intersection'
import '@blerglab/chat/blerg.css'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => true }))
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })

it.each([390, 3840])('shows cached text and reconciles the missing tail within one second at %i px', async width => {
  await page.viewport(width, 900)
  resetChatState()
  ws.resetWsMock()
  ws.send.mockReturnValue(true)
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
  const store = useTranscriptStore.getState()
  store.ingest(ev({ seq: 800, payload: {text: 'Cached conversation', source: 'chat'} }))
  store.ingest(ev({seq: 799, kind: 'background_task_started', payload: {task_id: 'deploy', call_id: 'deploy-call', kind: 'bash', description: 'Wait for the deploy', started_at: new Date().toISOString()}}))
  store.ingestReplayDone(SESSION, {lastSeq: 800, hasMore: false, older: true, hasOlder: false, firstSeq: 800})
  const transport = blergTransport()
  const view = (id: string) => <div style={{height: '100vh'}}><ChatView session={id} transport={transport} meta={meta({status: 'idle'})} /></div>
  const mounted = render(view(SESSION))
  act(() => ws.emit({type: 'agent_events_replay_done', session_id: SESSION, last_seq: 800, has_more: false}))
  expect(screen.getByTestId('working')).toHaveTextContent('Waiting on a background task')
  const input = screen.getByRole('textbox')
  fireEvent.change(input, {target: {value: 'Please continue'}})
  await act(async () => fireEvent.keyDown(input, {key: 'Enter'}))
  expect(screen.getByTestId('queued-message')).toHaveTextContent(/Sending/)
  const sent = ws.send.mock.calls.map(([m]) => m).find(m => m.type === 'agent_user_message')!
  if (sent.type !== 'agent_user_message') throw new Error('Missing client message')
  mounted.rerender(view('workspace-b'))
  // A has older history, and receives a receipt, final reply and turn end while away.
  useTranscriptStore.setState(s => ({sessions: {...s.sessions, [SESSION]: {...s.sessions[SESSION], hasOlder: true}}}))
  const tail = [
    ev({seq: 801, kind: 'user_message', payload: {text: 'Please continue', source: 'chat', client_id: sent.client_id}}),
    ev({seq: 802, kind: 'assistant_text', payload: {text: 'Newest reply while away', done: true}}),
    ev({seq: 803, kind: 'background_task_done', payload: {task_id: 'deploy', call_id: 'deploy-call', kind: 'bash', status: 'completed', summary: 'Deployed'}}),
    ev({seq: 804, kind: 'turn_done', payload: {model: 'claude-sonnet-5', stop_reason: 'end_turn', usage: {input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0}}}),
  ]
  const requestOrder: Array<'tail' | 'history'> = []
  let historyBlocked = false
  let requestedAfter: number | undefined
  ws.send.mockImplementation(message => {
    if (message.type === 'subscribe_agent_events' && message.session_id === SESSION) {
      requestOrder.push(message.before_seq ? 'history' : 'tail')
      if (message.before_seq) historyBlocked = true
      else if (!historyBlocked) {
        requestedAfter = message.after_seq
        // Real socket responses are asynchronous. The old implementation queues a history
        // query first, which prevents this response until that slow query completes.
        queueMicrotask(() => {
          for (const event of tail) ws.emit(event)
          ws.emit({type: 'agent_events_replay_done', session_id: SESSION, last_seq: 804, has_more: false})
        })
      }
    }
    return true
  })
  const started = performance.now()
  mounted.rerender(view(SESSION))
  expect(screen.getByText('Cached conversation')).toBeVisible()
  await waitFor(() => expect(screen.getByText('Newest reply while away')).toBeVisible(), {timeout: 1000})
  await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  const latency = performance.now() - started
  console.log(`BLG-757 ${width}px switch-back latency: ${latency.toFixed(1)} ms`)
  expect(latency).toBeLessThan(1000)
  expect(requestedAfter).toBe(800)
  expect(screen.queryByTestId('queued-message')).toBeNull()
  expect(screen.queryByTestId('working')).toBeNull()
  expect(requestOrder[0]).toBe('tail')
  const firstHistory = requestOrder.indexOf('history')
  if (firstHistory !== -1) expect(firstHistory).toBeGreaterThan(requestOrder.indexOf('tail'))
  act(() => ws.setSocketOpen(false))
})


it('resolves Sending on return when the server never received the message', async () => {
  resetChatState()
  ws.resetWsMock()
  ws.send.mockReturnValue(true)
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
  useTranscriptStore.getState().ingest(ev({seq: 1, payload: {text: 'Cached before send', source: 'chat'}}))
  useTranscriptStore.getState().ingestReplayDone(SESSION, {lastSeq: 1, hasMore: false})
  const transport = blergTransport()
  const view = (id: string) => <ChatView session={id} transport={transport} meta={meta({status: 'idle'})} />
  const mounted = render(view(SESSION))
  act(() => ws.emit({type: 'agent_events_replay_done', session_id: SESSION, last_seq: 1, has_more: false}))
  const box = screen.getByRole('textbox')
  fireEvent.change(box, {target: {value: 'Lost before server receipt'}})
  await act(async () => fireEvent.keyDown(box, {key: 'Enter'}))
  expect(screen.getByTestId('queued-message')).toHaveTextContent(/Sending/)
  mounted.rerender(view('workspace-b'))
  ws.send.mockImplementation(message => {
    if (message.type === 'subscribe_agent_events' && message.session_id === SESSION) queueMicrotask(() => {
      ws.emit({type: 'agent_events_replay_done', session_id: SESSION, last_seq: 1, has_more: false})
    })
    return true
  })
  mounted.rerender(view(SESSION))
  await waitFor(() => expect(screen.getByTestId('queued-message')).toHaveTextContent(/May not have been delivered/), {timeout: 1000})
  expect(screen.getByTestId('queued-message')).not.toHaveTextContent(/Sending/)
  expect(ws.send.mock.calls.filter(([message]) => message.type === 'agent_user_message')).toHaveLength(1)
  act(() => ws.setSocketOpen(false))
})


it('serves a top-sentinel history request after an empty return catch-up', async () => {
  resetChatState()
  ws.resetWsMock()
  ws.send.mockReturnValue(true)
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
  installIntersectionObserver()
  useTranscriptStore.getState().ingest(ev({seq: 1, payload: {text: 'Short cached transcript', source: 'chat'}}))
  useTranscriptStore.getState().ingestReplayDone(SESSION, {lastSeq: 1, hasMore: false, older: true, hasOlder: false, firstSeq: 1})
  const transport = blergTransport()
  const view = (id: string) => <ChatView session={id} transport={transport} meta={meta({status: 'idle'})} />
  const mounted = render(view(SESSION))
  act(() => ws.emit({type: 'agent_events_replay_done', session_id: SESSION, last_seq: 1, has_more: false}))
  mounted.rerender(view('workspace-b'))
  useTranscriptStore.setState(s => ({sessions: {...s.sessions, [SESSION]: {...s.sessions[SESSION], hasOlder: true}}}))
  ws.send.mockClear()
  mounted.rerender(view(SESSION))
  const olderRequests = () => ws.send.mock.calls.filter(([message]) => message.type === 'subscribe_agent_events' && message.session_id === SESSION && message.before_seq)
  // The top sentinel asks while the forward replay is still in flight. No newer
  // events arrive, so askedAt/heldCount will not change to provoke another request.
  act(() => intersect())
  expect(olderRequests()).toHaveLength(0)
  act(() => ws.emit({type: 'agent_events_replay_done', session_id: SESSION, last_seq: 1, has_more: false}))
  await waitFor(() => expect(olderRequests()).toHaveLength(1), {timeout: 1000})
  expect(document.querySelector('.agent-timeline')).toHaveClass('backfilling')
  act(() => intersect())
  expect(olderRequests()).toHaveLength(1)
  act(() => ws.setSocketOpen(false))
})
