import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ChatView from './ChatView'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { AgentEvent } from '../types'

const KEY = COMPACT

let seq = 0
function ev(kind: string, payload: unknown): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, kind, payload })
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

function mount(status: 'idle' | 'running' = 'idle', t: FakeTransport = createFakeTransport()) {
  const r = render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status })} />)
  return { t, ...r }
}

// Live events after mount go through the transport, as they would from the server.
function arrive(t: FakeTransport, ...events: AgentEvent[]) {
  act(() => { for (const e of events) t.emit(e) })
}

describe('ChatView compact tool calls', () => {
  beforeEach(() => {
    resetChatState()
    seq = 0
  })
  afterEach(() => {
    localStorage.removeItem(KEY)
  })

  it('renders one or two calls as inline rows and three or more as one summary', () => {
    seed(...bashRun(2))
    const { t } = mount()
    expect(screen.getByTestId('tool-inline')).toBeInTheDocument()
    expect(screen.getAllByTestId('tool-row')).toHaveLength(2)
    expect(screen.queryByTestId('tool-group')).not.toBeInTheDocument()
    expect(screen.queryByTestId('tool-card')).not.toBeInTheDocument()

    arrive(t, ...bashRun(1, 3))
    expect(screen.getByTestId('tool-group')).toBeInTheDocument()
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
  })

  it('splits groups at assistant text and other cards, but not at hidden kinds', () => {
    seed(
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
    mount()
    // 4 calls before the text (one group), 3 after (one group), 1 after the error.
    expect(screen.getAllByTestId('tool-group')).toHaveLength(2)
    expect(screen.getByText(/Ran 4 tool calls/)).toBeInTheDocument()
    expect(screen.getByText(/Ran 3 tool calls/)).toBeInTheDocument()
    expect(screen.getAllByTestId('tool-inline')).toHaveLength(1)
    expect(screen.getByText('in the middle')).toBeInTheDocument()
  })

  it('summarises counts by tool name, most used first', () => {
    seed(
      call('a', 'Read', { file_path: 'src/App.tsx' }), result('a'),
      call('b', 'Bash', { command: 'ls' }), result('b'),
      call('c', 'Bash', { command: 'pwd' }), result('c'),
      call('d', 'Edit', { file_path: 'x.go' }), result('d', 'ok', false, 900),
      call('e', 'Bash', { command: 'make' }), result('e', 'ok', false, 1400),
    )
    mount()
    const toggle = screen.getByTestId('tool-group-toggle')
    expect(toggle).toHaveTextContent('Ran 5 tool calls · Bash ×3, Read ×1, Edit ×1')
    // Total duration: 100 + 100 + 100 + 900 + 1400 = 2.6s.
    expect(toggle).toHaveTextContent('2.6s')
    expect(toggle).toHaveTextContent('✓')
    expect(screen.getByTestId('tool-preview')).toHaveTextContent('$ make')
  })

  it('previews the latest call for each kind of tool', () => {
    seed(...bashRun(2), call('r', 'Read', { file_path: 'src/App.tsx' }), result('r'))
    mount()
    expect(screen.getByTestId('tool-preview').textContent).toBe('Read src/App.tsx')
  })

  it('shows a failure badge and reveals the failing line without expanding', () => {
    seed(
      ...bashRun(2),
      call('f1', 'Bash', { command: 'npm test' }), result('f1', 'FAIL src/a.test.ts\nmore detail', true),
      call('f2', 'Bash', { command: 'go vet' }), result('f2', 'vet: nope', true),
      ...bashRun(1, 10),
    )
    mount()
    expect(screen.getByTestId('tool-fail-badge')).toHaveTextContent('✗ 2 failed')
    const lines = screen.getAllByTestId('tool-fail')
    expect(lines).toHaveLength(2)
    expect(lines[0]).toHaveTextContent('FAIL src/a.test.ts')
    expect(lines[0]).not.toHaveTextContent('more detail')
    expect(screen.getByTestId('tool-group-toggle')).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
  })

  it('shows the running call in the preview with a spinner', () => {
    seed(...bashRun(3), call('run', 'Bash', { command: 'npm test -- --run' }))
    mount('running')
    expect(screen.getByTestId('tool-preview')).toHaveTextContent('$ npm test -- --run')
    expect(within(screen.getByTestId('tool-group-toggle')).getByRole('img', { name: 'running' })).toBeInTheDocument()
  })

  it('does not spin for a call with no result once the session has stopped', () => {
    seed(...bashRun(3), call('run', 'Bash', { command: 'npm test' }))
    mount()
    expect(screen.queryByRole('img', { name: 'running' })).not.toBeInTheDocument()
  })

  it('expands on click and on the keyboard, showing input and output', async () => {
    const user = userEvent.setup()
    seed(...bashRun(3))
    mount()
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
    seed(...bashRun(2))
    const { t } = mount()
    // Grows from two calls (inline) to three (summary) without a reset.
    arrive(t, ...bashRun(1, 3))
    expect(screen.getByTestId('tool-group-toggle')).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(screen.getByTestId('tool-group-toggle'))
    const row = screen.getAllByTestId('tool-row')[1]
    fireEvent.click(within(row).getByRole('button'))
    expect(within(row).getByTestId('tool-detail')).toBeInTheDocument()

    arrive(t, ...bashRun(2, 4))
    expect(screen.getByTestId('tool-group-toggle')).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getAllByTestId('tool-row')).toHaveLength(5)
    expect(within(screen.getAllByTestId('tool-row')[1]).getByTestId('tool-detail')).toBeInTheDocument()
    expect(screen.getByTestId('tool-group-toggle')).toHaveTextContent('Ran 5 tool calls')

    // A separate group the user never touched stays collapsed.
    arrive(t, ev('assistant_text', { text: 'next', done: true }), ...bashRun(3, 20))
    const toggles = screen.getAllByTestId('tool-group-toggle')
    expect(toggles[0]).toHaveAttribute('aria-expanded', 'true')
    expect(toggles[1]).toHaveAttribute('aria-expanded', 'false')
  })

  it('caps very long output and reveals it with Show all', () => {
    const long = Array.from({ length: 100 }, (_, i) => `line ${i}`).join('\n')
    seed(call('big', 'Bash', { command: 'seq' }), result('big', long))
    mount()
    fireEvent.click(within(screen.getByTestId('tool-row')).getByRole('button'))
    const detail = screen.getByTestId('tool-detail')
    expect(detail).toHaveTextContent('line 39')
    expect(detail).not.toHaveTextContent('line 40')
    fireEvent.click(screen.getByRole('button', { name: 'Show all 100 lines' }))
    expect(screen.getByTestId('tool-detail')).toHaveTextContent('line 99')
    expect(screen.queryByRole('button', { name: /Show all/ })).not.toBeInTheDocument()
  })

  it('survives unknown tools and inputs that are not objects', () => {
    seed(
      call('1', 'mystery_tool', null), result('1'),
      call('2', 'codex_exec', { command: ['bash', '-lc', 'ls -la'] }), result('2'),
      call('3', 'weird', 42), result('3', 'ok'),
      call('4', 'hermes', 'plain string input'), result('4'),
      call('5', 'odd', { a: { b: 1 }, list: [1, 2] }), result('5'),
      ev('tool_call', { call_id: 'no-tool' }),
      ev('tool_call', null),
    )
    mount()
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
    seed(call('x', 'Bash', { command: '<img src=x onerror=alert(1)>' }), result('x', '<b>bold</b>'))
    const { container } = mount()
    fireEvent.click(within(screen.getByTestId('tool-row')).getByRole('button'))
    expect(container.querySelector('.tcg-detail b')).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('.tcg-out')?.textContent).toContain('<b>bold</b>')
  })

  it('keeps publish tools as full cards that end a group', () => {
    seed(
      ...bashRun(3),
      call('m', 'push_mockup', {}), result('m', 'published: https://example.test/m'),
      ...bashRun(1, 9),
    )
    mount()
    expect(screen.getByTestId('mockup-link')).toHaveAttribute('href', 'https://example.test/m')
    expect(screen.getAllByTestId('tool-group')).toHaveLength(1)
    expect(screen.getAllByTestId('tool-inline')).toHaveLength(1)
  })

  it('with the toggle off renders the legacy cards, and remembers the choice', () => {
    seed(...bashRun(4))
    const { unmount } = mount()
    const box = screen.getByLabelText('Compact tool calls') as HTMLInputElement
    expect(box.checked).toBe(true)
    fireEvent.click(box)
    expect(screen.getAllByTestId('tool-card')).toHaveLength(4)
    expect(screen.queryByTestId('tool-group')).not.toBeInTheDocument()
    expect(localStorage.getItem(KEY)).toBe('0')
    unmount()

    mount()
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
      seed(...bashRun(3))
      mount()
      expect(screen.getByTestId('tool-group')).toBeInTheDocument()
      fireEvent.click(screen.getByLabelText('Compact tool calls'))
      expect(screen.getAllByTestId('tool-card')).toHaveLength(3)
    } finally {
      get.mockRestore()
      set.mockRestore()
    }
  })

  it('keeps the choice in the host’s own storage when one is given', () => {
    seed(...bashRun(3))
    const map = new Map<string, string>()
    const storage = { get: (k: string) => map.get(k) ?? null, set: (k: string, v: string | null) => { if (v === null) map.delete(k); else map.set(k, v) } }
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} storage={storage} />)
    fireEvent.click(screen.getByLabelText('Compact tool calls'))
    expect(screen.getAllByTestId('tool-card')).toHaveLength(3)
    expect(map.get(KEY)).toBe('0')
    expect(localStorage.getItem(KEY)).toBeNull()
  })

  it('renders a 2,000-event transcript as summary rows only, quickly', () => {
    const events: AgentEvent[] = []
    for (let g = 0; g < 50; g++) {
      events.push(ev('assistant_text', { text: `step ${g}`, done: true }))
      events.push(...bashRun(20, g * 20 + 1))
    }
    expect(events.length).toBeGreaterThan(2000)
    seed(...events)
    const t0 = performance.now()
    mount()
    const elapsed = performance.now() - t0
    expect(screen.getAllByTestId('tool-group')).toHaveLength(50)
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
    expect(elapsed).toBeLessThan(3000)
  })
})
