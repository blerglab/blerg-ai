import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import type { AgentEvent, SessionInfo } from '../types'

vi.mock('../ws', () => ({
  send: vi.fn(),
  onMessage: vi.fn(() => () => {}),
  onOpen: vi.fn(() => () => {}),
}))

import { send } from '../ws'

const base: SessionInfo = {
  id: 's1',
  daemon_id: 'd1',
  status: 'running',
  project_path: '/r/proj',
  repo: 'proj',
  title: 'Test',
  model: 'claude-sonnet-5',
  started_at: '2026-09-30T00:00:00Z',
  kind: 'agent',
}

let n = 0
function ev(seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  n++
  return {
    type: 'agent_event', session_id: 's1', client_event_id: `e${n}`, seq,
    ts: '2026-09-30T00:00:00Z', kind, payload,
  }
}

const sent = () => vi.mocked(send).mock.calls.map(c => c[0] as { type: string; text?: string })

function type(text: string) {
  const box = screen.getByRole('textbox') as HTMLTextAreaElement
  fireEvent.change(box, { target: { value: text } })
  return box
}

describe('AgentChatView queued messages and Esc', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
    const ingest = useAgentTranscript.getState().ingest
    ingest(ev(1, 'user_message', { text: 'start', source: 'chat' }))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows a message sent mid-turn as queued, clears the box, and swaps it for the real event', () => {
    render(<AgentChatView session={base} />)
    const box = type('and also check the tests')
    fireEvent.keyDown(box, { key: 'Enter' })

    expect(sent().some(m => m.type === 'agent_user_message' && m.text === 'and also check the tests')).toBe(true)
    expect(box.value).toBe('')
    expect(screen.getByTestId('queued-message').textContent).toContain('and also check the tests')
    expect(screen.getByTestId('queued-message').textContent).toMatch(/Queued/)
    // the message is already with the agent: it says when it will be picked up
    expect(screen.getByTestId('queued-message').textContent).toMatch(/next step/)

    // The turn picks it up: the real event replaces the queued bubble.
    act(() => {
      useAgentTranscript.getState().ingest(ev(9, 'user_message', { text: 'and also check the tests', source: 'chat' }))
    })
    expect(screen.queryByTestId('queued-message')).toBeNull()
    expect(screen.getAllByText('and also check the tests')).toHaveLength(1)
  })

  it('keeps two identical queued messages apart and resolves them one at a time', () => {
    render(<AgentChatView session={base} />)
    const box = type('ok')
    fireEvent.keyDown(box, { key: 'Enter' })
    type('ok')
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(screen.getAllByTestId('queued-message')).toHaveLength(2)

    act(() => { useAgentTranscript.getState().ingest(ev(10, 'user_message', { text: 'ok', source: 'chat' })) })
    expect(screen.getAllByTestId('queued-message')).toHaveLength(1)
    act(() => { useAgentTranscript.getState().ingest(ev(11, 'user_message', { text: 'ok', source: 'chat' })) })
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('shows a message sent to an idle or disconnected session at once, as sending, but not slash commands', () => {
    render(<AgentChatView session={{ ...base, status: 'disconnected' }} />)
    const box = type('hello')
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(screen.getByTestId('queued-message').textContent).toContain('hello')
    expect(screen.getByTestId('queued-message').textContent).toMatch(/Sending/)
    expect(screen.getByTestId('queued-message').textContent).not.toMatch(/Queued/)
    // the real event replaces it
    act(() => { useAgentTranscript.getState().ingest(ev(9, 'user_message', { text: 'hello', source: 'chat' })) })
    expect(screen.queryByTestId('queued-message')).toBeNull()

    const box2 = screen.getByRole('textbox')
    fireEvent.change(box2, { target: { value: '/model haiku' } })
    fireEvent.keyDown(box2, { key: 'Enter' })
    expect(screen.queryByTestId('queued-message')).toBeNull()
  })

  it('keeps a message that was never picked up on screen, marked as not delivered, when the session fails', () => {
    const { rerender } = render(<AgentChatView session={base} />)
    const box = type('one more thing')
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(screen.getByTestId('queued-message')).toBeTruthy()
    rerender(<AgentChatView session={{ ...base, status: 'error' }} />)
    expect(screen.getByTestId('queued-message').textContent).toMatch(/Not delivered/)
    expect(screen.getByTestId('queued-message').textContent).toContain('one more thing')
  })

  it('Esc interrupts a running turn, and does nothing when idle', () => {
    const { unmount } = render(<AgentChatView session={base} />)
    const box = screen.getByRole('textbox')
    fireEvent.keyDown(box, { key: 'Escape' })
    expect(sent().some(m => m.type === 'interrupt_session')).toBe(true)
    unmount()

    vi.clearAllMocks()
    render(<AgentChatView session={{ ...base, status: 'idle' }} />)
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })
    expect(sent().some(m => m.type === 'interrupt_session')).toBe(false)
  })

  it('tells you Enter sends and Esc interrupts while it works, and not when idle', () => {
    const { unmount } = render(<AgentChatView session={base} />)
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Enter sends/)
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Esc interrupts/)
    unmount()
    render(<AgentChatView session={{ ...base, status: 'idle' }} />)
    expect(screen.queryByTestId('composer-hint')).toBeNull()
  })

  it('does not promise steering on an engine that holds the message until the turn ends', () => {
    render(<AgentChatView session={{ ...base, engine: 'codex' }} />)
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Enter queues/)
    const box = type('later')
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(screen.getByTestId('queued-message').textContent).not.toMatch(/next step/)
  })

  it('puts Attach beside Send, after the text box', () => {
    render(<AgentChatView session={{ ...base, status: 'idle' }} />)
    const attach = screen.getByRole('button', { name: 'Attach files' })
    const send = screen.getByRole('button', { name: 'Send' })
    const box = screen.getByRole('textbox')
    expect(attach.parentElement).toBe(send.parentElement)
    expect(attach.parentElement?.classList.contains('agent-composer-actions')).toBe(true)
    // textarea first, then the Attach and Send buttons
    expect(box.compareDocumentPosition(attach) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(attach.compareDocumentPosition(send) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('says there is no pod when a cluster session is disconnected, and what a message will do', () => {
    render(<AgentChatView session={{ ...base, status: 'disconnected', runtime: 'cluster' }} />)
    const banner = screen.getByTestId('offline-banner')
    expect(banner.textContent).toMatch(/No pod is running/)
    expect(banner.textContent).toMatch(/new pod will start/)
    expect(banner.textContent).toMatch(/uncommitted changes are not/)
  })

  it('words the banner for a machine session differently, and shows none for a live session', () => {
    const { unmount } = render(<AgentChatView session={{ ...base, status: 'disconnected', runtime: 'daemon' }} />)
    expect(screen.getByTestId('offline-banner').textContent).toMatch(/disconnected from its machine/)
    unmount()
    render(<AgentChatView session={{ ...base, status: 'idle' }} />)
    expect(screen.queryByTestId('offline-banner')).toBeNull()
  })

  it('draws a sent-but-unrecorded message as a quiet bubble with a spinner and a status line', () => {
    const { container } = render(<AgentChatView session={{ ...base, status: 'disconnected' }} />)
    const box = type('first')
    fireEvent.keyDown(box, { key: 'Enter' })
    const bubble = screen.getByTestId('queued-message')
    expect(bubble.classList.contains('sending')).toBe(true)
    expect(bubble.querySelector('.queue-spinner')).not.toBeNull()
    expect(bubble.querySelector('.card-author')?.textContent).toBe('You')
    expect(bubble.querySelector('.queue-status')?.textContent).toMatch(/Sending/)
    expect(container.querySelector('.agent-card.user.queued')).not.toBeNull()
  })

  it('numbers the queue when several messages wait behind a running turn', () => {
    render(<AgentChatView session={base} />)
    const box = type('one')
    fireEvent.keyDown(box, { key: 'Enter' })
    type('two')
    fireEvent.keyDown(box, { key: 'Enter' })
    type('three')
    fireEvent.keyDown(box, { key: 'Enter' })
    const statuses = screen.getAllByTestId('queued-message').map(b => b.querySelector('.queue-status')?.textContent)
    expect(statuses).toEqual(['Queued · 1 of 3', 'Queued · 2 of 3', 'Queued · 3 of 3'])
  })

  it('marks a message the session never picked up with a warning and no spinner', () => {
    const { rerender } = render(<AgentChatView session={base} />)
    const box = type('lost')
    fireEvent.keyDown(box, { key: 'Enter' })
    rerender(<AgentChatView session={{ ...base, status: 'error' }} />)
    const bubble = screen.getByTestId('queued-message')
    expect(bubble.classList.contains('failed')).toBe(true)
    expect(bubble.querySelector('.queue-spinner')).toBeNull()
    expect(bubble.querySelector('.queue-status')?.textContent).toMatch(/Not delivered/)
  })

  it('keeps every toolbar button nameable and iconised so it can collapse to icons on a narrow screen', () => {
    const { container } = render(<AgentChatView session={base} />)
    for (const name of ['Rules', /Skills/, /Files/, 'Stop']) {
      const btn = screen.getByRole('button', { name })
      expect(btn.querySelector('.tb-icon')).not.toBeNull()
      expect(btn.querySelector('.tb-label')).not.toBeNull()
    }
    const compact = container.querySelector('.compact-toggle')
    expect(compact?.querySelector('.tb-icon')).not.toBeNull()
    expect(screen.getByRole('checkbox', { name: 'Compact tool calls' })).toBeTruthy()
  })

  it('says a stopped session has ended, says why, and locks the composer', () => {
    render(<AgentChatView session={{ ...base, status: 'stopped', end_reason: 'stopped_by_user', ended_by: { kind: 'human', self: true } }} />)
    const banner = screen.getByTestId('ended-banner')
    expect(banner.textContent).toMatch(/This session has ended/)
    expect(banner.textContent).toMatch(/Ended by you/)
    expect(banner.textContent).toMatch(/start a new session/)
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).disabled).toBe(true)
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).placeholder).toMatch(/has ended/)
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('still says it ended when no reason was recorded, and shows nothing for a live session', () => {
    const { unmount } = render(<AgentChatView session={{ ...base, status: 'ended' }} />)
    expect(screen.getByTestId('ended-banner').textContent).toMatch(/This session has ended/)
    unmount()
    render(<AgentChatView session={{ ...base, status: 'idle' }} />)
    expect(screen.queryByTestId('ended-banner')).toBeNull()
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).disabled).toBe(false)
  })
})
