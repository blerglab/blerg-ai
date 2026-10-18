import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page } from 'vitest/browser'
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
    it(`keeps a heredoc card compact and its complete text scrollable on ${phone ? 'phone' : 'desktop'}`, async () => {
      await page.viewport(phone ? 390 : 1280, 800)
      const card = show(heredoc, true, phone)
      expect(card.getBoundingClientRect().height).toBeLessThanOrEqual(340)
      for (const name of ['Shell command', 'Command description', 'Command result']) {
        const region = within(card).getByRole('region', { name })
        expect(region.scrollHeight).toBeGreaterThan(region.clientHeight)
        expect(region.textContent).toContain('fixture line 80')
        region.focus()
        expect(document.activeElement).toBe(region)
        region.scrollTop = region.scrollHeight
        expect(region.scrollTop).toBeGreaterThan(0)
      }
      expect(within(card).getByRole('button', { name: 'Dismiss' })).toBeVisible()
      expect(within(card).getByTestId('rail-shell-status')).toHaveTextContent('failed')
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(phone ? 390 : 1280)
    })
  }
  it('keeps an ordinary command on one line without a scroll box', async () => {
    await page.viewport(1280, 800)
    const card = show('npm test', false)
    const command = within(card).getByTestId('rail-command')
    expect(command.textContent).toBe('$ npm test')
    expect(command.getBoundingClientRect().height).toBeLessThan(24)
    expect(command.scrollHeight).toBe(command.clientHeight)
    expect(within(card).getByTestId('rail-task-note')).toHaveTextContent('Run the checks')
  })
})
