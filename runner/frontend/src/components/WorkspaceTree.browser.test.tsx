import { afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import WorkspaceTree from './WorkspaceTree'
import '../index.css'
import '@blerglab/chat/blerg.css'

const { read } = vi.hoisted(() => ({ read: vi.fn() }))
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => read(...args) }))
vi.mock('../ws', () => import('../test/wsMock'))
read.mockImplementation(async () => new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [{ name: 'a-very-long-file-name-'.repeat(12) + '.txt', path: 'long.txt', directory: false }, { name: 'node_modules', path: 'node_modules', directory: true, ignored: true }], truncated: false }, members: [], runtime: 'host' })))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))
afterEach(async () => { await page.viewport(1280, 800) })
it.each([[390, 844], [3840, 2160]])('uses its panel space without horizontal overflow at %i px', async (width, height) => {
  await page.viewport(width, height)
  const { findByRole } = render(<div style={{ width: width < 700 ? '100%' : 340 }}><WorkspaceTree sessionId="session" active /></div>)
  await findByRole('treeitem', { name: /a-very-long-file-name/ })
  const tree = document.querySelector('.workspace-tree')!.getBoundingClientRect()
  expect(tree.width).toBeGreaterThan(width < 700 ? 300 : 330)
  expect(tree.right).toBeLessThanOrEqual(width)
  const ignored = await findByRole('treeitem', { name: 'node_modules' })
  expect(ignored).not.toHaveClass('workspace-row-ignored')
  expect(Number(getComputedStyle(ignored.querySelector('.workspace-name')!).opacity)).toBe(1)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  expect(document.querySelector('.workspace-row')!.getBoundingClientRect().width).toBeLessThanOrEqual(tree.width)
})

it.each([[390, 844], [3840, 2160]])('retries Git status with private details in a tooltip at %i px', async (width, height) => {
  await page.viewport(width, height)
  let attempts = 0, manualRetries = 0
  read.mockImplementation(async (url: string) => {
    const status = url.includes('op=status')
    if (status) attempts++
    const failed = status && manualRetries < 2
    return new Response(JSON.stringify({
      result: { ok: !failed, launch_generation: 'retry', generation: 1, entries: [],
        ...(status ? failed ? { error_code: 'unavailable', status_reason: 'git_failed' } : { status: { branch: 'main', changed: 0, untracked: 0 } } : {}) },
      members: [{ id: 'repo', name: 'repository', path: '.' }], runtime: 'host',
    }))
  })
  const { container } = render(<div style={{ width: width < 700 ? '100%' : 340 }}><WorkspaceTree sessionId={`retry-${width}`} active /></div>)
  // A background focus read must not consume a scripted manual-retry outcome.
  container.addEventListener('click', event => {
    if (event.target instanceof Element && event.target.closest('[aria-label="Retry Git status"]')) manualRetries++
  }, true)
  const message = await screen.findByText('Git status unavailable')
  expect(message.parentElement).toHaveAttribute('title', expect.stringContaining('Git status command'))
  expect(message.parentElement?.textContent).not.toContain('resource limits')
  const retry = screen.getByRole('button', { name: 'Retry Git status' })
  expect(retry).toHaveAttribute('title', 'Retry Git status')
  expect(retry.textContent).toBe('')
  if (width === 390) {
    expect(retry.getBoundingClientRect().width).toBeGreaterThanOrEqual(44)
    expect(retry.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
  }
  expect(retry.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  await userEvent.click(retry)
  await expect.element(page.getByRole('button', { name: 'Retry Git status' })).toBeEnabled()
  expect(screen.getAllByText('Git status unavailable')).toHaveLength(1)
  expect(screen.queryByText(/resource limits/)).toBeNull()
  // BLG-270 also revalidates on focus, including while native input refocuses the frame.
  const beforeFocus = attempts
  act(() => { window.dispatchEvent(new Event('focus')) })
  await waitFor(() => expect(attempts).toBeGreaterThan(beforeFocus))
  await expect.element(page.getByRole('button', { name: 'Retry Git status' })).toBeEnabled()
  await userEvent.click(retry)
  expect(await screen.findByText('main · 0 changed')).toBeVisible()
  expect(manualRetries).toBe(2)
  expect(attempts).toBeGreaterThanOrEqual(4)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it.each([[390, 844], [3840, 2160]])('collapses folders from the header at %i px', async (width, height) => {
  await page.viewport(width, height)
  read.mockImplementation(async (url: string) => new Response(JSON.stringify({ result: { ok: true, launch_generation: 'collapse', generation: 1, entries: url.includes('path=folder') ? [{ name: 'file.txt', path: 'folder/file.txt', directory: false }] : [{ name: 'folder', path: 'folder', directory: true }], truncated: false }, members: [], runtime: 'host' })))
  render(<div style={{ width: width < 700 ? '100%' : 340 }}><WorkspaceTree sessionId={`collapse-${width}`} active /></div>)
  await userEvent.click(await screen.findByRole('treeitem', { name: 'folder' }))
  await screen.findByRole('treeitem', { name: 'file.txt' })
  await userEvent.click(screen.getByRole('button', { name: 'Collapse folders' }))
  expect(screen.getByRole('treeitem', { name: 'folder' })).toHaveAttribute('aria-expanded', 'false')
  expect(screen.getByRole('treeitem', { name: 'folder' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.queryByRole('treeitem', { name: 'file.txt' })).toBeNull()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it.each([390, 3840])('spins the shared file refresh control while its read is pending at %i px', async width => {
  await page.viewport(width, 844)
  let finish!: (response: Response) => void
  const reply = () => new Response(JSON.stringify({ result: { ok: true, launch_generation: 'spin', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' }))
  read.mockResolvedValue(reply())
  render(<div style={{ width: width < 700 ? '100%' : 340 }}><WorkspaceTree sessionId={`spin-${width}`} active /></div>)
  const refresh = screen.getByRole('button', { name: 'Refresh workspace' })
  await expect.element(page.getByRole('button', { name: 'Refresh workspace' })).toBeEnabled()
  read.mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
  await userEvent.click(refresh)
  expect(refresh).toHaveAttribute('aria-busy', 'true')
  expect(refresh).toBeDisabled()
  expect(refresh.querySelector('svg')).toHaveClass('refresh-control-spin')
  if (width === 390) {
    expect(refresh.getBoundingClientRect().width).toBeGreaterThanOrEqual(44)
    expect(refresh.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
  }
  finish(reply())
  await expect.element(page.getByRole('button', { name: 'Refresh workspace' })).toBeEnabled()
})
