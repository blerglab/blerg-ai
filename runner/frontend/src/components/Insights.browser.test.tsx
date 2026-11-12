// The Insights page on a phone, in real Chromium: layout (what is wider than the screen, where a
// tooltip lands) only exists in a real browser. The owner's ask: the page must look right on a phone.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import '../index.css'
import { richActivity } from '../test/activityFixture'
import { richInsights, richPrices } from '../test/insightsFixture'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))

import { InsightsDashboard as Insights } from './Insights'

const PHONE = { width: 390, height: 700 }

beforeEach(async () => {
  apiFetch.mockClear()
  apiFetch.mockImplementation((url: string) => {
    const body = url.startsWith('/api/insights/activity') ? richActivity : url.startsWith('/api/insights/prices') ? richPrices : richInsights
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response)
  })
  await page.viewport(PHONE.width, PHONE.height)
})

async function renderPage() {
  render(<MemoryRouter><Insights /></MemoryRouter>)
  await screen.findByTestId('question-health')
  await userEvent.selectOptions(screen.getByRole('combobox',{name:'Insights view'}),'spending')
  await userEvent.click(await screen.findByText('Model prices',{selector:'summary'}))
  // the stored prices arrive after the report: a Remove button means every section is on screen
  await screen.findByRole('button', { name: /remove claude-opus-5-5/i })
}

describe('Insights on a phone (390 px)', () => {
 it('a tap opens a step tooltip inside the screen; a second tap, Escape or an outside tap closes it',async()=>{
  render(<MemoryRouter><Insights/></MemoryRouter>)
  const row=await screen.findByTestId('step-engine')
  const name=within(row).getByRole('button',{name:'Starting agent'})
  row.scrollIntoView({block:'end'})
  await userEvent.click(name)
  const tip=await screen.findByRole('tooltip'),r=tip.getBoundingClientRect()
  expect(name).toHaveAttribute('aria-expanded','true')
  expect(r.left).toBeGreaterThanOrEqual(0);expect(r.right).toBeLessThanOrEqual(PHONE.width)
  expect(r.top).toBeGreaterThanOrEqual(0);expect(r.bottom).toBeLessThanOrEqual(PHONE.height)
  await userEvent.click(name);await waitFor(()=>expect(screen.queryByRole('tooltip')).toBeNull())
  await userEvent.click(name);await screen.findByRole('tooltip');await userEvent.keyboard('{Escape}')
  await waitFor(()=>expect(screen.queryByRole('tooltip')).toBeNull())
  await userEvent.click(name);await screen.findByRole('tooltip')
  await userEvent.click(screen.getByRole('heading',{name:'Pod startup'}))
  await waitFor(()=>expect(screen.queryByRole('tooltip')).toBeNull())
 })
  it('is never wider than the screen: tables stack, the price editor scrolls inside its section', async () => {
    await renderPage()
    expect(screen.getByRole('figure',{name:'Estimated spending'})).toBeTruthy()
    // Native scrollbars consume viewport width; the content must fit the usable width.
    expect(window.innerWidth).toBe(PHONE.width)
    const usableWidth = document.documentElement.clientWidth
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(usableWidth)
    const prices = screen.getByTestId('prices-scroll')
    expect(prices.getBoundingClientRect().right).toBeLessThanOrEqual(usableWidth)
    expect(prices.scrollWidth).toBeGreaterThan(prices.clientWidth)
  })

})


describe('Insights question layout', () => {
 it.each([390,1024,3840])('keeps questions and a single chart inside %i px',async width=>{
  await page.viewport(width,width===3840?2160:900)
  const view=render(<MemoryRouter initialEntries={['/insights/flow']}><div style={{height:'100vh'}}><Insights/></div></MemoryRouter>)
  await screen.findByRole('heading',{name:'What does a card really cost?'})
  const content=view.container.querySelector('.insights-tab')!
  expect(content.scrollWidth).toBeLessThanOrEqual(content.clientWidth)
  content.scrollTop=200
  if(width===390)await userEvent.selectOptions(screen.getByRole('combobox',{name:'Insights view'}),'time');else await page.getByRole('button',{name:'Time',exact:true}).click()
  await screen.findByRole('heading',{name:'Where does the time go?'})
  expect(content.scrollTop).toBe(0)
  expect(view.container.querySelectorAll('.time-chart')).toHaveLength(1)
  if(width===390)await userEvent.selectOptions(screen.getByRole('combobox',{name:'Insights view'}),'cost');else await page.getByRole('button',{name:'Card cost',exact:true}).click()
  await screen.findByRole('heading',{name:'What does a card really cost?'})
  expect(apiFetch.mock.calls.filter(c=>String(c[0]).startsWith('/api/insights/activity')).length).toBe(1)
  expect(screen.queryByRole('heading',{name:'Pod startup'})).toBeNull()
 })
})
