import { afterEach, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import { page } from 'vitest/browser'
import WorkspaceTree from './WorkspaceTree'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../apiFetch', () => ({ apiFetch: async () => new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [{ name: 'a-very-long-file-name-'.repeat(12) + '.txt', path: 'long.txt', directory: false }], truncated: false }, members: [], runtime: 'host' })) }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))
afterEach(async () => { await page.viewport(1280, 800) })
it.each([[390, 844], [3840, 2160]])('uses its panel space without horizontal overflow at %i px', async (width, height) => {
  await page.viewport(width, height)
  const { findByRole } = render(<div style={{ width: width < 700 ? '100%' : 340 }}><WorkspaceTree sessionId="session" active /></div>)
  await findByRole('treeitem', { name: /a-very-long-file-name/ })
  const tree = document.querySelector('.workspace-tree')!.getBoundingClientRect()
  expect(tree.width).toBeGreaterThan(width < 700 ? 300 : 330)
  expect(tree.right).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  expect(document.querySelector('.workspace-row')!.getBoundingClientRect().width).toBeLessThanOrEqual(tree.width)
})
