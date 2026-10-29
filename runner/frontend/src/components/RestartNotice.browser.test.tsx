import { expect, it } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ChatView } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../../../../packages/chat/src/test/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

for (const width of [390, 3840]) {
  it.each([
    ['Updated to abc123456789 · continued', 'Updated to abc123456789 · continued'],
    ['Host restarted · paused — check the interrupted command', 'Paused after a restart'],
  ])(`keeps one restart row inside ${width}px: %s`, async (message, shown) => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    resetChatState()
    seed(
      ev({ seq: 1, kind: 'tool_call', payload: { call_id: 'update-1', tool: 'Bash', input: { command: 'make restart-daemon' } } }),
      ev({ seq: 2, kind: 'error', payload: { code: 'session_recovery', retryable: true, message: "The session's host restarted. Full recovery instructions." } }),
      ev({ seq: 3, kind: 'user_message', payload: { source: 'system', text: `[system] ↻ ${message}` } }),
    )
    const mount = () => render(<div style={{ height: '100vh' }}><ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} /></div>)
    const view = mount()
    await waitFor(() => expect(screen.getAllByTestId('restart-notice')).toHaveLength(1))
    const row = screen.getByTestId('restart-notice')
    expect(row.textContent).toContain(shown)
    expect(row.textContent).not.toContain('check the interrupted command')
    expect(screen.queryByText(/Full recovery instructions/)).toBeNull()
    const bounds = row.getBoundingClientRect()
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(width)
    // A phone can wrap the reason, but the notice remains a compact row.
    expect(bounds.height).toBeLessThan(100)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    view.unmount()
    mount()
    await waitFor(() => expect(screen.getAllByTestId('restart-notice')).toHaveLength(1))
  })
}

for (const width of [390, 3840]) {
  it(`discloses every interrupted call and background item at ${width}px`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    resetChatState()
    const message = 'Host restarted · paused — check the interrupted command'
    const details = [
      'A command was cut off: it may not have finished.',
      'Tool calls that were cut off:',
      '- Bash: make deploy',
      '- Read: notes.txt',
      'Background tasks that were cut off:',
      '- bash task-1: Watch the build',
      'Subagents that were cut off:',
      '- Explore "Find the callers", agent id agent-2',
    ].join('\n')
    seed(
      ev({ seq: 1, kind: 'tool_call', payload: { call_id: 'deploy-1', tool: 'Bash', input: { command: 'make deploy' } } }),
      ev({ seq: 2, kind: 'error', payload: { code: 'session_recovery', retryable: true, message: 'Full recovery instructions. Never re-run anything destructive without checking.' } }),
      ev({ seq: 3, kind: 'user_message', payload: { source: 'system', text: `[system] ↻ ${message}\n${details.split('\n').slice(1).join('\n')}` } }),
    )
    const mount = () => render(<div style={{ height: '100vh' }}><ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} /></div>)
    const view = mount()
    await waitFor(() => expect(screen.getAllByTestId('restart-notice')).toHaveLength(1))
    const row = screen.getByTestId('restart-notice')
    const disclosure = row.querySelector('details')
    expect(disclosure).not.toBeNull()
    expect(disclosure!.open).toBe(false)
    const body = screen.getByTestId('restart-details')
    expect(body).not.toBeVisible()
    expect(row.getBoundingClientRect().height).toBeLessThan(100)
    fireEvent.click(row.querySelector('summary')!)
    expect(disclosure!.open).toBe(true)
    expect(body).toBeVisible()
    for (const item of details.split('\n')) expect(body).toHaveTextContent(item)
    expect(row).not.toHaveTextContent('Never re-run')
    const bounds = row.getBoundingClientRect()
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(width)
    expect(body.scrollWidth).toBeLessThanOrEqual(body.clientWidth)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    view.unmount()
    mount()
    await waitFor(() => expect(screen.getAllByTestId('restart-notice')).toHaveLength(1))
    expect(screen.getByTestId('restart-notice').querySelector('details')!.open).toBe(false)
    expect(screen.getByTestId('restart-details')).not.toBeVisible()
  })
}

for (const width of [390, 3840]) {
  it(`resumes a paused session from its restart row at ${width}px`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    resetChatState()
    seed(
      ev({ seq: 1, kind: 'tool_call', payload: { call_id: 'deploy-1', tool: 'Bash', input: { command: 'make deploy' } } }),
      ev({ seq: 2, kind: 'user_message', payload: { source: 'system', text: '[system] ↻ Host restarted · paused — check the interrupted command\nTool calls that were cut off:\n- Bash: make deploy' } }),
    )
    const fake = createFakeTransport()
    const send = fake.transport.send.bind(fake.transport)
    let release!: () => void
    const gate = new Promise<void>(r => { release = r })
    fake.transport.send = async (...args) => { await gate; return send(...args) }
    render(<div style={{ height: '100vh' }}><ChatView session={SESSION} transport={fake.transport} meta={meta({ status: 'idle' })} /></div>)
    const row = await screen.findByTestId('restart-notice')
    const button = screen.getByRole('button', { name: 'Resume' })
    expect(row.querySelector('summary')!.textContent).toBe('Paused after a restart')
    const rb = row.getBoundingClientRect(), bb = button.getBoundingClientRect()
    expect(bb.left).toBeGreaterThanOrEqual(rb.left)
    expect(bb.right).toBeLessThanOrEqual(Math.min(rb.right, width))
    expect(bb.height).toBeGreaterThanOrEqual(width < 640 ? 36 : 26)
    expect(rb.height).toBeLessThan(100)
    const cs = getComputedStyle(button)
    expect(cs.borderTopWidth).toBe('1px')
    expect(cs.borderTopLeftRadius).toBe('6px')
    expect(cs.fontFamily).toBe(getComputedStyle(row).fontFamily)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    fireEvent.click(button)
    expect(button).toBeDisabled()
    fireEvent.click(button)
    release()
    await waitFor(() => expect(row.querySelector('summary')!.textContent).toBe('Resumed'))
    expect(fake.sent).toEqual(['Continue the task you were working on.'])
    expect(screen.queryByRole('button', { name: 'Resume' })).toBeNull()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
}
