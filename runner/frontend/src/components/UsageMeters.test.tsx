import { describe, it, expect } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import UsageMeters from './UsageMeters'
import { meterLevel, resetsIn } from '../lib/usageLimits'
import { makeDaemon } from '../test/fixtures'
import type { UsageLimits } from '../types'

const NOW = new Date('2026-10-16T11:50:00Z')
const at = (mins: number) => new Date(NOW.getTime() + mins * 60_000).toISOString()

const codex: UsageLimits = {
  provider: 'codex',
  windows: [{ id: '5h', used_percent: 42, resets_at: at(130) }, { id: '7d', used_percent: 91.6, resets_at: at(3 * 1440 + 240) }],
}
const claudeCapped: UsageLimits = { provider: 'claude', windows: [{ id: 'capped', used_percent: 100, resets_at: at(130) }] }

describe('UsageMeters', () => {
  it('draws nothing when no provider reports', () => {
    const { container } = render(<UsageMeters daemons={[makeDaemon(), makeDaemon({ id: 'd2', usage_limits: [] })]} now={NOW} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('draws one row per provider, a labelled bar per window, with the percentage and when it resets', () => {
    render(<UsageMeters daemons={[makeDaemon({ usage_limits: [codex, claudeCapped] })]} now={NOW} />)
    const rows = screen.getAllByTestId(/^usage-row-/)
    expect(rows.map(r => r.dataset.testid)).toEqual(['usage-row-claude', 'usage-row-codex'])

    const row = screen.getByTestId('usage-row-codex')
    expect(row).toHaveTextContent('Codex')
    const five = within(row).getByTestId('usage-window-5h')
    expect(five).toHaveTextContent('5h')
    expect(five).toHaveTextContent('42%')
    expect(five.title).toBe('5-hour window: 42% used · resets in 2h 10m')
    expect(five.querySelector('.usage-meter__fill')).toHaveStyle({ width: '42%' })
    const week = within(row).getByTestId('usage-window-7d')
    expect(week).toHaveTextContent('week')
    expect(week).toHaveTextContent('92%')
    expect(week.title).toBe('Weekly window: 92% used · resets in 3d 4h')
  })

  describe('a spend limit (work or enterprise account)', () => {
    const spend = (...windows: UsageLimits['windows']): UsageLimits => ({ provider: 'claude', windows })

    it('draws the monthly spend limit with no reset, labelled and named as a spend limit', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [spend({ id: 'month', used_percent: 34 })] })]} now={NOW} />)
      const month = screen.getByTestId('usage-window-month')
      expect(month).toHaveTextContent('month')
      expect(month).toHaveTextContent('34%')
      expect(month.title).toBe('Monthly spend limit: 34% used')
      expect(month).toHaveAccessibleName('Claude monthly spend limit, 34% used')
      expect(month.querySelector('.usage-meter__reset')).toBeNull()
      expect(month.querySelector('.usage-meter__fill')).toHaveStyle({ width: '34%' })
    })

    it('says "<1%" for a share under one percent, never 0%', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [spend({ id: 'month', used_percent: 0.7072 })] })]} now={NOW} />)
      const month = screen.getByTestId('usage-window-month')
      expect(month).toHaveTextContent('<1%')
      expect(month).not.toHaveTextContent('0%')
      expect(month.title).toBe('Monthly spend limit: <1% used')
      expect(month).toHaveAccessibleName('Claude monthly spend limit, under 1% used')
    })

    it('still says 0% for a share of exactly zero', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [spend({ id: 'month', used_percent: 0 })] })]} now={NOW} />)
      expect(screen.getByTestId('usage-window-month')).toHaveTextContent('0%')
    })

    it('orders 5h, 7d, month, then a cap, and keeps a cap that has no reset time', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [spend(
        { id: 'capped', used_percent: 100 },
        { id: 'month', used_percent: 100 },
        { id: '7d', used_percent: 10, resets_at: at(600) },
        { id: '5h', used_percent: 20, resets_at: at(60) },
      )] })]} now={NOW} />)
      const row = screen.getByTestId('usage-row-claude')
      expect(within(row).getAllByTestId(/^usage-window-/).map(w => w.dataset.testid)).toEqual([
        'usage-window-5h', 'usage-window-7d', 'usage-window-month', 'usage-window-capped',
      ])
      const capped = screen.getByTestId('usage-window-capped')
      expect(capped).toHaveTextContent(/^capped$/)
      expect(capped.title).toBe('Claude usage limit reached · reset time unknown')
    })

    it('draws the daily and weekly spend limits after the month', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [spend(
        { id: 'spend-week', used_percent: 40 }, { id: 'day', used_percent: 12 }, { id: 'month', used_percent: 25 },
      )] })]} now={NOW} />)
      const row = screen.getByTestId('usage-row-claude')
      expect(within(row).getAllByTestId(/^usage-window-/).map(w => w.dataset.testid)).toEqual([
        'usage-window-month', 'usage-window-day', 'usage-window-spend-week',
      ])
      expect(screen.getByTestId('usage-window-day').title).toBe('Daily spend limit: 12% used')
      expect(screen.getByTestId('usage-window-spend-week').title).toBe('Weekly spend limit: 40% used')
    })
  })

  it('colours a window by how much is used', () => {
    expect([0, 69.9, 70, 89.9, 90, 100].map(meterLevel)).toEqual(['quiet', 'quiet', 'warning', 'warning', 'danger', 'danger'])
    render(<UsageMeters daemons={[makeDaemon({ usage_limits: [codex] })]} now={NOW} />)
    expect(screen.getByTestId('usage-window-5h')).toHaveClass('usage-meter--quiet')
    expect(screen.getByTestId('usage-window-7d')).toHaveClass('usage-meter--danger')
  })

  it.each(['codex', 'fixture-engine'])('keeps a %s cap without a reset visible without inferring a spend limit', (provider) => {
    render(<UsageMeters daemons={[makeDaemon({ usage_limits: [{ provider, windows: [{ id: 'capped', used_percent: 100 }] }] })]} now={NOW} />)
    const capped = screen.getByTestId('usage-window-capped')
    expect(capped).toBeVisible()
    expect(capped).toHaveAttribute('role', 'status')
    expect(capped).toHaveTextContent('capped')
    expect(capped.title).toBe(`${provider === 'codex' ? 'Codex' : provider} usage limit reached · reset time unknown`)
    expect(capped.title).not.toContain('spend')
  })

  it('says a capped provider is capped and when it resets, until then', () => {
    const { rerender } = render(<UsageMeters daemons={[makeDaemon({ usage_limits: [claudeCapped] })]} now={NOW} />)
    const capped = screen.getByTestId('usage-window-capped')
    const clock = new Date(at(130)).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
    expect(capped).toHaveTextContent(`capped · resets ${clock}`)
    expect(capped).toHaveClass('usage-meter--danger')
    expect(capped.title).toBe('Claude refused a call for its usage limit · resets in 2h 10m')
    expect(capped).toHaveAttribute('role', 'status')
    // Past the reset: nothing left to show.
    rerender(<UsageMeters daemons={[makeDaemon({ usage_limits: [claudeCapped] })]} now={new Date(at(131))} />)
    expect(screen.queryByTestId('usage-row-claude')).toBeNull()
  })

  it('gives each bar a progressbar role, its value and a spoken name with the reset and when it was read', () => {
    const asOf = new Date(at(-4))
    const clock = asOf.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
    render(<UsageMeters daemons={[makeDaemon({ usage_limits: [{ ...codex, fetched_at: asOf.toISOString() }] })]} now={NOW} />)
    const bars = screen.getAllByRole('progressbar')
    expect(bars).toHaveLength(2)
    expect(bars[0]).toHaveAttribute('aria-valuenow', '42')
    expect(bars[0]).toHaveAttribute('aria-valuemin', '0')
    expect(bars[0]).toHaveAttribute('aria-valuemax', '100')
    expect(bars[0]).toHaveAccessibleName(`Codex 5-hour window, 42% used, resets in 2h 10m, as of ${clock}`)
    expect(screen.getByTestId('usage-window-5h').title).toBe(`5-hour window: 42% used · resets in 2h 10m · as of ${clock}`)
  })

  it('shows the reset time as text too, for a screen with no hover', () => {
    render(<UsageMeters daemons={[makeDaemon({ usage_limits: [codex] })]} now={NOW} />)
    const reset = screen.getByTestId('usage-window-5h').querySelector('.usage-meter__reset')
    expect(reset).toHaveTextContent('↻2h')
  })

  it('drops a window whose reset has passed: its percentage is no longer true', () => {
    const passed: UsageLimits = { provider: 'codex', windows: [{ id: '5h', used_percent: 97, resets_at: at(-1) }, { id: '7d', used_percent: 40, resets_at: at(600) }] }
    render(<UsageMeters daemons={[makeDaemon({ usage_limits: [passed] })]} now={NOW} />)
    expect(screen.queryByTestId('usage-window-5h')).toBeNull()
    expect(screen.getByTestId('usage-window-7d')).toBeInTheDocument()
  })

  it('names the machine when more than one reports', () => {
    render(<UsageMeters daemons={[
      makeDaemon({ name: 'desk', usage_limits: [codex] }),
      makeDaemon({ id: 'd2', name: 'laptop', usage_limits: [codex] }),
    ]} now={NOW} />)
    expect(screen.getAllByTestId('usage-row-codex').map(r => r.textContent)).toEqual([
      expect.stringContaining('Codex · desk'), expect.stringContaining('Codex · laptop'),
    ])
  })

  it('reads a reset time the short way', () => {
    expect(resetsIn(at(0.5), NOW)).toBe('in <1m')
    expect(resetsIn(at(45), NOW)).toBe('in 45m')
    expect(resetsIn(at(130), NOW)).toBe('in 2h 10m')
    expect(resetsIn(at(1440 * 2 + 60), NOW)).toBe('in 2d 1h')
    expect(resetsIn(undefined, NOW)).toBe('')
    expect(resetsIn(at(-1), NOW)).toBe('')
  })
})
