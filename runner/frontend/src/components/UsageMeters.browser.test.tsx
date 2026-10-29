// Card #85 in a real browser: the meters, a placeholder row and a no-window row fit the sidebar on
// a phone and on a 4K screen, and every row carries its tooltip.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { commands, page } from 'vitest/browser'
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
          makeDaemon({ id: 'a', name: 'Workstation', status: 'connected', available_engines: ['claude', 'codex'], usage_limits: [{ provider: 'claude', windows: [{ id: '5h', used_percent: 20, resets_at: at(60) }, { id: '7d', used_percent: 64, resets_at: at(2 * 1440) }, { id: '7d_opus', used_percent: 100, resets_at: at(3 * 1440) }, { id: '7d_sonnet', used_percent: 99 }, { id: '7d_fable', used_percent: 20 }] }] }),
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
    expect(screen.getAllByRole('progressbar')).toHaveLength(5)
  })
})

// Both motion modes must fit at phone and desktop sizes. Real computed styles prove the
// reduced-motion override disables animation and reveals the accessible warning marker.
describe('critical usage meters', () => {
  it.each([390, 3840])('uses a slow pulse and a solid reduced-motion warning at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    render(<div style={{ width: width < 600 ? width : 300 }}>
      <UsageMeters now={NOW} daemons={[makeDaemon({ usage_limits: [{ provider: 'codex', windows: [{ id: '7d', used_percent: 94.6, resets_at: at(60) }] }] })]} />
    </div>)
    const meter = screen.getByRole('progressbar')
    const fill = meter.querySelector('.usage-meter__fill')!
    const percent = meter.querySelector('.usage-meter__pct')!
    const marker = meter.querySelector('.usage-meter__critical')!
    expect(meter).toHaveClass('usage-meter--danger', 'usage-meter--pulse')
    expect(meter.title).toMatch(/resets .+ \(in 1h 0m\)/)
    expect(meter.scrollWidth).toBeLessThanOrEqual(meter.clientWidth + 1)
    expect(getComputedStyle(fill).animationDuration).toBe('2s')
    expect(getComputedStyle(percent).animationDuration).toBe('2s')
    expect(marker).not.toBeVisible()
    await commands.setUsageReducedMotion(true)
    try {
      expect(getComputedStyle(fill).animationName).toBe('none')
      expect(getComputedStyle(percent).animationName).toBe('none')
      expect(getComputedStyle(percent).fontWeight).toBe('800')
      expect(marker).toBeVisible()
      expect(marker).toHaveAccessibleName('Usage limit nearly reached')
      expect(meter.scrollWidth).toBeLessThanOrEqual(meter.clientWidth + 1)
    } finally { await commands.setUsageReducedMotion(false) }
  })
})

// Refusals use the same critical motion and accessible reduced-motion treatment.
describe('capped usage chip', () => {
  it.each([390, 3840])('pulses and respects reduced motion at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    render(<div style={{ width: width < 600 ? width : 300 }}>
      <UsageMeters now={NOW} daemons={[makeDaemon({ usage_limits: [{ provider: 'codex', windows: [{ id: '7d', used_percent: 50, resets_at: at(60) }, { id: 'capped', used_percent: 100, resets_at: at(60) }] }] })]} />
    </div>)
    const chip = screen.getByTestId('usage-window-capped')
    const dot = chip.querySelector('.usage-meter__dot')!
    const text = chip.querySelector('.usage-meter__pct')!
    const marker = chip.querySelector('.usage-meter__critical')!
    expect(getComputedStyle(dot).animationDuration).toBe('2s')
    expect(getComputedStyle(text).animationDuration).toBe('2s')
    expect(marker).not.toBeVisible()
    await commands.setUsageReducedMotion(true)
    try {
      expect(getComputedStyle(dot).animationName).toBe('none')
      expect(getComputedStyle(text).animationName).toBe('none')
      expect(getComputedStyle(text).fontWeight).toBe('800')
      expect(marker).toBeVisible()
      expect(marker).toHaveAccessibleName('Usage limit reached')
      expect(marker.getBoundingClientRect().right).toBeLessThanOrEqual(chip.getBoundingClientRect().right)
      expect(chip.scrollWidth).toBeLessThanOrEqual(chip.clientWidth + 1)
    } finally { await commands.setUsageReducedMotion(false) }
  })
})
