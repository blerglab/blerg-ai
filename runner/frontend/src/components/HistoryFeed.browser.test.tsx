// History's feed of strokes in real Chromium: on a phone (390 px) every line fits the drawer on one
// line with nothing wider than the screen; on a 4K screen (3840 px) the panel stays 340 px and every
// line still fits it; and a stroke's time jumps to its message in the conversation.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { strokesReply } from '../test/reelFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import type { TopicsReply } from '../lib/topics'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

/** The fixture's strokes, the decision jumping to the person's first message (rail1) and the open
 *  question to its card (rail23), both in the seeded conversation. */
function reply(): TopicsReply {
  const r = strokesReply(new Date())
  for (const s of r.strokes!) {
    if (s.id === 'st-d2') s.jump = { event_id: 'rail1', seq: 1 }
    else if (s.id === 'st-q') s.jump = { event_id: 'rail23', seq: 23 }
    else if (s.jump) s.jump = { ...s.jump, seq: 1 }
  }
  return r
}

beforeEach(() => {
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.setItem(RAIL_TAB_KEY, 'history')
  const { live } = seedRichRail()
  apiFetch.mockImplementation((url: string) => json(
    url.startsWith('/api/tool-changes') ? { tool_changes: [] }
      : url.endsWith('/topics') ? reply()
        : url.endsWith('/live') ? live
          : {},
  ))
  vi.stubGlobal('fetch', vi.fn(() => json([])))
})
afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

function renderPage() {
  render(
    <div style={{ height: '100vh' }}>
      <MemoryRouter initialEntries={['/sessions/sess-a']}>
        <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
      </MemoryRouter>
    </div>,
  )
}

const noOverflow = (width: number) => {
  expect(document.documentElement.clientWidth).toBe(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
}

/** Every stroke is one line: its text on one line, and the line inside the panel. */
function oneLineEach(panel: HTMLElement) {
  const p = panel.getBoundingClientRect()
  const strokes = within(panel).getAllByTestId('stroke')
  const all = [...strokes, ...within(panel).getAllByTestId('stroke-pin')]
  // Eight in the feed and the waiting question pinned above it (a marker stands in its place).
  expect(strokes.length).toBe(8)
  expect(within(panel).getAllByTestId('stroke-pin')).toHaveLength(1)
  for (const s of all) {
    const r = s.getBoundingClientRect()
    expect(r.left).toBeGreaterThanOrEqual(p.left)
    expect(r.right).toBeLessThanOrEqual(p.right + 0.5)
    const text = within(s).getByTestId('stroke-text')
    const lh = parseFloat(getComputedStyle(text).lineHeight)
    expect(text.getBoundingClientRect().height).toBeLessThanOrEqual(lh + 1)
  }
  for (const chip of within(panel).getAllByRole('button').filter(b => b.closest('.stroke-filters'))) {
    expect(chip.getBoundingClientRect().right).toBeLessThanOrEqual(p.right)
  }
}

describe('History feed on a phone (390 px)', () => {
  beforeEach(async () => { await page.viewport(390, 700) })

  it('fits every stroke and the filters in the drawer, one line each', async () => {
    renderPage()
    await userEvent.click(screen.getByTestId('rail-toggle'))
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(390))
    await within(drawer).findAllByTestId('stroke')
    oneLineEach(drawer)
    // A filter still fits: pressing one leaves its lines only.
    await userEvent.click(within(drawer).getByTestId('stroke-filter-decision'))
    expect(within(drawer).getAllByTestId('stroke').map(s => s.dataset.kind)).toEqual(['decision', 'decision', 'decision'])
    noOverflow(390)
  })
})

describe('History feed squeezed on a phone (390 px)', () => {
  beforeEach(async () => { await page.viewport(390, 700) })

  it('keeps a long title and a chip-heavy line readable: the meta and the chips give way, not the words', async () => {
    const r = reply()
    const yesterday = (h: number) => new Date(Date.now() - 26 * 3600_000 + h * 60_000).toISOString()
    const done = r.topics[1]
    done.title = 'Refactor the session store so a daemon restart keeps every agent session alive across the cluster'
    done.started_at = yesterday(0)
    done.ended_at = yesterday(48)
    done.counts = { documents: 12, agents: 38, commits: 14, cards: 6 }
    r.strokes!.unshift({
      id: 'st-heavy', kind: 'milestone', source: 'mark', by: 'agent', text: 'Merged and deployed the session store refactor',
      at: yesterday(50), seq: 1, topic_id: 'tp_open', jump: { event_id: 'rail1', seq: 1 }, card: { board_id: 'b1', number: 1234567 },
      commits: [{ sha: '1a2b3c4d5e', url: null }, { sha: '9f8e7d6c5b', url: null }, { sha: '0badc0ffee', url: null }],
    })
    apiFetch.mockImplementation((url: string) => json(url.endsWith('/topics') ? r : url.endsWith('/live') ? seedRichRail().live : {}))
    renderPage()
    await userEvent.click(screen.getByTestId('rail-toggle'))
    const drawer = await screen.findByRole('dialog', { name: 'Session panel' })
    await waitFor(() => expect(drawer.getBoundingClientRect().right).toBe(390))
    await within(drawer).findAllByTestId('stroke')
    const d = drawer.getBoundingClientRect()
    for (const head of within(drawer).getAllByTestId('reel-head')) {
      const title = head.querySelector('.rail-item-title')!.getBoundingClientRect()
      expect(title.width).toBeGreaterThan(120)
      for (const el of head.querySelectorAll('.reel-meta, .reel-count')) {
        expect(el.getBoundingClientRect().right).toBeLessThanOrEqual(d.right + 0.5)
      }
    }
    const heavy = within(drawer).getAllByTestId('stroke').find(s => s.textContent!.includes('session store refactor'))!
    const text = within(heavy).getByTestId('stroke-text')
    expect(text.getBoundingClientRect().width).toBeGreaterThan(90)
    expect(text.getBoundingClientRect().height).toBeLessThanOrEqual(parseFloat(getComputedStyle(text).lineHeight) + 1)
    for (const el of heavy.querySelectorAll('*')) {
      const b = el.getBoundingClientRect()
      if (b.width === 0) continue
      expect(b.right).toBeLessThanOrEqual(d.right + 0.5)
    }
    noOverflow(390)
  })
})

describe('History feed on a 4K screen (3840 px)', () => {
  beforeEach(async () => { await page.viewport(3840, 2000) })

  it('keeps the panel at 340 px with every line inside it, one line each', async () => {
    renderPage()
    const panel = await screen.findByTestId('session-rail')
    // An open question brings the panel up on Live: pick History.
    await userEvent.click(within(panel).getByRole('tab', { name: /History/ }))
    await within(panel).findAllByTestId('stroke')
    // The panel is a fixed 340 px on every screen (owner ruling, #122).
    expect([340, 341]).toContain(Math.round(panel.getBoundingClientRect().width)) // 340 px and its 1 px border
    oneLineEach(panel)
    noOverflow(3840)
  })

  it('jumps to the stroke’s message in the conversation', async () => {
    // A long conversation after the seeded one, so its first message is far above the view.
    const store = useTranscriptStore.getState()
    for (let i = 0; i < 60; i++) {
      store.ingest({ session_id: 'sess-a', client_event_id: `long${i}`, seq: 100 + i, ts: new Date().toISOString(), kind: 'assistant_text',
        payload: { text: `Step ${i}: ` + 'a sentence about the work that fills the line. '.repeat(6), done: true } } as AgentEvent)
    }
    renderPage()
    const panel = await screen.findByTestId('session-rail')
    // An open question brings the panel up on Live: pick History.
    await userEvent.click(within(panel).getByRole('tab', { name: /History/ }))
    await within(panel).findAllByTestId('stroke')
    const line = (t: string) => [...within(panel).getAllByTestId('stroke'), ...within(panel).getAllByTestId('stroke-pin')].find(s => s.textContent!.includes(t))!
    const target = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[data-event-id="rail1"]')
      expect(el).not.toBeNull()
      return el!
    })
    const scroller = (el: HTMLElement): HTMLElement => {
      let p = el.parentElement
      while (p && !(p.scrollHeight > p.clientHeight && /(auto|scroll)/.test(getComputedStyle(p).overflowY))) p = p.parentElement
      return p ?? document.documentElement
    }
    const box = scroller(target)
    // The conversation starts at its newest message: the first one is above the view.
    box.scrollTop = box.scrollHeight
    await waitFor(() => expect(target.getBoundingClientRect().bottom).toBeLessThan(box.getBoundingClientRect().top + 1))
    await userEvent.click(within(line('Use the existing sheet')).getByTestId('stroke-jump'))
    await waitFor(() => {
      const r = target.getBoundingClientRect()
      const b = box.getBoundingClientRect()
      expect(r.top).toBeGreaterThanOrEqual(b.top - 1)
      expect(r.bottom).toBeLessThanOrEqual(b.bottom + 1)
    })
    // And the question's line to its card.
    await userEvent.click(within(line('Which drawer width')).getByTestId('stroke-jump'))
    const ask = document.querySelector<HTMLElement>('[data-event-id="rail23"]')!
    await waitFor(() => {
      const r = ask.getBoundingClientRect()
      const b = scroller(ask).getBoundingClientRect()
      expect(r.top).toBeGreaterThanOrEqual(b.top - 1)
      expect(r.bottom).toBeLessThanOrEqual(b.bottom + 1)
    })
  })
})
