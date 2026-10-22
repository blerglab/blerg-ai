import { describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { ChildSessionRow, StartedSessions } from './ChildSessions'
import type { LiveChildSession } from '../lib/liveState'
import '../index.css'
import './SessionRail.css'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../ws', () => import('../test/wsMock'))

const now = Date.parse('2026-10-19T10:02:00Z')
const base: LiveChildSession = { id: 'c1', title: 'Review the runner change set with a long title', engine: 'codex', model: 'fixture-model', effort: 'high', status: 'running', started_at: '2026-10-19T10:00:00Z', asking: false, tokens: 1000, activity: 'Bash · npm test', activity_since: '2026-10-19T10:01:30Z' }
const stopped: LiveChildSession = { ...base, id: 'c2', title: 'Finished worker', status: 'idle', activity: undefined }

// A running child is one card, in Running now, with everything; Started sessions has one line that
// brings it into view, and lists the child that stopped.
describe('a running child shows once, in Chromium', () => {
  it.each([390, 3840])('at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const show = vi.fn()
    const { container, getByTestId, getAllByTestId } = render(<MemoryRouter><div style={{ width: 'min(420px, 100%)' }}>
      <section data-testid="running"><ul className="rail-list"><ChildSessionRow variant="run" child={base} now={now} parentId="parent" /></ul></section>
      <StartedSessions sessionId="parent" children={[base, stopped]} now={now} onShowRunning={show} />
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
