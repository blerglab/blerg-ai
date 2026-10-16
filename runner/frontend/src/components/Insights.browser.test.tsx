// The Insights page on a phone, in real Chromium: layout (what is wider than the screen, where a
// tooltip lands) only exists in a real browser. The owner's ask: the page must look right on a phone.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import '../index.css'
import { richInsights, richPrices } from '../test/insightsFixture'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import Insights from './Insights'

const PHONE = { width: 390, height: 700 }

beforeEach(async () => {
  apiFetch.mockImplementation((url: string) => {
    const body = url.startsWith('/api/insights/prices') ? richPrices : richInsights
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response)
  })
  await page.viewport(PHONE.width, PHONE.height)
})

async function renderPage() {
  render(<MemoryRouter><Insights /></MemoryRouter>)
  // the stored prices arrive after the report: a Remove button means every section is on screen
  await screen.findByRole('button', { name: /remove claude-opus-5-5/i })
}

describe('Insights on a phone (390 px)', () => {
  it('is never wider than the screen: tables stack, the price editor scrolls inside its section', async () => {
    await renderPage()
    expect(screen.getByTestId('tokens-stack')).toBeTruthy()
    expect(screen.getByTestId('startup-stack')).toBeTruthy()
    expect(document.documentElement.clientWidth).toBe(PHONE.width)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(PHONE.width)
    const prices = screen.getByTestId('prices-scroll')
    expect(prices.getBoundingClientRect().right).toBeLessThanOrEqual(PHONE.width)
    expect(prices.scrollWidth).toBeGreaterThan(prices.clientWidth)
  })

  it('a tap opens a step\'s tooltip inside the screen; a second tap, Esc or a tap elsewhere closes it', async () => {
    await renderPage()
    const row = screen.getByTestId('step-engine')
    const name = within(row).getByRole('button', { name: 'Starting agent' })
    // the row sits at the bottom of the screen: the tooltip has no room beneath it and opens above
    row.scrollIntoView({ block: 'end' })
    await userEvent.click(name)
    const tip = await screen.findByRole('tooltip')
    expect(name.getAttribute('aria-expanded')).toBe('true')
    const r = tip.getBoundingClientRect()
    expect(r.left).toBeGreaterThanOrEqual(0)
    expect(r.right).toBeLessThanOrEqual(PHONE.width)
    expect(r.top).toBeGreaterThanOrEqual(0)
    expect(r.bottom).toBeLessThanOrEqual(PHONE.height)
    expect(r.bottom).toBeLessThanOrEqual(row.getBoundingClientRect().top)

    await userEvent.click(name)
    await waitFor(() => expect(screen.queryByRole('tooltip')).toBeNull())
    expect(name.getAttribute('aria-expanded')).toBe('false')

    await userEvent.click(name)
    await screen.findByRole('tooltip')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('tooltip')).toBeNull())

    await userEvent.click(name)
    await screen.findByRole('tooltip')
    await userEvent.click(screen.getByRole('heading', { name: 'Pod startup' }))
    await waitFor(() => expect(screen.queryByRole('tooltip')).toBeNull())
  })
})
