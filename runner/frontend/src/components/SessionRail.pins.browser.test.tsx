// Card #127 in real Chromium, at 390 and 3840 px: Live's pin lines and Running now's work line stay
// compact and inside the 340 px panel; a hovercard opens over the page (not clipped by the panel)
// and stays on screen; a file's hovercard draws a Mermaid diagram with the viewer's sandboxed
// renderer, and nothing is read for a chip that is never hovered.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { cdp, page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, clearPreviewCache, type ArtifactInfo, type TicketSummaryLive, type TransportFiles } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { railReady } from '../test/railReady'
import '@blerglab/chat/blerg.css'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (original) => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (original) => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn().mockResolvedValue({ ok: false }) }))

const pin = (key: string, title: string, updatedAt: number, status = 'In progress'): TicketSummaryLive =>
  ({ source: 'tracker', key, title, status, url: '', eventId: `e-${key}`, updatedAt })
const tickets = [
  pin('#125', 'Pins on Live with a title long enough that it has to be cut in the panel', 50),
  pin('#123', 'Main first', 40, 'In review'),
  pin('#122', 'Build History fold', 30),
  pin('#110', 'Older', 20),
]
const MERMAID = 'graph TD\n  A[Start] --> B[Done]\n'
const files: ArtifactInfo[] = [
  { id: 'm1', name: 'flow.mmd', size: MERMAID.length, content_type: 'text/plain', view: 'text', version: 1, latest_version: 1, origin: 'agent', created_at: '2026-10-21T10:00:00Z' },
  { id: 'n1', name: 'notes.md', size: 20, content_type: 'text/markdown', view: 'markdown', version: 1, latest_version: 1, origin: 'agent', created_at: '2026-10-21T09:00:00Z' },
]

function show(width: number) {
  const raw = vi.fn(async (_s: string, id: string) => new Blob([id === 'm1' ? MERMAID : '# Notes\n'], { type: 'text/plain' }))
  const ops = { list: async () => files, raw, download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') } } as TransportFiles
  const now = Date.now()
  const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Build History fold · #122', done: false, in_progress: true, assignees: [{ kind: 'subagent' as const, id: 'S1' }] }] }
  render(<MemoryRouter><div style={{ display: 'flex', height: 900, width }}>
    <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}}
      live={makeLive({ tickets, checklist, files: { ops, items: files, status: 'ready' as never, refresh: () => {} },
        subagents: [{ key: 'S1', title: 'History builder', type: 'general', model: '', tokens: null, doing: 'Edit · src/History.tsx', startedAt: now - 30_000, background: false }] })} />
  </div></MemoryRouter>)
  return { raw }
}

const frame = () => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
let clock: ManualHoverClock
const hovercard = () => document.querySelector<HTMLElement>('[data-testid="hovercard"]')

beforeEach(async () => {
  clock = installHoverClock()
  localStorage.clear()
  clearHovercardCache()
  clearPreviewCache()
  const mouse = cdp() as unknown as { send(m: string, p?: object): Promise<unknown> }
  const f = window.frameElement?.getBoundingClientRect()
  await mouse.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: (f?.left ?? 0) + 2, y: (f?.top ?? 0) + 2 })
})
afterEach(() => clock.uninstall())

describe('Pins and hovercards, in Chromium', () => {
  it.each([390, 3840])('at %i px: pin lines and the work line are compact and inside the panel', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    show(width)
    const section = screen.getByTestId('rail-pins')
    const panel = section.getBoundingClientRect()
    const lines = within(section).getAllByTestId('rail-pin')
    expect(lines).toHaveLength(3)
    for (const l of lines) {
      const r = l.getBoundingClientRect()
      expect(r.height).toBeLessThan(28)
      expect(r.right).toBeLessThanOrEqual(panel.right + 0.5)
    }
    expect(within(section).getByTestId('rail-pin-more').textContent).toBe('+1 more')
    // A header, three lines and "+1 more": Running now is not pushed far down.
    expect(section.getBoundingClientRect().height).toBeLessThan(175)
    const work = within(screen.getByTestId('rail-subagent')).getByTestId('rail-run-work')
    expect(work.getBoundingClientRect().height).toBeLessThan(24)
    expect(work.textContent).toContain('#122')
    expect(work.getBoundingClientRect().right).toBeLessThanOrEqual(panel.right + 0.5)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it.each([390, 3840])('at %i px: hovering a pin opens its card over the page after the delay, on screen', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    show(width)
    // The panel at its narrowest (the owner's 340 px): the card is wider than it.
    if (width === 3840) screen.getByTestId('session-rail').style.width = '340px'
    const chip = screen.getAllByTestId('rail-pin-open')[0]
    // The drawer (390 px) slides in: the chip must stand still under the pointer before it rests on
    // it, however slowly a loaded machine runs the slide.
    await railReady()
    // The hovercard's clock is the test's: the delay is decided by it, not by the machine's load.
    await userEvent.hover(chip)
    clock.advance(HOVERCARD.openDelayMs - 1)
    await frame()
    expect(hovercard()).toBeNull()
    clock.advance(1)
    await expect.poll(hovercard).not.toBeNull()
    const card = hovercard()!
    expect(card.parentElement).toBe(document.body)
    expect(card.textContent).toContain('#125')
    expect(card.textContent).toContain('Pins on Live with a title long enough')
    await expect.poll(() => card.style.visibility).toBe('visible')
    const r = card.getBoundingClientRect()
    expect(r.left).toBeGreaterThanOrEqual(0)
    expect(r.right).toBeLessThanOrEqual(width)
    expect(r.bottom).toBeLessThanOrEqual(window.innerHeight)
    if (width === 3840) {
      // Wider than the 340 px panel: drawn outside it, not clipped by it.
      const panel = screen.getByTestId('session-rail').getBoundingClientRect()
      expect(panel.width).toBeLessThanOrEqual(341)
      expect(r.width).toBeGreaterThan(panel.width)
      expect(r.right).toBeGreaterThan(panel.right)
    } else {
      expect(r.width).toBeLessThanOrEqual(width - 16 + 0.5)
    }
    // Leaving it, away from the card, closes it.
    const away = cdp() as unknown as { send(m: string, p?: object): Promise<unknown> }
    const f = window.frameElement?.getBoundingClientRect()
    await away.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: (f?.left ?? 0) + 2, y: (f?.top ?? 0) + 2 })
    clock.advance(HOVERCARD.headingMaxMs)
    await expect.poll(hovercard).toBeNull()
  })

  it('a file\'s hovercard draws its Mermaid diagram (the sandboxed renderer); an unhovered file is never read', async () => {
    await page.viewport(1600, 1000)
    const { raw } = show(1600)
    await userEvent.click(screen.getByRole('tab', { name: /Files/ }))
    const row = await screen.findByRole('button', { name: 'View flow.mmd' })
    expect(raw).not.toHaveBeenCalled()
    await userEvent.hover(row)
    clock.advance(HOVERCARD.openDelayMs)
    await expect.poll(hovercard).not.toBeNull()
    const card = hovercard()!
    await expect.poll(() => card.querySelector('img.visual-diagram, [data-testid="visual-failed"]'), { timeout: 30_000 }).not.toBeNull()
    expect(card.querySelector('img.visual-diagram')).not.toBeNull()
    expect(within(card).getByTestId('hovercard-name').textContent).toBe('flow.mmd')
    expect(within(card).getByTestId('hovercard-publisher').textContent).toContain('by the agent')
    expect(raw.mock.calls.map(c => c[1])).toEqual(['m1'])
    const r = card.getBoundingClientRect()
    expect(r.right).toBeLessThanOrEqual(1600)
    expect(r.bottom).toBeLessThanOrEqual(1000)
  }, 40_000)
})
