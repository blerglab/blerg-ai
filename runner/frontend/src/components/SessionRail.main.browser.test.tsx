import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { useTranscriptStore, useWorkingLineStore, type BackgroundTask } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import type { LiveChildSession } from '../lib/liveState'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (original) => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (original) => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn().mockResolvedValue({ ok: false }) }))

// Card #123: Running now heads with the main agent ("Main" and its engine's mark, its current step from
// the working line); what runs inside it is indented under it behind ↳; a started session sits at its level.
interface Opts { progress?: boolean; since?: number; chatOffset?: number; liveOffset?: number }
function show(width: number, working: boolean, o: Opts = {}) {
  const session = makeSession({ status: working ? 'running' : 'idle', engine: 'claude' })
  const now = Date.now()
  useTranscriptStore.setState({ sessions: o.chatOffset ? { [session.id]: { events: [], streaming: '', thinking: false, lastSeq: 0, replayDone: true, hasMore: false, hasOlder: false, firstSeq: 0, clockOffset: o.chatOffset } } : {} })
  useWorkingLineStore.setState({ lines: working ? { [session.id]: {
    since: o.since ?? now - 280_000,
    waitingOn: { tool: 'Bash', startedAt: now - 249_000, detail: 'Fast-forward main to the release branch after the checks pass' },
    activity: null,
  } } : {} })
  const task: BackgroundTask = { taskId: 'b1', callId: 'c1', kind: 'bash', command: 'npm run build -- --watch', description: 'Watch the build', startedAt: now - 60_000, done: null }
  const child: LiveChildSession = { id: 'child-1', title: 'Parallel reviewer with a long title that has to be cut', engine: 'codex', model: 'fixture-model', status: 'running',
    started_at: new Date(now - 120_000).toISOString(), asking: false, tokens: 1200, activity: 'Bash · go test ./...', activity_since: new Date(now - 30_000).toISOString() }
  render(<MemoryRouter><div style={{ display: 'flex', height: 900, width }}>
    <SessionRail id="rail" session={session} live={makeLive({ clockOffset: o.liveOffset ?? 0, ...(o.progress ? { progress: { completed: 3, total: 8, label: 'Migrate the parser and port every remaining caller' } } : {}), subagents: [{ key: 'S1', title: 'Survey the repository layout', type: 'Explore', model: '', tokens: null, doing: 'Read · src/index.ts', startedAt: now - 90_000, background: false }] })}
      tasks={[task]} childSessions={[child]} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}} />
  </div></MemoryRouter>)
  return screen.getByTestId('rail-running')
}

beforeEach(() => localStorage.clear())

describe('Running now heads with Main, in Chromium', () => {
  it.each([390, 3840])('at %i px: Main first and compact, inside rows indented, the child at its level', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const running = show(width, true)
    expect(within(running).getByTestId('rail-running-count')).toHaveTextContent('4')
    const rows = [...running.querySelector('.rail-list')!.children] as HTMLElement[]
    expect(rows.map(r => r.dataset.testid)).toEqual(['rail-main', 'rail-subagent', 'rail-task', 'rail-child'])
    const [main, sub, task, child] = rows
    // Compact: two lines, each one line high.
    expect(main.querySelectorAll('.rail-run-line')).toHaveLength(2)
    for (const line of main.querySelectorAll('.rail-run-line')) expect(line.getBoundingClientRect().height).toBeLessThan(24)
    expect(main.getBoundingClientRect().height).toBeLessThan(64)
    expect(main).toHaveTextContent('Main')
    expect(within(main).getByTestId('rail-main-doing')).toHaveTextContent('Bash: Fast-forward main')
    expect(within(main).getByTestId('rail-main-step-elapsed')).toHaveTextContent('4:09')
    expect(within(main).getByTestId('rail-elapsed')).toHaveTextContent('4:40')
    expect(within(main).getByTestId('rail-main-engine')).toHaveAccessibleName('Engine: Claude')
    // The rows that run inside Main are indented under it, behind a visible ↳; the child is not.
    const left = main.getBoundingClientRect().left
    for (const r of [sub, task]) {
      expect(r.getBoundingClientRect().left).toBeGreaterThanOrEqual(left + 12)
      const arrow = r.querySelector('.rail-run-nest')!
      expect(arrow).toHaveTextContent('↳')
      expect(arrow.getBoundingClientRect().left).toBeGreaterThanOrEqual(left)
      expect(arrow.getBoundingClientRect().right).toBeLessThanOrEqual(r.getBoundingClientRect().left + 1)
    }
    expect(Math.abs(child.getBoundingClientRect().left - left)).toBeLessThan(1)
    expect(within(child).getByRole('img', { name: 'Engine: Codex' })).toBeVisible()
    // Every row inside its panel (the drawer on a phone may still be sliding in: compare with it).
    const panel = running.getBoundingClientRect()
    for (const r of rows) expect(r.getBoundingClientRect().right).toBeLessThanOrEqual(panel.right)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it.each([390, 3840])('at %i px: idle with a background command, Main is Zz and not counted', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const running = show(width, false)
    expect(within(running).getByTestId('rail-running-count')).toHaveTextContent('3')
    const main = within(running).getByTestId('rail-main')
    expect(within(main).getByRole('img', { name: 'Idle' })).toBeVisible()
    expect(main.querySelectorAll('.rail-run-line')).toHaveLength(1)
    expect(main.getBoundingClientRect().height).toBeLessThan(40)
  })
})

describe('Main with a progress bar and a step, in Chromium', () => {
  it.each([390, 3840])('at %i px the step and its clock stay visible inside the card', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const running = show(width, true, { progress: true })
    const main = within(running).getByTestId('rail-main')
    const card = main.getBoundingClientRect()
    const bar = within(main).getByTestId('rail-session-progress')
    expect(bar).toHaveTextContent('3/8')
    const step = within(main).getByTestId('rail-main-doing').getBoundingClientRect()
    expect(step.width).toBeGreaterThan(40)
    const clock = within(main).getByTestId('rail-main-step-elapsed')
    expect(clock).toHaveTextContent('4:09')
    const c = clock.getBoundingClientRect()
    expect(c.width).toBeGreaterThan(0)
    expect(c.left).toBeGreaterThanOrEqual(card.left)
    expect(c.right).toBeLessThanOrEqual(card.right)
    expect(bar.getBoundingClientRect().right).toBeLessThanOrEqual(step.left)
    expect(within(main).getByTestId('rail-main-engine').getBoundingClientRect().right).toBeLessThanOrEqual(card.right)
    expect(main.getBoundingClientRect().height).toBeLessThan(64)
  })
})

describe('Main clocks, in Chromium', () => {
  it('run on the chat\'s clock offset (the working line\'s), not the panel\'s', async () => {
    await page.viewport(1280, 900)
    const running = show(1280, true, { chatOffset: 0, liveOffset: 60_000 })
    const main = within(running).getByTestId('rail-main')
    expect(within(main).getByTestId('rail-elapsed')).toHaveTextContent('4:40')
    expect(within(main).getByTestId('rail-main-step-elapsed')).toHaveTextContent('4:09')
  })

  it('show no clock until it has a value (a turn that starts after the last tick)', async () => {
    await page.viewport(1280, 900)
    const running = show(1280, true, { since: Date.now() + 5_000 })
    const main = within(running).getByTestId('rail-main')
    expect(within(main).queryByTestId('rail-elapsed')).toBeNull()
    expect(main).not.toHaveTextContent('0:00')
  })
})

describe('a click on Main, in Chromium', () => {
  it('scrolls a visible chat to its end; a hidden one says the conversation is not on screen', async () => {
    await page.viewport(1280, 900)
    const host = document.createElement('div')
    host.innerHTML = '<div class="agent-timeline" style="height:100px;overflow:auto"><div style="height:2000px;min-height:2000px;flex:none"></div></div>'
    document.body.appendChild(host)
    const timeline = host.firstElementChild as HTMLElement
    try {
      const running = show(1280, true)
      const main = within(running).getByTestId('rail-main')
      await userEvent.click(within(main).getByTestId('rail-main-show'))
      await expect.poll(() => timeline.scrollTop).toBeGreaterThan(1500)
      expect(within(main).queryByRole('status')).toBeNull()
      host.style.display = 'none'
      await userEvent.click(within(main).getByTestId('rail-main-show'))
      expect(within(main).getByRole('status')).toHaveTextContent('The conversation is not on screen.')
    } finally {
      host.remove()
    }
  })
})

describe('the hierarchy for a screen reader', () => {
  it('rows under Main say they run inside it; a started session does not', async () => {
    await page.viewport(1280, 900)
    const running = show(1280, true)
    for (const id of ['rail-subagent', 'rail-task']) {
      expect(within(within(running).getByTestId(id)).getByTestId('rail-show').getAttribute('aria-description')).toMatch(/^Runs inside Main\. /)
    }
    expect(within(within(running).getByTestId('rail-child')).getByTestId('rail-child-open').getAttribute('aria-description')).not.toContain('inside Main')
  })
})
