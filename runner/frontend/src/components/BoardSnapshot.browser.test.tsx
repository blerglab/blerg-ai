import { expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { Markdown, ReferenceScope } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

for (const width of [390, 3840]) it(`renders board columns and fifty cards inside the chat at ${width}px`, async () => {
  await page.viewport(width, 900)
  const openCard = vi.fn()
  const cards = Array.from({ length: 50 }, (_, i) => ({ number: i + 1, title: `Work ${i + 1}`, tags: ['0.6.0'], priority: 'high', assignee: 'Builder' }))
  const source = '```board-snapshot\n' + JSON.stringify({ version: 1, mode: 'board', board_id: 'board-a', name: 'Release', as_of: '2026-10-21T12:00:00Z', filters: {}, columns: [{ id: 'todo', name: 'To do', cards }, { id: 'doing', name: 'Doing', cards: [] }, { id: 'done', name: 'Done', cards: [] }] }) + '\n```'
  render(<ReferenceScope sessionId={`snapshot-${width}`} refs={{ boardUrl: id => `/boards/${id}`, boardCards: async (boardId, ns) => new Map(ns.map(n => [n, { kind: 'card', source: 'blerg-board', boardId, key: `BLG-${n}` }])), openCard }}><Markdown text={source} /></ReferenceScope>)
  expect(screen.getAllByText(/^Work \d+$/)).toHaveLength(20)
  const cols = [...document.querySelectorAll('.board-snapshot-column')].map(col => col.getBoundingClientRect())
  if (width === 390) expect(cols[1].top).toBeGreaterThan(cols[0].top)
  else { expect(cols[1].left).toBeGreaterThan(cols[0].left); expect(cols[1].top).toBe(cols[0].top) }
  await userEvent.click(screen.getByRole('button', { name: /#1 · high/ }))
  expect(openCard).toHaveBeenCalledWith(expect.objectContaining({ boardId: 'board-a', key: 'BLG-1' }))
  expect(screen.getByRole('link', { name: 'Open board' })).toHaveAttribute('href', '/boards/board-a')
  await userEvent.click(screen.getByRole('button', { name: 'Show 30 more cards' }))
  expect(screen.getAllByText(/^Work \d+$/)).toHaveLength(50)
  await userEvent.click(screen.getByRole('button', { name: 'To do (50)' }))
  expect(screen.queryByText('Work 1')).toBeNull()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
