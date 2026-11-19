import { expect, it, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { page } from 'vitest/browser'
import RepositoryGraph from './RepositoryGraph'
import '../index.css'
import './DockRepositories.css'
vi.mock('../ws', () => import('../test/wsMock'))
const sha = 'a'.repeat(40)
const graph = { refs: [{ name: 'main', sha }], more: true, commits: [
  { sha: 'b'.repeat(40), parents: [sha], subject: 'Worker change with a long readable subject' },
  { sha, parents: [], subject: 'Main' },
] }
for (const width of [390, 3840]) it(`keeps one continuous canvas and fixed rows at ${width}px`, async () => {
  await page.viewport(width, 900)
  const more = vi.fn()
  const { container } = render(<div style={{ width: '100%' }}><RepositoryGraph name="app" sessionId="s" depth={50} graph={graph} onMore={more} /></div>)
  const history = screen.getByRole('list', { name: 'Commit history for app' })
  expect(history).toBeVisible()
  expect(container.querySelectorAll('svg')).toHaveLength(1)
  const scroll = container.querySelector('.repository-graph-scroll')!
  expect(scroll.scrollWidth).toBeLessThanOrEqual(scroll.clientWidth)
  expect(container.querySelector('svg')!.getBoundingClientRect().width).toBeLessThanOrEqual(56)
  for (const row of history.children) {
    expect(row.getBoundingClientRect().height).toBe(48)
    for (const selector of ['.repository-graph-subject', 'code', 'time']) {
      const bounds = row.querySelector(selector)!.getBoundingClientRect()
      expect(bounds.left).toBeGreaterThanOrEqual(0)
      expect(bounds.right).toBeLessThanOrEqual(width)
      expect(bounds.width).toBeGreaterThan(0)
    }
  }
  fireEvent.click(screen.getByRole('button', { name: 'More history' }))
  expect(more).toHaveBeenCalledOnce()
})
