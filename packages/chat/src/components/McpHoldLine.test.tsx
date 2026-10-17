import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { McpHoldLine } from './McpHoldLine'

describe('the held first message line', () => {
  it('says what it waits for, the clock, and Send now when the viewer may release', () => {
    vi.useFakeTimers()
    vi.setSystemTime(13_000)
    const send = vi.fn()
    render(<McpHoldLine hold={{ since: 1000, until: 31_000 }} onSendNow={send} />)
    const line = screen.getByTestId('mcp-hold')
    expect(line.textContent).toBe('Waiting for MCP servers before sending your first message · 0:12 / 0:30 · Send now')
    act(() => { vi.advanceTimersByTime(3000) })
    expect(line.textContent).toContain('0:15 / 0:30')
    fireEvent.click(screen.getByRole('button', { name: 'Send now' }))
    expect(send).toHaveBeenCalledOnce()
    vi.useRealTimers()
  })

  it('has no button for a viewer', () => {
    render(<McpHoldLine hold={{ since: 0, until: 30_000 }} />)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
