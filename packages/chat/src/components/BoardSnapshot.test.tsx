import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import Markdown from './Markdown'
import { ReferenceScope } from './RefLinks'
import { parseBoardSnapshot } from './BoardSnapshot'

const card = (number: number) => ({ number, title: `Card ${number}`, tags: ['0.6.0'], priority: 'high', assignee: 'Builder' })
const block = (mode = 'board', count = 3) => '```board-snapshot\n' + JSON.stringify({ version: 1, mode, board_id: 'board-a', name: 'Release', as_of: '2026-10-21T12:00:00Z', filters: { tag: ['0.6.0'] }, columns: [{ id: 'todo', name: 'To do', cards: Array.from({ length: count }, (_, i) => card(i + 1)) }, { id: 'done', name: 'Done', cards: [] }] }) + '\n```'

describe('board snapshots', () => {
  it('renders columns, counts, timestamp and filters from one closed fence', () => {
    render(<Markdown text={block()} />)
    expect(screen.getByText('To do (3)')).toBeInTheDocument()
    expect(screen.getByText('Done (0)')).toBeInTheDocument()
    expect(screen.getByText(/As of/)).toHaveTextContent('2026-10-21T12:00:00Z')
    expect(screen.getByText(/Filters:/)).toHaveTextContent('0.6.0')
  })
  it('renders fifty cards in bounded windows and expands the rest', () => {
    render(<Markdown text={block('cards', 50)} />)
    expect(screen.getAllByText(/^Card \d+$/)).toHaveLength(20)
    fireEvent.click(screen.getByRole('button', { name: 'Show 30 more cards' }))
    expect(screen.getAllByText(/^Card \d+$/)).toHaveLength(50)
  })
  it('leaves malformed or unclosed blocks as code', () => {
    render(<Markdown text={'```board-snapshot\n{"version":1}\n```'} />)
    expect(screen.queryByRole('region', { name: 'Board snapshot' })).toBeNull()
  })
})

it('refuses malformed data and leaves streaming snapshots as code', () => {
  const source = block()
  render(<Markdown text={source.slice(0, -4)} />)
  expect(screen.queryByRole('region', { name: 'Board snapshot' })).toBeNull()
  const valid = JSON.parse(source.split('\n')[1])
  expect(parseBoardSnapshot(JSON.stringify({ ...valid, board_id: null }))).toBeNull()
  expect(parseBoardSnapshot(JSON.stringify({ ...valid, board_id: '../other' }))).toBeNull()
  expect(parseBoardSnapshot(JSON.stringify({ ...valid, columns: [{ ...valid.columns[0], cards: [card(1), card(1)] }] }))).toBeNull()
})

it('opens no live dialog when the current viewer cannot read a card', async () => {
  const openCard = vi.fn()
  const boardCards = vi.fn().mockResolvedValue(new Map())
  render(<ReferenceScope sessionId="denied-snapshot" refs={{ boardCards, openCard }}><Markdown text={block()} /></ReferenceScope>)
  fireEvent.click(screen.getByRole('button', { name: /#1 · high/ }))
  await screen.findByText('This card is unavailable or you do not have access.')
  expect(boardCards).toHaveBeenCalledWith('board-a', [1])
  expect(openCard).not.toHaveBeenCalled()
})
