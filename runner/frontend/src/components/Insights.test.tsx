import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import Insights, { NARROW_QUERY } from './Insights'
import type { InsightsResponse, ModelPrice } from '../lib/insights'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const q = (p50: number, p90 = p50, max = p90, count = 1) => ({ count, p50, p90, max })
const zeroTok = { turns: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, cost_usd: null }

function report(over: Partial<InsightsResponse> = {}): InsightsResponse {
  return {
    range: { label: '7d', from: '2026-09-25T00:00:00Z', to: '2026-10-02T00:00:00Z' },
    scope: 'all',
    is_admin: true,
    generated_at: '2026-10-02T00:00:00Z',
    sessions: {
      started: 12, ended: 8, alive: 3,
      by_runtime: [{ key: 'cluster', count: 11 }, { key: 'daemon', count: 1 }],
      by_outcome: [{ key: 'stopped_by_user', count: 5 }, { key: 'not_resumed', count: 3 }],
      lifetime_seconds: q(9360, 20000, 90000, 8),
      running_seconds: 3600, waiting_seconds: 600, idle_seconds: 6000, busy_fraction: 0.35,
      by_day: [{ day: '2026-09-30', started: 5 }, { day: '2026-10-01', started: 7 }],
    },
    startup: {
      groups: [{ runtime: 'cluster', kind: 'start', attempts: 10, failed: 1, seconds: q(42.3, 110.7, 1211, 9) }],
      // the slowest start at any step is 60 s, so the shared axis runs 0 to 60 s in 10 s ticks
      steps: [
        { runtime: 'cluster', step: 'schedule', count: 9, min: 1, p25: 1.5, p50: 2, p75: 3, p90: 6, max: 9, slow: 1 },
        { runtime: 'cluster', step: 'clone', count: 9, min: 5, p25: 15, p50: 30, p75: 45, p90: 55, max: 60, slow: 0 },
        { runtime: 'cluster', step: 'engine', count: 3, min: 6, p25: 7, p50: 8, p75: 10, p90: 11.6, max: 12, slow: 0 },
      ],
      failures: [{ key: 'queued', count: 1 }],
      slowest: [{ session_id: 'sess-slow', title: 'the slow one', runtime: 'cluster', seconds: 1211, at: '2026-10-01T10:00:00Z' }],
    },
    tokens: {
      totals: { turns: 212, input: 7000, output: 440000, cache_read: 61000000, cache_write: 900000, cost_usd: 4.2 },
      cache_hit_rate: 0.98,
      by_model: [
        { model: 'm-one', priced: true, turns: 150, input: 5000, output: 300000, cache_read: 40000000, cache_write: 600000, cost_usd: 4.2 },
        { model: 'm-two', priced: false, turns: 62, input: 2000, output: 140000, cache_read: 21000000, cache_write: 300000, cost_usd: null },
      ],
      daily: [
        { day: '2026-09-30', ...zeroTok, turns: 100, input: 3000, output: 200000, cache_read: 30000000 },
        { day: '2026-10-01', ...zeroTok, turns: 112, input: 4000, output: 240000, cache_read: 31000000 },
      ],
      top_sessions: [{ session_id: 'sess-top', title: 'the big one', runtime: 'cluster', started_at: '2026-10-01T09:00:00Z', ...zeroTok, turns: 40, output: 90000, cost_usd: 1.5 }],
      unpriced_models: ['m-two'],
    },
    cluster: { active: 3, max: 12, peak_concurrent: 5 },
    crons: { runs: 4, late: 1, manual: 0, by_status: [{ key: 'started', count: 3 }, { key: 'failed', count: 1 }], seconds: q(300, 600, 900, 3) },
    ...over,
  }
}

function json(body: unknown, status = 200) {
  return Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as Response)
}

let current: InsightsResponse
let prices: ModelPrice[]

beforeEach(() => {
  vi.clearAllMocks()
  current = report()
  prices = []
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    if (url.startsWith('/api/insights/prices')) {
      if (init?.method === 'PUT' || init?.method === 'DELETE') return Promise.resolve({ ok: true, status: 204 } as Response)
      return json(prices)
    }
    if (url.startsWith('/api/insights')) return json(current)
    return json({}, 404)
  })
})

const renderPage = () => render(<MemoryRouter><Insights /></MemoryRouter>)
const urls = () => apiFetch.mock.calls.map((c) => c[0] as string)

describe('Insights', () => {
  it('shows the headline numbers once loaded', async () => {
    renderPage()
    expect(screen.getByRole('status')).toHaveTextContent(/loading/i)
    expect(await screen.findByTestId('card-started')).toHaveTextContent('12')
    expect(screen.getByTestId('card-startup')).toHaveTextContent('42 s')
    expect(screen.getByTestId('card-tokens')).toHaveTextContent('440k') // output tokens, the number people ask about
    expect(screen.getByTestId('card-cost')).toHaveTextContent('$4.20')
    expect(screen.getByTestId('card-cache')).toHaveTextContent('98%')
  })

  it('breaks a start down by step, in order, each with its count, slowest and slow starts', async () => {
    renderPage()
    const steps = await screen.findByTestId('start-steps')
    const rows = ['schedule', 'clone', 'engine'].map((k) => within(steps).getByTestId(`step-${k}`).textContent ?? '')
    expect(rows[0]).toMatch(/Scheduling.*n 9 · max 9 s · 1 slow/)
    expect(rows[1]).toMatch(/Cloning repo.*n 9 · max 1 min$/) // no "0 slow"
    expect(rows[2]).toMatch(/Starting agent.*n 3 · max 12 s · too few starts for a box/)
    expect(within(steps).getAllByTestId(/^step-/).map((li) => li.dataset.testid)).toEqual(['step-schedule', 'step-clone', 'step-engine'])
  })

  it('names every mark in one legend, with what slow means', async () => {
    renderPage()
    const legend = (await screen.findByTestId('start-steps-legend')).textContent ?? ''
    expect(legend).toMatch(/middle half of starts \(25th–75th percentile\)/)
    expect(legend).toMatch(/median/)
    expect(legend).toMatch(/90th percentile/)
    expect(legend).toMatch(/fastest to slowest/)
    expect(legend).toMatch(/slow = more than twice the median/)
  })

  it('draws the whisker, box, median and 90th mark in proportion on the shared axis', async () => {
    renderPage()
    const clone = await screen.findByTestId('step-clone')
    const part = (name: string) => clone.querySelector<HTMLElement>(`[data-part="${name}"]`)!.style
    const num = (v: string) => parseFloat(v)
    expect(num(part('whisker').left)).toBeCloseTo((5 / 60) * 100, 2)
    expect(num(part('whisker').width)).toBeCloseTo((55 / 60) * 100, 2)
    expect(num(part('box').left)).toBeCloseTo(25, 2)
    expect(num(part('box').width)).toBeCloseTo(50, 2)
    expect(num(part('median').left)).toBeCloseTo(50, 2)
    expect(num(part('p90').left)).toBeCloseTo((55 / 60) * 100, 2)
    const ticks = Array.from(screen.getByTestId('start-steps').querySelectorAll('.sb-tick')).map((t) => t.textContent)
    expect(ticks).toEqual(['0', '10 s', '20 s', '30 s', '40 s', '50 s', '1 min'])
    expect(screen.getByText('linear scale')).toBeInTheDocument()
  })

  it('draws a step with under five starts as its fastest, median and slowest dots, not a box', async () => {
    renderPage()
    const engine = await screen.findByTestId('step-engine')
    expect(engine.querySelector('[data-part="box"]')).toBeNull()
    expect(engine.querySelector('[data-part="p90"]')).toBeNull()
    const dots = Array.from(engine.querySelectorAll<HTMLElement>('[data-part="dot"]')).map((d) => parseFloat(d.style.left))
    expect(dots).toHaveLength(3)
    ;[6, 8, 12].forEach((v, i) => expect(dots[i]).toBeCloseTo((v / 60) * 100, 2))
  })

  it('shows every number for a step on hover and on keyboard focus', async () => {
    renderPage()
    const clone = await screen.findByTestId('step-clone')
    expect(screen.queryByRole('tooltip')).toBeNull()
    fireEvent.mouseEnter(clone)
    const tip = screen.getByRole('tooltip')
    expect(within(clone).getByRole('button')).toHaveAttribute('aria-describedby', tip.id)
    const lines = within(tip).getAllByRole('listitem').map((li) => li.textContent)
    expect(lines).toEqual([
      '5 s fastest', '15 s 25th percentile', '30 s median', '45 s 75th percentile', '55 s 90th percentile', '1 min slowest',
      '9 starts', '0 slow (over 1 min)',
    ])
    fireEvent.mouseLeave(clone)
    expect(screen.queryByRole('tooltip')).toBeNull()
    fireEvent.focus(screen.getByTestId('step-schedule'))
    expect(screen.getByRole('tooltip')).toHaveTextContent(/1 slow \(over 4 s\)/)
  })

  it('pins a step\'s tooltip on a click or tap; a second tap, Esc or a press elsewhere closes it', async () => {
    renderPage()
    const row = await screen.findByTestId('step-clone')
    const name = within(row).getByRole('button', { name: 'Cloning repo' })
    expect(name).toHaveAttribute('aria-expanded', 'false')
    // a tap on a touch screen: an emulated hover and focus, then the click
    fireEvent.mouseEnter(row)
    fireEvent.focus(name)
    fireEvent.click(name)
    expect(name).toHaveAttribute('aria-expanded', 'true')
    expect(name).toHaveAttribute('aria-controls', screen.getByRole('tooltip').id)
    // pinned: leaving the row keeps it open
    fireEvent.mouseLeave(row)
    expect(screen.getByRole('tooltip')).toBeInTheDocument()
    fireEvent.click(name)
    expect(screen.queryByRole('tooltip')).toBeNull()
    expect(name).toHaveAttribute('aria-expanded', 'false')

    fireEvent.click(name)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('tooltip')).toBeNull()

    fireEvent.click(name)
    fireEvent.pointerDown(within(row).getByRole('tooltip'))
    expect(screen.getByRole('tooltip')).toBeInTheDocument()
    fireEvent.pointerDown(document.body)
    expect(screen.queryByRole('tooltip')).toBeNull()
  })

  it('stacks the tokens and startup tables into one entry per row on a narrow screen', async () => {
    const wide = window.matchMedia
    window.matchMedia = vi.fn().mockImplementation((query: string) => ({
      matches: query === NARROW_QUERY, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }))
    try {
      renderPage()
      const tokens = await screen.findByTestId('tokens-stack')
      const entries = within(tokens).getAllByRole('listitem')
      expect(entries).toHaveLength(2)
      expect(within(entries[0]).getByText('m-one')).toBeInTheDocument()
      const figs = (li: HTMLElement) => Array.from(li.querySelectorAll('dt')).map((dt) => `${dt.textContent} ${dt.nextElementSibling?.textContent}`)
      expect(figs(entries[0])).toEqual(['Turns 150', 'Input 5k', 'Output 300k', 'Cache read 40M', 'Cache write 600k', 'Est. cost $4.20'])
      expect(figs(entries[1])).toContain('Est. cost no price')
      const startup = screen.getByTestId('startup-stack')
      expect(within(startup).getByRole('listitem')).toHaveTextContent(/cluster · start.*10 attempts, 1 failed/)
      expect(screen.queryAllByRole('table').map((t) => t.closest('[data-testid="prices-scroll"]'))).not.toContain(null)
    } finally {
      window.matchMedia = wide
    }
  })

  it('links the slowest starts and the biggest token users to their sessions', async () => {
    renderPage()
    expect(await screen.findByRole('link', { name: /the slow one/ })).toHaveAttribute('href', '/sessions/sess-slow')
    expect(screen.getByRole('link', { name: /the big one/ })).toHaveAttribute('href', '/sessions/sess-top')
  })

  it('refetches when the range changes', async () => {
    renderPage()
    await screen.findByTestId('card-started')
    fireEvent.click(screen.getByRole('button', { name: '30d' }))
    await waitFor(() => expect(urls().some((u) => u.includes('range=30d'))).toBe(true))
  })

  it('tells an empty range from a broken one', async () => {
    current = report({
      sessions: { ...report().sessions, started: 0, ended: 0, alive: 0, by_runtime: [], by_outcome: [], by_day: [] },
      startup: { groups: [], steps: [], failures: [], slowest: [] },
      tokens: { totals: { ...zeroTok, cost_usd: null }, cache_hit_rate: 0, by_model: [], daily: [], top_sessions: [], unpriced_models: [] },
    })
    renderPage()
    expect(await screen.findByText(/No sessions in this range/i)).toBeInTheDocument()

    apiFetch.mockImplementation(() => json({ error: 'boom' }, 500))
    fireEvent.click(screen.getByRole('button', { name: '24h' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not load/i)
    expect(screen.getByRole('button', { name: /try again/i })).toBeInTheDocument()
  })

  describe('as a member', () => {
    beforeEach(() => { current = report({ is_admin: false, scope: 'mine' }) })

    it('has no scope switch and no price editor, and says where estimates come from', async () => {
      renderPage()
      await screen.findByTestId('card-started')
      expect(screen.queryByRole('button', { name: /everyone/i })).toBeNull()
      expect(screen.queryByTestId('price-editor')).toBeNull()
      expect(screen.getByText(/your own sessions/i)).toBeInTheDocument()
    })
  })

  describe('as an administrator', () => {
    it('can switch between everyone and just their own sessions', async () => {
      renderPage()
      await screen.findByTestId('card-started')
      fireEvent.click(screen.getByRole('button', { name: /^mine$/i }))
      await waitFor(() => expect(urls().some((u) => u.includes('scope=mine'))).toBe(true))
    })

    it('offers a price for each model that has none, and saves it per million tokens', async () => {
      renderPage()
      const editor = await screen.findByTestId('price-editor')
      expect(within(editor).getByText('m-two')).toBeInTheDocument()
      fireEvent.change(within(editor).getByLabelText(/input.*m-two/i), { target: { value: '3' } })
      fireEvent.change(within(editor).getByLabelText(/output.*m-two/i), { target: { value: '15' } })
      fireEvent.change(within(editor).getByLabelText(/cache read.*m-two/i), { target: { value: '0.3' } })
      fireEvent.change(within(editor).getByLabelText(/cache write.*m-two/i), { target: { value: '3.75' } })
      fireEvent.click(within(editor).getByRole('button', { name: /save m-two/i }))
      await waitFor(() => {
        const put = apiFetch.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PUT')
        expect(put).toBeTruthy()
        expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({
          model: 'm-two', input_per_mtok: 3, output_per_mtok: 15, cache_read_per_mtok: 0.3, cache_write_per_mtok: 3.75,
        })
      })
      // and the report is read again so the estimate appears
      await waitFor(() => expect(urls().filter((u) => u.startsWith('/api/insights?')).length).toBeGreaterThan(1))
    })

    it('refuses a negative or empty price before sending anything', async () => {
      renderPage()
      const editor = await screen.findByTestId('price-editor')
      fireEvent.change(within(editor).getByLabelText(/input.*m-two/i), { target: { value: '-1' } })
      fireEvent.click(within(editor).getByRole('button', { name: /save m-two/i }))
      expect(await within(editor).findByRole('alert')).toHaveTextContent(/zero or more/i)
      expect(apiFetch.mock.calls.some((c) => (c[1] as RequestInit | undefined)?.method === 'PUT')).toBe(false)
    })

    it('can remove a stored price', async () => {
      prices = [{ model: 'm-one', input_per_mtok: 3, output_per_mtok: 15, cache_read_per_mtok: 0.3, cache_write_per_mtok: 3.75 }]
      renderPage()
      const editor = await screen.findByTestId('price-editor')
      fireEvent.click(await within(editor).findByRole('button', { name: /remove m-one/i }))
      await waitFor(() => {
        const del = apiFetch.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'DELETE')
        expect(del?.[0]).toBe('/api/insights/prices?model=m-one')
      })
    })
  })
})
