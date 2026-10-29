import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { boardCardsReader, resetBoardKeyMapsForTest } from './cardRefs'
import { boardCardRef, resetLiveBoardCardsForTest } from './liveBoardCard'
import { ChecklistRefsContext, useChecklistRefs } from './checklistRefs'
import { CardRefText } from '../components/ChecklistRefs'
import { apiFetch } from '../apiFetch'
import { resetRefStoresForTest } from '@blerglab/chat'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
beforeEach(() => { resetLiveBoardCardsForTest(); resetBoardKeyMapsForTest(); resetRefStoresForTest(); vi.mocked(apiFetch).mockReset() })
function stub() {
  vi.mocked(apiFetch).mockImplementation(async path => {
    if (path === '/api/session-boards') return new Response(JSON.stringify({ boards: [{ id:'board-a', key:'BLG', key_aliases:['OLD'] }, {id:'board-b', key:'AD'}] }))
    const b = String(path).includes('/board-b/') ? 'board-b' : 'board-a'
    const number = b === 'board-b' ? 6 : 205
    return new Response(JSON.stringify({ card: { id: 'card', board_id: b, number, title: 'Work', reference: `${b === 'board-b' ? 'AD' : 'NEW'}-${number}` }, url: '' }))
  })
}
it('canonicalizes legacy references without losing the resolved board id', async () => {
  stub()
  const found = await boardCardsReader({ id: 'OLD' }, 's')([205])
  expect(found.get(205)).toMatchObject({ boardId: 'board-a', key: 'NEW-205' })
  expect(boardCardRef({ source: 'blerg-board', key: 'NEW-205', boardId: 'board-a' })).toEqual({ board: 'board-a', number: 205 })
})
it('renders cross-board key references in a plan even without an unambiguous board', async () => {
  stub()
  const open = vi.fn()
  const texts = ['BLG-205/AD-6']
  function Plan() {
    const value = useChecklistRefs('s', texts, null, true, open)
    return <ChecklistRefsContext.Provider value={value}><CardRefText text={texts[0]} /></ChecklistRefsContext.Provider>
  }
  render(<Plan />)
  await waitFor(() => expect(screen.getAllByTestId('plan-ref')).toHaveLength(2))
  expect(screen.getAllByTestId('plan-ref').map(b => b.textContent)).toEqual(['NEW-205', 'AD-6'])
  fireEvent.click(screen.getByRole('button', { name: 'Open card AD-6: Work' }))
  expect(open).toHaveBeenCalledWith(expect.objectContaining({ boardId: 'board-b', key: 'AD-6' }))
})

it('keeps pre-confirmation explicit board references openable', () => {
  expect(boardCardRef({ source: 'blerg-board', boardId: 'board-a', key: 'board:board-a#205' })).toEqual({ board:'board-a', number:205 })
  expect(boardCardRef({ source: 'blerg-board', boardId: 'board-b', key: 'board:board-a#205' })).toBeNull()
})

it('keeps common acronyms plain with one cached discovery and no card reads', async () => {
  stub()
  const { Markdown, createRefStore, splitRefs } = await import('@blerglab/chat')
  const { RefContext } = await import('../../../../packages/chat/src/components/RefLinks')
  const text = 'UTF-8 ISO-8601 SHA-256 AES-128 COVID-19 GPT-4 555-1234 2026-10'
  const refs = { boardCards: (key: string, ns: number[]) => boardCardsReader({id:key}, 's')(ns) }
  const store = createRefStore()
  store.setResolvers(refs)
  render(<RefContext.Provider value={{store, sessionId:'s', refs}}><Markdown text={text} /></RefContext.Provider>)
  for (const p of splitRefs(text)) if ('key' in p) await waitFor(() => expect(store.get(p.key).state).toBe('none'))
  expect(document.querySelector('.md')?.textContent).toBe(text)
  expect(screen.queryByTestId('md-ref')).toBeNull()
  expect(apiFetch).toHaveBeenCalledTimes(1)
  expect(apiFetch).toHaveBeenCalledWith('/api/session-boards')
})
