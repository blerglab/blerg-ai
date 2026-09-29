import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import type { AgentEvent, SessionInfo } from '../types'

vi.mock('../ws', () => ({
  send: vi.fn(),
  onMessage: vi.fn(() => () => {}),
  onOpen: vi.fn(() => () => {}),
}))

const KEY = 'blerg.agent.compactTools'

const session: SessionInfo = {
  id: 's1',
  daemon_id: 'd1',
  status: 'idle',
  project_path: '/r/proj',
  repo: 'proj',
  title: 'Test',
  model: 'claude-sonnet-5',
  started_at: '2026-07-26T00:00:00Z',
  kind: 'agent',
}

let seq = 0
function ev(kind: string, payload: unknown): AgentEvent {
  seq++
  return {
    type: 'agent_event',
    session_id: 's1',
    client_event_id: `e${seq}`,
    seq,
    ts: '2026-07-26T00:00:00Z',
    kind,
    payload,
  } as AgentEvent
}

function ingest(...events: AgentEvent[]) {
  const i = useAgentTranscript.getState().ingest
  for (const e of events) i(e)
}

function call(id: string, tool: string, input: unknown) {
  return ev('tool_call', { tool, call_id: id, input })
}

function result(id: string, output = 'ok', is_error = false, duration_ms = 100) {
  return ev('tool_result', { call_id: id, output, is_error, duration_ms })
}

// n finished Bash calls named c<from>..c<from+n-1>.
function bashRun(n: number, from = 1) {
  const out: AgentEvent[] = []
  for (let i = from; i < from + n; i++) {
    out.push(call(`c${i}`, 'Bash', { command: `echo ${i}` }), result(`c${i}`))
  }
  return out
}

describe('AgentChatView compact tool calls', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    seq = 0
    localStorage.removeItem(KEY)
    useAgentTranscript.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.removeItem(KEY)
  })

  it('renders one or two calls as inline rows and three or more as one summary', () => {
    ingest(...bashRun(2))
    const { rerender } = render(<AgentChatView session={session} />)
    expect(screen.getByTestId('tool-inline')).toBeInTheDocument()
    expect(screen.getAllByTestId('tool-row')).toHaveLength(2)
    expect(screen.queryByTestId('tool-group')).not.toBeInTheDocument()
    expect(screen.queryByTestId('tool-card')).not.toBeInTheDocument()

    act(() => ingest(...bashRun(1, 3)))
    rerender(<AgentChatView session={session} />)
    expect(screen.getByTestId('tool-group')).toBeInTheDocument()
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
  })

  it('splits groups at assistant text and other cards, but not at hidden kinds', () => {
    ingest(
      ...bashRun(2),
      ev('status_changed', { status: 'running', reason: 'turn' }),
      ev('provider_blocks', {}),
      ev('capabilities', { engine: 'claude', groups: [] }),
      ev('start_stage', { stages: [] }),
      ev('user_message', { text: 'reminder', source: 'system' }),
      ...bashRun(2, 3),
      ev('assistant_text', { text: 'in the middle', done: true }),
      ...bashRun(3, 5),
      ev('error', { message: 'boom', retryable: false }),
      ...bashRun(1, 8),
    )
    render(<AgentChatView session={session} />)
    // 4 calls before the text (one group), 3 after (one group), 1 after the error.
    expect(screen.getAllByTestId('tool-group')).toHaveLength(2)
    expect(screen.getByText(/Ran 4 tool calls/)).toBeInTheDocument()
    expect(screen.getByText(/Ran 3 tool calls/)).toBeInTheDocument()
    expect(screen.getAllByTestId('tool-inline')).toHaveLength(1)
    expect(screen.getByText('in the middle')).toBeInTheDocument()
  })

  it('summarises counts by tool name, most used first', () => {
    ingest(
      call('a', 'Read', { file_path: 'src/App.tsx' }), result('a'),
      call('b', 'Bash', { command: 'ls' }), result('b'),
      call('c', 'Bash', { command: 'pwd' }), result('c'),
      call('d', 'Edit', { file_path: 'x.go' }), result('d', 'ok', false, 900),
      call('e', 'Bash', { command: 'make' }), result('e', 'ok', false, 1400),
    )
    render(<AgentChatView session={session} />)
    const toggle = screen.getByTestId('tool-group-toggle')
    expect(toggle).toHaveTextContent('Ran 5 tool calls · Bash ×3, Read ×1, Edit ×1')
    // Total duration: 100 + 100 + 100 + 900 + 1400 = 2.6s.
    expect(toggle).toHaveTextContent('2.6s')
    expect(toggle).toHaveTextContent('✓')
    expect(screen.getByTestId('tool-preview')).toHaveTextContent('$ make')
  })

  it('previews the latest call for each kind of tool', () => {
    ingest(...bashRun(2), call('r', 'Read', { file_path: 'src/App.tsx' }), result('r'))
    render(<AgentChatView session={session} />)
    expect(screen.getByTestId('tool-preview').textContent).toBe('Read src/App.tsx')
  })

  it('shows a failure badge and reveals the failing line without expanding', () => {
    ingest(
      ...bashRun(2),
      call('f1', 'Bash', { command: 'npm test' }), result('f1', 'FAIL src/a.test.ts\nmore detail', true),
      call('f2', 'Bash', { command: 'go vet' }), result('f2', 'vet: nope', true),
      ...bashRun(1, 10),
    )
    render(<AgentChatView session={session} />)
    expect(screen.getByTestId('tool-fail-badge')).toHaveTextContent('✗ 2 failed')
    const lines = screen.getAllByTestId('tool-fail')
    expect(lines).toHaveLength(2)
    expect(lines[0]).toHaveTextContent('FAIL src/a.test.ts')
    expect(lines[0]).not.toHaveTextContent('more detail')
    expect(screen.getByTestId('tool-group-toggle')).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
  })

  it('shows the running call in the preview with a spinner', () => {
    ingest(...bashRun(3), call('run', 'Bash', { command: 'npm test -- --run' }))
    render(<AgentChatView session={{ ...session, status: 'running' }} />)
    expect(screen.getByTestId('tool-preview')).toHaveTextContent('$ npm test -- --run')
    expect(within(screen.getByTestId('tool-group-toggle')).getByRole('img', { name: 'running' })).toBeInTheDocument()
  })

  it('does not spin for a call with no result once the session has stopped', () => {
    ingest(...bashRun(3), call('run', 'Bash', { command: 'npm test' }))
    render(<AgentChatView session={session} />)
    expect(screen.queryByRole('img', { name: 'running' })).not.toBeInTheDocument()
  })

  it('expands on click and on the keyboard, showing input and output', async () => {
    const user = userEvent.setup()
    ingest(...bashRun(3))
    render(<AgentChatView session={session} />)
    const toggle = screen.getByTestId('tool-group-toggle')
    toggle.focus()
    await user.keyboard('{Enter}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getAllByTestId('tool-row')).toHaveLength(3)
    await user.keyboard(' ')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)

    fireEvent.click(toggle)
    const first = screen.getAllByTestId('tool-row')[0]
    expect(first).toHaveTextContent('Bash')
    expect(first).toHaveTextContent('$ echo 1')
    expect(first).toHaveTextContent('100ms')
    fireEvent.click(within(first).getByRole('button'))
    expect(within(first).getByTestId('tool-detail')).toHaveTextContent('"command": "echo 1"')
    expect(within(first).getByTestId('tool-detail')).toHaveTextContent('result (100ms)')
  })

  it('keeps an expanded group expanded, and an untouched one collapsed, as events arrive', () => {
    ingest(...bashRun(2))
    const { rerender } = render(<AgentChatView session={session} />)
    // Grows from two calls (inline) to three (summary) without a reset.
    act(() => ingest(...bashRun(1, 3)))
    rerender(<AgentChatView session={session} />)
    expect(screen.getByTestId('tool-group-toggle')).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(screen.getByTestId('tool-group-toggle'))
    const row = screen.getAllByTestId('tool-row')[1]
    fireEvent.click(within(row).getByRole('button'))
    expect(within(row).getByTestId('tool-detail')).toBeInTheDocument()

    act(() => ingest(...bashRun(2, 4)))
    rerender(<AgentChatView session={session} />)
    expect(screen.getByTestId('tool-group-toggle')).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getAllByTestId('tool-row')).toHaveLength(5)
    expect(within(screen.getAllByTestId('tool-row')[1]).getByTestId('tool-detail')).toBeInTheDocument()
    expect(screen.getByTestId('tool-group-toggle')).toHaveTextContent('Ran 5 tool calls')

    // A separate group the user never touched stays collapsed.
    act(() => ingest(ev('assistant_text', { text: 'next', done: true }), ...bashRun(3, 20)))
    rerender(<AgentChatView session={session} />)
    const toggles = screen.getAllByTestId('tool-group-toggle')
    expect(toggles[0]).toHaveAttribute('aria-expanded', 'true')
    expect(toggles[1]).toHaveAttribute('aria-expanded', 'false')
  })

  it('caps very long output and reveals it with Show all', () => {
    const long = Array.from({ length: 100 }, (_, i) => `line ${i}`).join('\n')
    ingest(call('big', 'Bash', { command: 'seq' }), result('big', long))
    render(<AgentChatView session={session} />)
    fireEvent.click(within(screen.getByTestId('tool-row')).getByRole('button'))
    const detail = screen.getByTestId('tool-detail')
    expect(detail).toHaveTextContent('line 39')
    expect(detail).not.toHaveTextContent('line 40')
    fireEvent.click(screen.getByRole('button', { name: 'Show all 100 lines' }))
    expect(screen.getByTestId('tool-detail')).toHaveTextContent('line 99')
    expect(screen.queryByRole('button', { name: /Show all/ })).not.toBeInTheDocument()
  })

  it('survives unknown tools and inputs that are not objects', () => {
    ingest(
      call('1', 'mystery_tool', null), result('1'),
      call('2', 'codex_exec', { command: ['bash', '-lc', 'ls -la'] }), result('2'),
      call('3', 'weird', 42), result('3', 'ok'),
      call('4', 'hermes', 'plain string input'), result('4'),
      call('5', 'odd', { a: { b: 1 }, list: [1, 2] }), result('5'),
      ev('tool_call', { call_id: 'no-tool' }),
      ev('tool_call', null),
    )
    render(<AgentChatView session={session} />)
    const toggle = screen.getByTestId('tool-group-toggle')
    fireEvent.click(toggle)
    const rows = screen.getAllByTestId('tool-row')
    expect(rows).toHaveLength(7)
    expect(rows[1]).toHaveTextContent('$ bash -lc ls -la')
    expect(rows[3]).toHaveTextContent('plain string input')
    rows.forEach(r => fireEvent.click(within(r).getByRole('button')))
    expect(screen.getAllByTestId('tool-detail')).toHaveLength(7)
  })

  it('never renders tool output as HTML', () => {
    ingest(call('x', 'Bash', { command: '<img src=x onerror=alert(1)>' }), result('x', '<b>bold</b>'))
    const { container } = render(<AgentChatView session={session} />)
    fireEvent.click(within(screen.getByTestId('tool-row')).getByRole('button'))
    expect(container.querySelector('.tcg-detail b')).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('.tcg-out')?.textContent).toContain('<b>bold</b>')
  })

  it('keeps publish tools as full cards that end a group', () => {
    ingest(
      ...bashRun(3),
      call('m', 'push_mockup', {}), result('m', 'published: https://example.test/m'),
      ...bashRun(1, 9),
    )
    render(<AgentChatView session={session} />)
    expect(screen.getByTestId('mockup-link')).toHaveAttribute('href', 'https://example.test/m')
    expect(screen.getAllByTestId('tool-group')).toHaveLength(1)
    expect(screen.getAllByTestId('tool-inline')).toHaveLength(1)
  })

  it('with the toggle off renders the legacy cards, and remembers the choice', () => {
    ingest(...bashRun(4))
    const { unmount } = render(<AgentChatView session={session} />)
    const box = screen.getByLabelText('Compact tool calls') as HTMLInputElement
    expect(box.checked).toBe(true)
    fireEvent.click(box)
    expect(screen.getAllByTestId('tool-card')).toHaveLength(4)
    expect(screen.queryByTestId('tool-group')).not.toBeInTheDocument()
    expect(localStorage.getItem(KEY)).toBe('0')
    unmount()

    render(<AgentChatView session={session} />)
    expect((screen.getByLabelText('Compact tool calls') as HTMLInputElement).checked).toBe(false)
    expect(screen.getAllByTestId('tool-card')).toHaveLength(4)
    fireEvent.click(screen.getByLabelText('Compact tool calls'))
    expect(screen.getByTestId('tool-group')).toBeInTheDocument()
    expect(localStorage.getItem(KEY)).toBe('1')
  })

  it('still works when localStorage throws', () => {
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    try {
      ingest(...bashRun(3))
      render(<AgentChatView session={session} />)
      expect(screen.getByTestId('tool-group')).toBeInTheDocument()
      fireEvent.click(screen.getByLabelText('Compact tool calls'))
      expect(screen.getAllByTestId('tool-card')).toHaveLength(3)
    } finally {
      get.mockRestore()
      set.mockRestore()
    }
  })

  it('renders a 2,000-event transcript as summary rows only, quickly', () => {
    const events: AgentEvent[] = []
    for (let g = 0; g < 50; g++) {
      events.push(ev('assistant_text', { text: `step ${g}`, done: true }))
      events.push(...bashRun(20, g * 20 + 1))
    }
    expect(events.length).toBeGreaterThan(2000)
    ingest(...events)
    const t0 = performance.now()
    render(<AgentChatView session={session} />)
    const elapsed = performance.now() - t0
    expect(screen.getAllByTestId('tool-group')).toHaveLength(50)
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
    expect(elapsed).toBeLessThan(3000)
  })
})
