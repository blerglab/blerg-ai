import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import type { BackgroundTask } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (original) => ({
  ...(await original<typeof import('../authClient')>()), getAccessToken: () => 'fixture-token', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async (original) => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn().mockResolvedValue({ ok: false }) }))

const heredoc = "cat >> runner/internal/server/fixture_test.go <<'EOF'\n" +
  Array.from({ length: 80 }, (_, i) => `fixture line ${i + 1}: ${'long command content '.repeat(8)}`).join('\n') + '\nEOF'
function show(command: string, long = true, phone = false) {
  const task: BackgroundTask = {
    taskId: 'shell-fixture', callId: 'call-fixture', kind: 'bash', command,
    description: long ? `Append regression fixture:\n${command}` : 'Run the checks',
    startedAt: Date.now() - 1000,
    done: long ? { status: 'failed', summary: `Fixture failure:\n${command}`, at: Date.now() } : null,
  }
  render(<MemoryRouter><div style={{ display: 'flex', height: 800 }}>
    <SessionRail id="rail" session={makeSession()} live={makeLive()} tasks={[task]} toolChanges={0}
      mode={phone ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}} />
  </div></MemoryRouter>)
  return screen.getByTestId('rail-task')
}
beforeEach(() => localStorage.clear())
describe('bounded Running now shell command cards', () => {
  for (const phone of [false, true]) {
    it(`keeps a heredoc card to two lines, the whole text in its tooltip, on ${phone ? 'phone' : 'desktop'}`, async () => {
      await page.viewport(phone ? 390 : 1280, 800)
      const card = show(heredoc, true, phone)
      // Two lines: the title (what it is for) and the result; the command and the rest in the tooltip,
      // and in the conversation the card brings into view.
      expect(card.getBoundingClientRect().height).toBeLessThanOrEqual(60)
      const lines = [...card.querySelectorAll('.rail-run-line')]
      expect(lines).toHaveLength(2)
      for (const line of lines) expect(line.getBoundingClientRect().height).toBeLessThan(24)
      const title = within(card).getByTestId('rail-show')
      expect(title.getAttribute('aria-description')).toContain('fixture line 80')
      expect(within(card).getByTestId('rail-task-summary').getAttribute('title')).toContain('fixture line 80')
      expect(within(card).getByRole('button', { name: 'Dismiss' })).toBeVisible()
      expect(within(card).getByRole('img', { name: 'failed' })).toBeVisible()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(phone ? 390 : 1280)
    })
  }
  it('on a phone a tap on the step line shows the whole command in place, scrollable, and a second tap folds it', async () => {
    await page.viewport(390, 800)
    const card = show(heredoc, true, true)
    const step = within(card).getByTestId('rail-task-summary')
    const folded = card.getBoundingClientRect().height
    await userEvent.click(step)
    expect(step).toHaveAttribute('aria-expanded', 'true')
    expect(step.textContent).toContain('fixture line 80')
    expect(step.textContent).toContain('Append regression fixture')
    expect(card.getBoundingClientRect().height).toBeGreaterThan(folded + 60)
    expect(step.scrollHeight).toBeGreaterThan(step.clientHeight)
    step.scrollTop = step.scrollHeight
    expect(step.scrollTop).toBeGreaterThan(0)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    await userEvent.click(step)
    expect(step).toHaveAttribute('aria-expanded', 'false')
    expect(card.getBoundingClientRect().height).toBeLessThanOrEqual(60)
  })
  it('keeps an ordinary command on one line', async () => {
    await page.viewport(1280, 800)
    const card = show('npm test', false)
    const command = within(card).getByTestId('rail-command')
    expect(command.textContent).toBe('$ npm test')
    expect(command.getBoundingClientRect().height).toBeLessThan(24)
    expect(within(card).getByTestId('rail-task-note')).toHaveTextContent('Run the checks')
  })
})
