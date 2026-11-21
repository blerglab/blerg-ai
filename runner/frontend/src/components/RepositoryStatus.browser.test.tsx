import { afterEach, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import RepositoryStatus from './RepositoryStatus'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: (...args: Parameters<typeof fetch>) => fetch(...args) }))
afterEach(() => vi.restoreAllMocks())

for (const width of [390, 3840]) it(`shows graph-header status semantics at ${width}px`, async () => {
  await page.viewport(width, 900)
  let failure = false
  vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify(failure
    ? { result: { ok: false, status_reason: 'git_failed' } }
    : { result: { ok: true, status: { branch: 'main', changed: 2, untracked: 1, partial: true } } })))
  const view = render(<header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: 12 }}><h2 style={{ margin: 0 }}>Repository graph</h2><RepositoryStatus sessionId="s" path="." name="app" active /></header>)
  const partial = await screen.findByRole('img', { name: '3+ uncommitted' })
  expect(partial).toHaveAttribute('title', expect.stringContaining('incomplete'))
  const shots = (import.meta.env.VITE_CHROME_SHOTS_DIR || '').replace(/\/$/, '')
  if (shots) await page.screenshot({ path: `${shots}/repository-status-partial-${width}.png` })
  view.unmount()
  failure = true
  render(<header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: 12 }}><h2 style={{ margin: 0 }}>Repository graph</h2><RepositoryStatus sessionId="s" path="." name="app" active /></header>)
  expect(await screen.findByRole('img', { name: 'Git status unavailable' })).toHaveAttribute('title', expect.stringContaining('Git status command'))
  const retry = screen.getByRole('button', { name: 'Retry state for app' })
  expect(retry.getBoundingClientRect().width).toBeLessThan(50)
  if (shots) await page.screenshot({ path: `${shots}/repository-status-failure-${width}.png` })
  failure = false
  await userEvent.click(retry)
  expect(await screen.findByRole('img', { name: '3+ uncommitted' })).toBeVisible()
})
