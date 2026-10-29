// The owner saw the start card's rows painted like its icons: done rows filled green and pending
// rows boxed, because the row and its icon shared the state class (sp-done, sp-pending).
import '../index.css'
import '@blerglab/chat/blerg.css'
import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { StartProgress } from '@blerglab/chat'

it('paints only the icons by state, never the rows', () => {
  render(<div className="blerg-chat"><StartProgress session={{ id: 's1', status: 'starting' }} onStop={vi.fn()} attempt={{
    runtime: 'docker', startedAt: Date.now(), ready: false, failed: false, stages: [
      { id: 'queued', label: 'Preparing session', state: 'done' },
      { id: 'plugins', label: 'Installing plugins', state: 'active' },
      { id: 'engine', label: 'Starting the agent', state: 'pending' },
    ] }} /></div>)
  const rows = screen.getAllByRole('listitem')
  for (const row of rows) {
    const style = getComputedStyle(row)
    expect(style.backgroundColor).toMatch(/^(rgba\(0, 0, 0, 0\)|transparent)$/)
    expect(style.borderTopStyle).toBe('none')
  }
  const icon = (row: HTMLElement) => getComputedStyle(row.querySelector('.sp-icon')!)
  expect(icon(rows[0]).backgroundColor).not.toMatch(/^(rgba\(0, 0, 0, 0\)|transparent)$/)
  expect(icon(rows[2]).borderTopStyle).toBe('solid')
})
