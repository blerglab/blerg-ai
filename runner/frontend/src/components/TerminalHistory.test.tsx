import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, waitFor } from '@testing-library/react'
import TerminalHistory from './TerminalHistory'
import * as wsMock from '../test/wsMock'

vi.mock('../ws', () => import('../test/wsMock'))
beforeEach(() => wsMock.resetWsMock())

describe('legacy Terminal history', () => {
  it('replays saved ANSI output without any runtime or input requests', async () => {
    const { container, unmount } = render(<TerminalHistory sessionId="legacy" />)
    act(() => wsMock.emit({ type: 'session_output', session_id: 'legacy', seq: 1, data: btoa('\x1b[31mSaved work\x1b[0m\r\n') }))
    await waitFor(() => expect(container.textContent).toContain('Saved work'))
    act(() => wsMock.emit({ type: 'history_done', session_id: 'legacy' }))
    fireEvent.keyDown(container, { key: 'Enter' })
    expect(container.querySelector('textarea')).toBeNull()
    expect(wsMock.send.mock.calls.map(([msg]) => msg.type)).toEqual(['subscribe_session'])
    unmount()
    expect(wsMock.send).toHaveBeenLastCalledWith({ type: 'unsubscribe_session', session_id: 'legacy' })
  })
  it('filters other sessions and replaces history on reconnect', async () => {
    const { container } = render(<TerminalHistory sessionId="legacy" />)
    act(() => wsMock.emit({ type: 'session_output', session_id: 'other', seq: 1, data: btoa('Other work') }))
    expect(container.textContent).not.toContain('Other work')
    act(() => wsMock.emit({ type: 'session_output', session_id: 'legacy', seq: 1, data: btoa('Original work') }))
    await waitFor(() => expect(container.textContent).toContain('Original work'))
    act(() => wsMock.flushOpen())
    act(() => wsMock.emit({ type: 'session_output', session_id: 'legacy', seq: 1, data: btoa('Replayed work') }))
    await waitFor(() => expect(container.textContent).toContain('Replayed work'))
    expect(container.textContent).not.toContain('Original work')
  })
})
