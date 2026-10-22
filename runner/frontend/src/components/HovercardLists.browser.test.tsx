// Card #129, in real Chromium with a real mouse (CDP), at 1280 and 3840 px: hovercards of chips in a
// vertical list open beside the list (to the left of the side panel), so walking the pointer down
// the list never lands on a card; while one is open the next chip's card opens at once (warm); a
// pointer heading for the card keeps it open.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { cdp, page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, useWorkingLineStore, type BackgroundTask, type TicketSummaryLive } from '@blerglab/chat'
import type { LiveChildSession } from '../lib/liveState'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { reelReply } from '../test/reelFixture'
import type { Stroke, TopicsReply } from '../lib/topics'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { railReady } from '../test/railReady'
import '@blerglab/chat/blerg.css'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (original) => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (original) => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn().mockResolvedValue({ ok: false, status: 404, json: async () => ({}) }) }))

const NOW = new Date()
function historyReply(): TopicsReply {
  const reply = reelReply(NOW)
  const at = (min: number) => new Date(NOW.getTime() - min * 60_000).toISOString()
  reply.strokes = Array.from({ length: 5 }, (_, i): Stroke => ({
    id: `st-${i}`, kind: 'milestone', source: 'mark', by: 'agent', text: `Milestone number ${i + 1} reached`, seq: 300 - i,
    at: at(i * 3 + 1), jump: { event_id: `ev-${i}`, seq: 300 - i }, card: { board_id: 'b1', board_name: 'Product', number: 140 + i, title: `History card ${i + 1}` },
  }))
  return reply
}
vi.mock('../hooks/useTopics', () => ({ useTopics: () => ({ status: 'ready', reply: historyReply() }) }))

const pin = (key: string, title: string, at: number): TicketSummaryLive => ({ source: 'tracker', key, title, status: 'In progress', url: '', eventId: `e-${key}`, updatedAt: at })
const pins = [1, 2, 3, 4, 5].map(i => pin(`#12${i}`, `Ticket number ${i}`, 100 - i))

function show(width: number) {
  render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width }}>
    <div style={{ flex: 1 }} data-testid="chat-area" />
    <SessionRail id="rail" session={makeSession({ status: 'running' })} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={() => {}}
      live={makeLive({ tickets: pins })} />
  </div></MemoryRouter>)
}

const LONG_STEP = 'Fast-forward main to the release branch after every check in the pipeline passes, then tag the release candidate'
function showRunning(width: number, step = LONG_STEP) {
  const session = makeSession({ status: 'running', engine: 'claude', model: 'claude-opus-5-5' })
  const now = Date.now()
  useWorkingLineStore.setState({ lines: { [session.id]: { since: now - 120_000, waitingOn: { tool: 'Bash', startedAt: now - 60_000, detail: step }, activity: null } } })
  const sub = (key: string, title: string, model = 'claude-sonnet-4-5') => ({ key, title, type: 'Explore', model, tokens: 1200, doing: `Read · src/${key}.ts`, startedAt: now - 90_000, background: false })
  const task: BackgroundTask = { taskId: 'b1', callId: 'c1', kind: 'bash', command: 'npm run build -- --watch', description: 'Watch the build', startedAt: now - 60_000, done: null }
  const child: LiveChildSession = { id: 'child-1', title: 'Parallel reviewer', engine: 'codex', model: 'gpt-5-codex', status: 'running',
    started_at: new Date(now - 120_000).toISOString(), asking: false, tokens: 3400, activity: 'Bash · go test ./...' }
  render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width }}>
    <div style={{ flex: 1 }} />
    <SessionRail id="rail" session={session} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={() => {}}
      tasks={[task]} childSessions={[child]} live={makeLive({ subagents: [sub('S1', 'Survey the layout'), sub('S2', 'Audit the tests', '')] })} />
  </div></MemoryRouter>)
}

type Mouse = { send(method: string, params?: object): Promise<unknown> }
let mouse: Mouse
let at = { x: 0, y: 0 }
const card = () => document.querySelector<HTMLElement>('[data-testid="hovercard"]')
const center = (el: Element) => { const r = el.getBoundingClientRect(); return { x: Math.round(r.left + Math.min(r.width / 2, 60)), y: Math.round(r.top + r.height / 2) } }
const within_ = (p: { x: number; y: number }, r: DOMRect) => p.x >= r.left && p.x <= r.right && p.y >= r.top && p.y <= r.bottom

/** Moves the real mouse to (x, y) of this page. The test page is a frame the runner scales to fit
 *  its window: CDP takes the window's coordinates. */
async function moveTo(x: number, y: number) {
  at = { x, y }
  const f = window.frameElement?.getBoundingClientRect()
  const k = f ? f.width / window.innerWidth : 1
  await mouse.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: (f?.left ?? 0) + x * k, y: (f?.top ?? 0) + y * k })
}
/** Moves in small steps; after each, the pointer must not be over a card unless `into` allows it. */
async function walk(to: { x: number; y: number }, opts: { step?: number; into?: boolean } = {}) {
  const from = at
  const n = Math.max(1, Math.ceil(Math.hypot(to.x - from.x, to.y - from.y) / (opts.step ?? 3)))
  for (let i = 1; i <= n; i++) {
    await moveTo(Math.round(from.x + ((to.x - from.x) * i) / n), Math.round(from.y + ((to.y - from.y) * i) / n))
    const c = card()
    if (c && !opts.into) expect(within_(at, c.getBoundingClientRect()), `the pointer at ${at.x},${at.y} is on a card`).toBe(false)
  }
}

/** Walks straight down the chips; each chip's card opens in turn, the first after the delay, the
 *  rest at once (warm), and the pointer never enters a card. */
/** Hovercards' time, the test's own: a card opens when the test says the delay has passed. */
let clock: ManualHoverClock
/** The open delay passes; the card is there. */
async function opened() {
  await railReady()
  clock.advance(HOVERCARD.openDelayMs)
  await expect.poll(() => card()?.style.visibility).toBe('visible')
}

async function walkDown(chips: HTMLElement[], keyOf: (i: number) => string, x?: number) {
  const first = { ...center(chips[0]), ...(x !== undefined ? { x } : {}) }
  await moveTo(first.x, first.y - 1)
  await moveTo(first.x, first.y)
  await opened()
  expect(card()!.textContent).toContain(keyOf(0))
  for (let i = 1; i < chips.length; i++) {
    const c = center(chips[i])
    await walk({ x: first.x, y: c.y })
    // At once: only the short warm dwell passes, far less than the open delay.
    clock.advance(HOVERCARD.warmDwellMs)
    await expect.poll(() => card()?.textContent ?? '').toContain(keyOf(i))
    await expect.poll(() => card()?.style.visibility).toBe('visible')
    // Beside the list: the card is not over the list's rows.
    const panel = screen.getByTestId('session-rail').getBoundingClientRect()
    expect(card()!.getBoundingClientRect().right).toBeLessThanOrEqual(panel.left)
  }
}

beforeEach(async () => {
  clock = installHoverClock()
  localStorage.clear()
  clearHovercardCache()
  mouse = cdp() as unknown as Mouse
  await moveTo(1, 1)
})
afterEach(async () => { await moveTo(1, 1); clock.uninstall(); useWorkingLineStore.setState({ lines: {} }) })

describe('Hovercards in lists, with a real mouse', () => {
  it.each([1280, 3840])('at %i px: walking down Tickets opens each card in turn, beside the panel, never under the pointer', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    show(width)
    await userEvent.click(screen.getByTestId('rail-pin-more'))
    await moveTo(1, 1)
    const chips = screen.getAllByTestId('rail-pin-open')
    expect(chips).toHaveLength(5)
    await walkDown(chips, i => `#12${i + 1}`)
    expect(card()!.dataset.side).toBe('left')
  })

  it.each([1280, 3840])('at %i px: walking down History\'s card chips does the same', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    show(width)
    await userEvent.click(screen.getByRole('tab', { name: /History/ }))
    await moveTo(1, 1)
    const chips = within(screen.getByTestId('session-rail')).getAllByTestId('stroke-card')
    expect(chips).toHaveLength(5)
    await walkDown(chips, i => `#${140 + i}`)
  })

  it.each([1280, 3840])('at %i px: a Running now row\'s card names the model in full and the whole step; walking down the rows switches at once', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    showRunning(width)
    await moveTo(1, 1)
    const rows = [...within(screen.getByTestId('rail-running')).getByRole('list').children] as HTMLElement[]
    expect(rows.map(r => r.dataset.testid)).toEqual(['rail-main', 'rail-subagent', 'rail-subagent', 'rail-task', 'rail-child'])
    // The row cuts the step short; its card has it whole, and the model by name, its id in the tooltip.
    const m = center(rows[0])
    await moveTo(m.x, m.y)
    await opened()
    const model = within(card()!).getByTestId('hovercard-run-model')
    expect(model.textContent).toContain('Claude Code · Opus 5.5')
    expect(model.querySelector('dd')!.getAttribute('title')).toBe('claude-opus-5-5')
    expect(within(card()!).getByTestId('hovercard-step').textContent).toContain(LONG_STEP)
    expect(within(card()!).getByTestId('hovercard-run-kind').textContent).toContain('Main agent')
    await moveTo(1, 1)
    clock.advance(HOVERCARD.headingMaxMs)
    await expect.poll(card).toBeNull()
    clock.advance(HOVERCARD.warmMs + 100)
    const x = Math.round(rows[0].getBoundingClientRect().left + 70)
    await walkDown(rows, i => ['Main', 'Survey the layout', 'Audit the tests', 'Watch the build', 'Parallel reviewer'][i], x)
    expect(within(card()!).getByTestId('hovercard-run-model').textContent).toContain('Codex')
    expect(within(card()!).getByTestId('hovercard-run-kind').textContent).toContain('Child session')
  })

  it.each([1280, 3840])('at %i px: a subagent\'s card always states its model: its own, or the one it inherits from Main', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    showRunning(width)
    await moveTo(1, 1)
    const subs = within(screen.getByTestId('rail-running')).getAllByTestId('rail-subagent')
    const own = center(subs[0])
    await moveTo(own.x, own.y)
    await opened()
    let model = within(card()!).getByTestId('hovercard-run-model').querySelector('dd')!
    expect(model.textContent).toBe('Sonnet 4.5')
    expect(model.getAttribute('title')).toBe('claude-sonnet-4-5')
    const inherits = center(subs[1])
    await moveTo(inherits.x, inherits.y)
    clock.advance(HOVERCARD.warmDwellMs)
    await expect.poll(() => card()?.textContent ?? '').toContain('Audit the tests')
    model = within(card()!).getByTestId('hovercard-run-model').querySelector('dd')!
    expect(model.textContent).toBe('Opus 5.5 (same as Main)')
    expect(model.getAttribute('title')).toBe('claude-opus-5-5')
  })

  it.each([1280, 3840])('at %i px: a 1 px staircase toward the card keeps it open', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    show(width)
    await userEvent.click(screen.getByTestId('rail-pin-more'))
    await moveTo(1, 1)
    // The last chip: nothing with a card of its own lies between it and its card's lower half (a
    // chip on the way takes over at once, by design).
    const chips = screen.getAllByTestId('rail-pin-open')
    const c0 = center(chips[4])
    await moveTo(c0.x, c0.y)
    await opened()
    const hc = card()!
    await expect.poll(() => hc.style.visibility).toBe('visible')
    // The card settles (its read lands) before the hand sets off.
    await expect.poll(() => hc.querySelector('.hc-loading')).toBeNull()
    const r = hc.getBoundingClientRect()
    const to = { x: Math.round(r.right - 20), y: Math.round(r.top + r.height * 0.7) }
    // One axis at a time, one pixel per move (a slow hand, a high-rate mouse).
    let { x, y } = at
    const dx = to.x - x, dy = to.y - y
    for (let i = 0; i < Math.abs(dx) + Math.abs(dy) && !(x === to.x && y === to.y); i++) {
      const moveX = Math.abs(to.x - x) / Math.abs(dx || 1) >= Math.abs(to.y - y) / Math.abs(dy || 1)
      if (moveX && x !== to.x) x += Math.sign(dx)
      else if (y !== to.y) y += Math.sign(dy)
      else x += Math.sign(dx)
      await moveTo(x, y)
      if (hc.matches(':hover')) break
      expect(card(), `closed at ${x},${y}`).toBe(hc)
    }
    clock.advance(HOVERCARD.headingMaxMs + HOVERCARD.graceMs)
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
    expect(card()).toBe(hc)
  })

  it.each([1280, 3840])('at %i px: a hand that starts down a little, then turns left to the card, keeps it open', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    show(width)
    await userEvent.click(screen.getByTestId('rail-pin-more'))
    await moveTo(1, 1)
    const chips = screen.getAllByTestId('rail-pin-open')
    const b = chips[4].getBoundingClientRect()
    const start = { x: Math.round(b.left + 60), y: Math.round(b.bottom - 2) }
    await moveTo(start.x, start.y - 6)
    await moveTo(start.x, start.y)
    await opened()
    const hc = card()!
    await expect.poll(() => hc.style.visibility).toBe('visible')
    for (let i = 1; i <= 3; i++) await moveTo(start.x, start.y + 2 * i)
    const r = hc.getBoundingClientRect()
    await walk({ x: Math.round(r.right - 10), y: start.y + 6 }, { into: true, step: 4 })
    clock.advance(HOVERCARD.headingMaxMs + HOVERCARD.graceMs)
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
    expect(card()).toBe(hc)
  })

  it('Tab onto the ticket chip inside a Running now row opens the ticket\'s card, not the row\'s', async () => {
    await page.viewport(1280, 900)
    const session = makeSession({ status: 'running' })
    const checklist = { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Build History fold · #122', done: false, in_progress: true, assignees: [{ kind: 'subagent' as const, id: 'S1' }] }] }
    render(<MemoryRouter><div style={{ display: 'flex', height: '100vh', width: 1280 }}><div style={{ flex: 1 }} />
      <SessionRail id="rail" session={session} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={() => {}}
        live={makeLive({ tickets: [pin('#122', 'Build History fold', 10)], checklist,
          subagents: [{ key: 'S1', title: 'History builder', type: 'general', model: '', tokens: null, doing: 'Edit', startedAt: Date.now(), background: false }] })} />
    </div></MemoryRouter>)
    const row = screen.getByTestId('rail-subagent')
    within(row).getByTestId('rail-work-item').focus()
    await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(within(row).getByTestId('rail-work-ticket'))
    clock.advance(HOVERCARD.openDelayMs)
    await expect.poll(() => card()?.dataset.kind).toBe('card')
    // Nothing else opens over it (the row's own card would have, with its own delay).
    clock.advance(HOVERCARD.openDelayMs * 2)
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
    expect(card()?.dataset.kind).toBe('card')
    expect(card()!.textContent).toContain('#122')
  })

  it.each([1280, 3840])('at %i px: walking down beside a tall card with a 20° drift toward it still switches to the next row within the warm dwell', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    // A long step: Main's card is tall, so a 20° drift down the list points into it.
    showRunning(width, Array.from({ length: 12 }, (_, i) => `${i + 1}. ${LONG_STEP}`).join('\n'))
    await moveTo(1, 1)
    const rows = [...within(screen.getByTestId('rail-running')).getByRole('list').children] as HTMLElement[]
    const from = { x: Math.round(rows[0].getBoundingClientRect().left + 120), y: center(rows[0]).y }
    await moveTo(from.x, from.y)
    await opened()
    const tall = card()!
    // Main's card (the whole step, the facts) is far taller than a row: the drift points into it.
    await expect.poll(() => tall.getBoundingClientRect().height).toBeGreaterThan(rows[0].getBoundingClientRect().height * 5)
    const to = center(rows[1]).y
    const drift = Math.tan((20 * Math.PI) / 180)
    for (let y = from.y + 3; y <= to; y += 3) await moveTo(Math.round(from.x - (y - from.y) * drift), y)
    clock.advance(HOVERCARD.warmDwellMs)
    await expect.poll(() => card()?.textContent ?? '').toContain('Survey the layout')
  })

  it.each([1280, 3840])('at %i px: moving diagonally into the card keeps it open; the next chip\'s card opens with no delay', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 900)
    show(width)
    await userEvent.click(screen.getByTestId('rail-pin-more'))
    await moveTo(1, 1)
    const chips = screen.getAllByTestId('rail-pin-open')
    const c0 = center(chips[4])
    await moveTo(c0.x, c0.y)
    await opened()
    const hc = card()!
    await expect.poll(() => hc.style.visibility).toBe('visible')
    const r = hc.getBoundingClientRect()
    // Down and to the left, into the card's lower half: through the safe triangle.
    await walk({ x: Math.round(r.right - 20), y: Math.round(r.top + r.height * 0.7) }, { into: true, step: 6 })
    clock.advance(HOVERCARD.headingMaxMs + HOVERCARD.graceMs)
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
    expect(card()).toBe(hc)
    // Back to the chip, then straight to the one above: its card is there at once.
    await walk(center(chips[4]), { into: true, step: 6 })
    const c1 = center(chips[3])
    await moveTo(c1.x, c1.y)
    // No open delay: the warm dwell alone.
    clock.advance(HOVERCARD.warmDwellMs)
    await expect.poll(() => card()?.textContent ?? '').toContain('#124')
  })
})
