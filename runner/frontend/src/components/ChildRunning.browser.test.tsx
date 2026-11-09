import { initializePersonTime } from '../lib/personTime'
import { apiFetch } from '../apiFetch'
import { browserWidths } from '../test/browserWidths'
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { ChildSessionRow, StartedSessions } from './ChildSessions'
import type { LiveChildSession } from '../lib/liveState'
import '../index.css'
import '@blerglab/chat/blerg.css'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import './SessionRail.css'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../ws', () => import('../test/wsMock'))

const now = Date.parse('2026-10-19T10:02:00Z')
vi.spyOn(Date, 'now').mockReturnValue(now)
const base: LiveChildSession = { id: 'c1', title: 'Review the runner change set with a long title', engine: 'codex', model: 'fixture-model', effort: 'high', status: 'running', started_at: '2026-10-19T10:00:00Z', asking: false, tokens: 1000, activity: 'Bash · npm test', activity_since: '2026-10-19T10:01:30Z' }
const stopped: LiveChildSession = { ...base, id: 'c2', title: 'Finished worker', status: 'idle', activity: undefined }

// A running child is one card, in Running now, with everything; Started sessions has one line that
// brings it into view, and lists the child that stopped.
describe('a running child shows once, in Chromium', () => {
  it.each(browserWidths([390, 3840]))('at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const show = vi.fn()
    const { container, getByTestId, getAllByTestId } = render(<MemoryRouter><div style={{ width: 'min(420px, 100%)' }}>
      <section data-testid="running"><ul className="rail-list"><ChildSessionRow variant="run" child={base} clockOffset={0} ticking={false} parentId="parent" /></ul></section>
      <StartedSessions sessionId="parent" children={[base, stopped]} clockOffset={0} active={false} onShowRunning={show} />
    </div></MemoryRouter>)
    expect(getAllByTestId('rail-child')).toHaveLength(2)
    const card = getByTestId('running').querySelector('[data-testid="rail-child"]') as HTMLElement
    // Its words are the title's description (the row's hovercard shows them; card #129).
    expect(card.querySelector('[data-testid="rail-child-open"]')!.getAttribute('aria-description')).toContain('effort: high')
    expect(card.textContent).toContain('Bash · npm test')
    expect(card.querySelector(".rail-clock")?.textContent).toBe("2 min")
    // The current activity keeps its own clock, on the step line.
    expect(card.querySelector('.rail-run-second .rail-clock')?.getAttribute('title')).toBe('Current activity elapsed')
    expect(card.querySelectorAll('.rail-run-line')).toHaveLength(2)
    expect(card.getBoundingClientRect().height).toBeLessThan(64)
    const button = card.querySelector('.rail-run-line')!.getBoundingClientRect()
    const discard = card.querySelector('.child-discard-trigger')!.getBoundingClientRect()
    expect(discard.left).toBeGreaterThanOrEqual(button.right)
    expect(discard.right).toBeLessThanOrEqual(width)
    const started = getByTestId('rail-started')
    expect(started.querySelectorAll('[data-testid="rail-child"]')).toHaveLength(1)
    expect(started.textContent).toContain('Finished worker')
    const line = getByTestId('rail-started-running')
    expect(line.textContent).toBe('1 running ↑')
    await userEvent.click(line)
    expect(show).toHaveBeenCalledOnce()
    expect(container.scrollWidth).toBeLessThanOrEqual(width)
  })
})

// An idle worker with a long card assignment in a narrow panel (owner screenshot 2026-11-05):
// the glyph line stays inside its card, and the assignment truncates instead of overflowing.
describe('an idle worker card with a long assignment, in Chromium', () => {
  it.each(browserWidths([390, 3840]))('stays inside its card at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const idle: LiveChildSession = { ...stopped, id: 'c3', title: 'BLG-501 cluster workers from desktop', tokens: 389_300_000 }
    const assignment = <span className="rail-work"><span aria-hidden="true">↳</span><span>BLG-479 + BLG-501 cluster workers from desktop and import</span></span>
    const { getByTestId } = render(<MemoryRouter><div style={{ width: '320px' }}><section data-testid="started-list"><ul className="rail-list">
      <ChildSessionRow variant="run" child={idle} clockOffset={0} ticking={false} parentId="parent" assignment={assignment} />
    </ul></section></div></MemoryRouter>)
    const card = getByTestId('started-list').querySelector<HTMLElement>('[data-testid="rail-child"]')!
    const second = card.querySelector<HTMLElement>('.rail-run-second')!
    expect(second.scrollWidth).toBeLessThanOrEqual(second.clientWidth + 1)
    expect(card.getBoundingClientRect().right).toBeLessThanOrEqual(320 + 1)
    const work = second.querySelector<HTMLElement>('.rail-work')!
    expect(work.getBoundingClientRect().right).toBeLessThanOrEqual(card.getBoundingClientRect().right + 1)
  })
})

for (const width of [390, 3840]) for (const theme of ['dark', 'colorblind-dark']) it(`shows idle time and both hover durations at ${width}px in ${theme}`, async () => {
 vi.mocked(apiFetch).mockResolvedValueOnce(Response.json({person_time:{timezone:'America/New_York'}}))
 await initializePersonTime()
 document.documentElement.setAttribute('data-theme', theme)
 await page.viewport(width, 900)
 const idle = { ...stopped, started_at: '2026-10-19T00:22:00Z', last_turn_ended_at: '2026-10-19T05:39:00Z', activity: 'Turn complete' }
 useSessionStore.setState({sessions:[makeSession({id:idle.id,title:idle.title,status:'idle'}),makeSession({id:base.id,title:base.title,status:'running'})]})
 render(<MemoryRouter><aside className="session-rail panel" style={{width:Math.min(320,width-24)}}><StartedSessions sessionId="parent" children={[idle,base]} clockOffset={0} active={false}/></aside></MemoryRouter>)
 expect(screen.getByText('idle 4h 23m')).toBeVisible()
 expect(screen.getByTitle('Workspace age')).toHaveTextContent('2 min')
 await userEvent.hover(screen.getAllByTestId('rail-child-open')[0])
 await expect.element(page.getByTestId('hovercard')).toBeVisible()
 const hover = screen.getByTestId('hovercard')
 expect(hover).toHaveTextContent('yesterday 8:22 PM · 9 h 40 min elapsed')
 expect(hover).toHaveTextContent('today 1:39 AM · idle 4h 23m')
 for(const row of screen.getAllByTestId('rail-child')) expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth)
 const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
 if(shots) await page.screenshot({path:`${shots}/idle-worker-${width}-${theme}.png`})
})
