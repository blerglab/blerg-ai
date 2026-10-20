// Outcome totals stay compact without hiding the full failed call from inspection.
import { afterEach, beforeEach, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ChatView } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import { ev, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'

beforeEach(resetChatState)
afterEach(() => { localStorage.clear() })
it.each([390, 1280].flatMap(width => [false, true].map(latestFailure => ({width, latestFailure}))))('keeps nineteen calls compact at $width px (latest completion failed: $latestFailure)', async ({width, latestFailure}) => {
  await page.viewport(width, 700)
  let seq = 0
  for (let i = 0; i < 19; i++) {
    seed(ev({ seq: ++seq, kind: 'tool_call', payload: { tool: 'bash', call_id: `c${i}`, input: { command: `echo fixture-${i}` } } }),
      ev({ seq: ++seq, kind: 'tool_result', payload: { call_id: `c${i}`, output: (latestFailure ? i >= 14 : i < 5) ? `failure detail ${i}` : 'ok', is_error: latestFailure ? i >= 14 : i < 5, duration_ms: 100 } }))
  }
  render(<div style={{ height: '100vh', width: '100%', minWidth: 0 }}><ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta()} /></div>)
  const group = await screen.findByTestId('tool-group')
  expect(group.dataset.outcome).toBe('mixed')
  expect(group.classList.contains('tcg-group-bad')).toBe(false)
  expect(within(group).getByTestId('tool-fail-badge').classList.contains('tcg-badge-quiet')).toBe(true)
  const line = group.querySelector<HTMLElement>('.tcg-line')!
  expect(line.scrollWidth).toBeLessThanOrEqual(line.clientWidth)
  expect(within(group).getByTestId('tool-success-count').textContent).toBe('14 succeeded')
  expect(within(group).getByTestId('tool-fail-badge').textContent).toContain('5 failed')
  expect(within(group).queryByText(/failure detail/)).toBeNull()
  expect(group.getBoundingClientRect().height).toBeLessThan(100)
  expect(group.scrollWidth).toBeLessThanOrEqual(group.clientWidth)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await userEvent.click(within(group).getByTestId('tool-group-toggle'))
  const row = within(group).getAllByTestId('tool-row')[latestFailure ? 18 : 0]
  await userEvent.click(within(row).getByRole('button'))
  expect(within(row).getByTestId('tool-detail').textContent).toContain(latestFailure ? 'failure detail 18' : 'failure detail 0')
})
