import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import TerminalHistory from './TerminalHistory'
import * as wsMock from '../test/wsMock'
vi.mock('../ws', () => import('../test/wsMock'))
beforeEach(() => wsMock.resetWsMock())
for (const width of [390, 3840]) describe(`legacy history at ${width}px`, () => {
  it('keeps saved output scrollable and never sends runtime commands', async () => {
    await page.viewport(width, 844)
    const { container } = render(<div style={{ display: 'flex', height: 400, width: '100%' }}><TerminalHistory sessionId="legacy" /></div>)
    const history = Array.from({ length: 100 }, (_, i) => `Saved line ${i}`).join('\r\n')
    wsMock.emit({ type: 'session_output', session_id: 'legacy', seq: 1, data: btoa(history) })
    wsMock.emit({ type: 'history_done', session_id: 'legacy' })
    await waitFor(() => expect(container.textContent).toContain('Saved line 99'))
    const region = container.querySelector<HTMLElement>('[role=region]')!
    expect(region.scrollHeight).toBeGreaterThan(region.clientHeight)
    region.scrollTop = region.scrollHeight
    expect(region.scrollTop).toBeGreaterThan(0)
    expect(container.querySelector('textarea')).toBeNull()
    expect(wsMock.send.mock.calls.map(([msg]) => msg.type)).toEqual(['subscribe_session'])
  })
})
