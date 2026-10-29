import { beforeEach, expect, it } from 'vitest'
import { act, render, screen, within, waitFor } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'
import { useTranscriptStore } from '../hooks/useTranscriptStore'

beforeEach(resetChatState)
it('shows a parent-upload note without answering an ask or removing live reply text', () => {
  seed(ev({ seq: 1, client_event_id: 'ask', kind: 'ask', payload: { body: 'Approve?', message_id: 'question' } }))
  const t = createFakeTransport({ answersAsks: true })
  render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'running' })} />)
  act(() => useTranscriptStore.getState().appendStreaming(SESSION, 'Unfinished root reply'))
  act(() => t.emit(ev({ seq: 2, client_event_id: 'upload', kind: 'session_upload', payload: { name: 'input.md', size: 42, artifact_id: 'file-id', from_session_id: 'parent', from_title: 'Parent task' } })))
  const note = screen.getByTestId('session-upload')
  expect(note).toHaveTextContent('from Parent task')
  expect(note).toHaveTextContent('input.md')
  expect(note.querySelector('svg')).toHaveAttribute('data-file-icon', 'markdown')
  expect(screen.getByTestId('streaming')).toHaveTextContent('Unfinished root reply')
  expect(within(screen.getByTestId('ask-card')).getByLabelText('Your answer')).toBeInTheDocument()
  expect(t.sentWith).toHaveLength(0)
})
it('renders untrusted origin and file names as text without executable markup or links', () => {
  seed(ev({ seq: 1, kind: 'session_upload', payload: { name: '<img src=x onerror=alert(1)>', size: 1, artifact_id: 'file', from_session_id: 'parent', from_title: '[Parent](javascript:alert(1))' } }))
  render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} />)
  const note = screen.getByTestId('session-upload')
  expect(note).toHaveTextContent('<img src=x onerror=alert(1)>')
  expect(note).toHaveTextContent('[Parent](javascript:alert(1))')
  expect(note.querySelector('img, a, script')).toBeNull()
})
it('refreshes the session file list when a parent uploads without sending a chat message', async () => {
  const t = createFakeTransport()
  render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
  await waitFor(() => expect(t.files?.listCalls).toBe(1))
  act(() => t.emit(ev({ seq: 1, client_event_id: 'upload', kind: 'session_upload', payload: { name: 'input.md', size: 42, artifact_id: 'file-id', from_session_id: 'parent', from_title: 'Parent task' } })))
  await waitFor(() => expect(t.files?.listCalls).toBe(2))
})
