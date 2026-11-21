import { beforeEach, expect, it } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ChatView, useChatReportStore } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import { ev, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'

beforeEach(resetChatState)
it.each([390, 3840])('shows a stopped background wait without overflow at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const ts = new Date(Date.now() - 5000).toISOString()
  seed(ev({ seq: 1, kind: 'user_message', ts, payload: { text: 'Run focused tests', source: 'chat' } }),
    ev({ seq: 2, kind: 'background_task_started', ts, payload: { task_id: 'fixture-task', call_id: 'fixture-call', kind: 'bash', description: 'Run focused tests', started_at: ts } }),
    ev({ seq: 3, kind: 'turn_done', ts, payload: { stop_reason: 'end_turn', model: 'm', usage: {} } }))
  const fake = createFakeTransport()
  render(<div style={{ height: '100vh', width: '100%', minWidth: 0 }}><ChatView session={SESSION} transport={fake.transport} meta={meta({ status: 'idle' })} /></div>)
  await screen.findByTestId('working')
  expect(useChatReportStore.getState().reports[SESSION]?.busy).toBe(true)
  await act(async () => { await useChatReportStore.getState().reports[SESSION]?.interrupt?.() })
  expect(fake.interrupts).toBe(1)
  // Sending Stop alone cannot claim success: the daemon acknowledges its wait.
  expect(screen.getByTestId('working-words')).toHaveTextContent('Waiting on a background task')
  act(() => fake.emit(ev({ seq: 4, kind: 'stop_result', payload: {state:'stopping',message:'Stopping the current work.'} })))
  expect(screen.getByTestId('stop-result')).toHaveTextContent('Stopping the current work.')
  act(() => fake.emit(ev({ seq: 5, kind: 'background_wait_stopped', payload: {} })))
  expect(screen.getByTestId('working-words')).toHaveTextContent('Stopped; background work may still be running')
  expect(useChatReportStore.getState().reports[SESSION]?.busy).toBe(false)
  const line = screen.getByTestId('working')
  expect(line.classList.contains('static')).toBe(true)
  expect(line.scrollWidth).toBeLessThanOrEqual(line.clientWidth)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  act(() => fake.emit(ev({ seq: 6, kind: 'background_task_done', payload: { task_id: 'fixture-task', call_id: 'fixture-call', kind: 'bash', status: 'stopped' } })))
  expect(screen.queryByTestId('working')).toBeNull()
})

it.each([390, 3840])('stops current work without a browser turn identity at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  seed(ev({ seq: 1, client_event_id: 'first-running', kind: 'status_changed', payload: { status: 'running', reason: 'turn' } }))
  const fake = createFakeTransport()
  const calls: Array<string | undefined> = []
  fake.transport.interrupt = async (_id, target) => { calls.push(target) }
  render(<div style={{ height: '100vh', width: '100%', minWidth: 0 }}><ChatView session={SESSION} transport={fake.transport} meta={meta({ status: 'running' })} /></div>)
  await screen.findByTestId('working')
  const pressed = useChatReportStore.getState().reports[SESSION]!.interrupt!
  await act(async () => pressed())
  act(() => {
    fake.emit(ev({ seq: 2, kind: 'status_changed', payload: { status: 'idle', reason: 'turn_done' } }))
    fake.emit(ev({ seq: 3, client_event_id: 'next-running', kind: 'status_changed', payload: { status: 'running', reason: 'turn' } }))
  })
  await act(async () => pressed())
  await act(async () => useChatReportStore.getState().reports[SESSION]!.interrupt!())
  expect(calls).toEqual([undefined, undefined, undefined])
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
