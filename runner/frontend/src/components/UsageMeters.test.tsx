import { describe, it, expect } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import UsageMeters from './UsageMeters'
import { clockTime, meterLevel, resetsIn } from '../lib/usageLimits'
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
  it('labels each model weekly window independently', () => {
    render(<UsageMeters now={NOW} daemons={[makeDaemon({ usage_limits: [{ provider: 'claude', windows: [
      { id: '7d_fable', used_percent: 20 }, { id: '7d_opus', used_percent: 100 },
      { id: '7d', used_percent: 40 }, { id: '7d_sonnet', used_percent: 99 },
    ] }] })]} />)
    expect(screen.getAllByRole('progressbar').map(w => w.dataset.testid)).toEqual([
      'usage-window-7d', 'usage-window-7d_opus', 'usage-window-7d_sonnet', 'usage-window-7d_fable',
    ])
    for (const model of ['Opus', 'Sonnet', 'Fable']) {
      expect(screen.getByTestId(`usage-window-7d_${model.toLowerCase()}`).title).toContain(`${model} weekly window`)
    }
  })

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
    expect(five.title).toBe(`5-hour window: 42% used, 58% left · resets ${clockTime(new Date(at(130)))} (in 2h 10m)`)
    expect(five.querySelector('.usage-meter__fill')).toHaveStyle({ width: '42%' })
    const week = within(row).getByTestId('usage-window-7d')
    expect(week).toHaveTextContent('week')
    expect(week).toHaveTextContent('92%')
    expect(week.title).toBe(`Weekly window: 92% used, 8% left · resets ${new Date(at(3 * 1440 + 240)).toLocaleDateString([], { weekday: 'short' })} ${clockTime(new Date(at(3 * 1440 + 240)))} (in 3d 4h)`)
  })

  describe('a spend limit (work or enterprise account)', () => {
    const spend = (...windows: UsageLimits['windows']): UsageLimits => ({ provider: 'claude', windows })

    it('draws the monthly spend limit with no reset, labelled and named as a spend limit', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [spend({ id: 'month', used_percent: 34 })] })]} now={NOW} />)
      const month = screen.getByTestId('usage-window-month')
      expect(month).toHaveTextContent('month')
      expect(month).toHaveTextContent('34%')
      expect(month.title).toBe('Monthly spend limit: 34% used, 66% left')
      expect(month).toHaveAccessibleName('Claude monthly spend limit, 34% used, 66% left')
      expect(month.querySelector('.usage-meter__reset')).toBeNull()
      expect(month.querySelector('.usage-meter__fill')).toHaveStyle({ width: '34%' })
    })

    it('says "<1%" for a share under one percent, never 0%', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [spend({ id: 'month', used_percent: 0.7072 })] })]} now={NOW} />)
      const month = screen.getByTestId('usage-window-month')
      expect(month).toHaveTextContent('<1%')
      expect(month).not.toHaveTextContent('0%')
      expect(month.title).toBe('Monthly spend limit: <1% used, 99% left')
      expect(month).toHaveAccessibleName('Claude monthly spend limit, under 1% used, 99% left')
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
      expect(capped).toHaveTextContent(/^capped!$/)
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
      expect(screen.getByTestId('usage-window-day').title).toBe('Daily spend limit: 12% used, 88% left')
      expect(screen.getByTestId('usage-window-spend-week').title).toBe('Weekly spend limit: 40% used, 60% left')
    })
  })

  it.each([79.9, 80, 89.9, 90, 94.9, 95, 100])('uses the severity and pulse for %s percent', percent => {
    render(<UsageMeters daemons={[makeDaemon({ usage_limits: [{ provider: 'codex', windows: [{ id: '7d', used_percent: percent, resets_at: at(60) }] }] })]} now={NOW} />)
    const meter = screen.getByRole('progressbar')
    expect(meter).toHaveClass(`usage-meter--${Math.round(percent) >= 90 ? 'danger' : Math.round(percent) >= 80 ? 'warning' : 'quiet'}`)
    expect(meter.classList.contains('usage-meter--pulse')).toBe(Math.round(percent) >= 95)
    if (Math.round(percent) >= 95) expect(meter.querySelector('[aria-label="Usage limit nearly reached"]')).toHaveTextContent('!')
    expect(meter.title).toContain(`resets ${clockTime(new Date(at(60)))}`)
  })

  it('colours a window by how much is used', () => {
    expect([0, 79.9, 80, 89.9, 90, 100].map(meterLevel)).toEqual(['quiet', 'quiet', 'warning', 'warning', 'danger', 'danger'])
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
    expect(capped).toHaveClass('usage-meter--danger', 'usage-meter--pulse')
    expect(capped.querySelector('.usage-meter__critical')).toHaveAccessibleName('Usage limit reached')
    expect(capped.title).toBe(`Claude refused a call for its usage limit · resets ${clock} (in 2h 10m)`)
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
    expect(bars[0]).toHaveAccessibleName(`Codex 5-hour window, 42% used, 58% left, resets in 2h 10m, as of ${clock}`)
    expect(screen.getByTestId('usage-window-5h').title).toBe(`5-hour window: 42% used, 58% left · resets ${clockTime(new Date(at(130)))} (in 2h 10m) · as of ${clock}`)
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

  // Card #85: a machine that runs Codex but has not reported its limits shows a placeholder saying
  // when they will appear; an account with no window says so; hovering a row gives every window's
  // used and left shares and its reset; no money is ever shown (the daemon sends none).
  describe('what each provider row says', () => {
    const claude: UsageLimits = { provider: 'claude', windows: [{ id: '5h', used_percent: 20, resets_at: at(60) }, { id: '7d', used_percent: 64.4, resets_at: at(2 * 1440) }] }

    it('holds a place for a provider a connected machine runs and has not read yet, saying when it appears', () => {
      render(<UsageMeters daemons={[makeDaemon({ status: 'connected', available_engines: ['claude', 'codex', 'hermes'], usage_limits: [claude] })]} now={NOW} />)
      const row = screen.getByTestId('usage-row-codex')
      expect(row).toHaveTextContent('Codex')
      expect(within(row).getByTestId('usage-pending')).toHaveAccessibleName('Codex: no usage limits read yet')
      expect(row.title).toMatch(/^Codex: no usage limits from this machine yet\. A daemon from 0\.4\.5 on reads them when it connects, every 10 minutes/)
      // It promises nothing an older daemon will not keep.
      expect(row.title).toMatch(/An older daemon never fills this row/)
      expect(within(row).queryByRole('progressbar')).toBeNull()
      // Only the meter providers get one, and Claude, which reported, keeps its bars.
      expect(screen.queryByTestId('usage-row-hermes')).toBeNull()
      expect(within(screen.getByTestId('usage-row-claude')).getAllByRole('progressbar')).toHaveLength(2)
    })

    it('holds no place for a machine that is not connected or does not run the engine', () => {
      const { container } = render(<UsageMeters daemons={[
        makeDaemon({ id: 'off', status: 'disconnected', available_engines: ['codex'] }),
        makeDaemon({ id: 'on', status: 'connected', available_engines: ['hermes'] }),
      ]} now={NOW} />)
      expect(container).toBeEmptyDOMElement()
    })

    it('says a provider is not read on this machine (turned off, an unreadable sign-in), not that it will appear', () => {
      render(<UsageMeters daemons={[makeDaemon({ status: 'connected', available_engines: ['claude', 'codex'], usage_limits: [{ provider: 'claude', windows: [], note: 'not_read' }, { provider: 'codex', windows: [{ id: '5h', used_percent: 3, resets_at: at(60) }] }] })]} now={NOW} />)
      const row = screen.getByTestId('usage-row-claude')
      expect(within(row).getByTestId('usage-not-read')).toHaveAccessibleName('Claude: usage limits not read on this machine')
      expect(row.title).toMatch(/does not read its usage limits: turned off \(BLERG_RUNNER_CLAUDE_USAGE=0 for Claude\), or the engine's sign-in is not one it can read/)
      expect(row.title).not.toMatch(/reads them when it connects/)
      expect(within(row).queryByTestId('usage-pending')).toBeNull()
    })

    it('draws no not-read row for an engine the machine does not run', () => {
      const { container } = render(<UsageMeters daemons={[makeDaemon({ status: 'connected', available_engines: ['claude'], usage_limits: [{ provider: 'codex', windows: [], note: 'not_read' }] })]} now={NOW} />)
      expect(container.querySelector('[data-testid="usage-row-codex"]')).toBeNull()
    })

    it('explains a placeholder whose last reading\'s windows have all reset', () => {
      render(<UsageMeters daemons={[makeDaemon({ status: 'connected', available_engines: ['codex'], usage_limits: [{ provider: 'codex', windows: [{ id: '5h', used_percent: 40, resets_at: at(-5) }] }] })]} now={NOW} />)
      const row = screen.getByTestId('usage-row-codex')
      expect(within(row).getByTestId('usage-pending')).toBeInTheDocument()
      expect(row.title).toMatch(/^Codex: the last reading's windows have all reset\./)
    })

    it('says when the account has no window, instead of drawing nothing', () => {
      render(<UsageMeters daemons={[makeDaemon({ status: 'connected', available_engines: ['codex'], usage_limits: [{ provider: 'codex', windows: [], note: 'no_windows' }] })]} now={NOW} />)
      const row = screen.getByTestId('usage-row-codex')
      expect(within(row).getByTestId('usage-none')).toHaveAccessibleName('Codex: this account has no usage-limit window')
      expect(row.title).toMatch(/no usage-limit window to show/)
      expect(screen.queryByTestId('usage-pending')).toBeNull()
    })

    it('draws a Codex work plan\'s spend limit with its reset', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [{ provider: 'codex', windows: [{ id: 'spend', used_percent: 30, resets_at: at(10 * 1440) }] }] })]} now={NOW} />)
      const spend = screen.getByTestId('usage-window-spend')
      expect(spend).toHaveTextContent('spend')
      expect(spend.title).toMatch(/^Spend limit: 30% used, 70% left · resets .+ \(in 10d 0h\)$/)
    })

    it('gives the row a tooltip with every window\'s used and left shares and its reset, and no money', () => {
      render(<UsageMeters daemons={[makeDaemon({ usage_limits: [claude] })]} now={NOW} />)
      const row = screen.getByTestId('usage-row-claude')
      expect(row.title).toBe(`Claude · 5-hour window: 20% used, 80% left · resets ${clockTime(new Date(at(60)))} (in 1h 0m) · Weekly window: 64% used, 36% left · resets ${new Date(at(2 * 1440)).toLocaleDateString([], { weekday: 'short' })} ${clockTime(new Date(at(2 * 1440)))} (in 2d 0h)`)
      expect(row.title).not.toMatch(/[$€£]/)
    })
  })
})
