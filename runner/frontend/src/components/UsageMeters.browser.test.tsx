// Card #85 in a real browser: the meters, a placeholder row and a no-window row fit the sidebar on
// a phone and on a 4K screen, and every row carries its tooltip.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import '../index.css'
import UsageMeters from './UsageMeters'
import { makeDaemon } from '../test/fixtures'

const NOW = new Date('2026-10-16T11:50:00Z')
const at = (mins: number) => new Date(NOW.getTime() + mins * 60_000).toISOString()

describe('usage meters', () => {
  it.each([390, 3840])('fit the sidebar with a placeholder and a no-window row at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    render(
      <div style={{ width: width < 600 ? width : 300, padding: '8px 16px', boxSizing: 'border-box' }}>
        <UsageMeters now={NOW} daemons={[
          makeDaemon({ id: 'a', name: 'Workstation', status: 'connected', available_engines: ['claude', 'codex'], usage_limits: [{ provider: 'claude', windows: [{ id: '5h', used_percent: 20, resets_at: at(60) }, { id: '7d', used_percent: 64, resets_at: at(2 * 1440) }] }] }),
          makeDaemon({ id: 'b', name: 'Work laptop with a long name', status: 'connected', available_engines: ['codex'], usage_limits: [{ provider: 'codex', windows: [], note: 'no_windows' }] }),
        ]} />
      </div>,
    )
    const rows = screen.getAllByTestId(/^usage-row-/)
    expect(rows).toHaveLength(3)
    for (const row of rows) {
      expect(row.title).not.toBe('')
      expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth + 1)
      expect(row.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    }
    expect(screen.getByTestId('usage-pending')).toBeVisible()
    expect(screen.getByTestId('usage-none')).toBeVisible()
    expect(screen.getAllByRole('progressbar')).toHaveLength(2)
  })
})
