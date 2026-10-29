// Card numbers and commit hashes in chat text (card #153): candidates are drawn as the text they are
// until confirmed, then link in place; prose that names no card, a hex word that is no commit, and
// anything in a fenced code block stay text; lookups are batched and asked once.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import Markdown from './Markdown'
import DiffView from './DiffView'
import { RefContext, type RefContextValue } from './RefLinks'
import { CommitViewerHost } from './CommitViewer'
import { createRefStore, DEBOUNCE_MS, splitRefs, type ChatRefs, type RefStore } from '../model/refs'
import type { CardTarget } from './Hovercard'
import type { TransportFiles } from '../transport/types'

const SHA = '08025f2c9d1e4b7a0c3f5e6d7a8b9c0d1e2f3a4b'

function cardsReader(exists: number[]) {
  return vi.fn(async (numbers: number[]) => new Map(numbers.filter(n => exists.includes(n)).map(n => [n, { kind: 'card', source: 'blerg-board', key: `#${n}`, boardId: 'b1', title: `Card ${n}` } as CardTarget])))
}

function setup(text: string, opts: { cards?: ChatRefs['cards']; boardCards?: ChatRefs['boardCards']; commit?: (sha: string) => Promise<string | null>; openCard?: ChatRefs['openCard'] } = {}) {
  const store: RefStore = createRefStore()
  store.setResolvers({ cards: opts.cards, boardCards: opts.boardCards, commit: opts.commit ?? null })
  const refs: ChatRefs = { cards: opts.cards, boardCards: opts.boardCards, openCard: opts.openCard ?? vi.fn(), commits: !!opts.commit }
  const ctx: RefContextValue = { store, sessionId: 's1', refs }
  const files = { commitDiff: vi.fn(async () => null) } as unknown as TransportFiles
  const view = render(
    <CommitViewerHost files={files}>
      <RefContext.Provider value={ctx}><Markdown text={text} /></RefContext.Provider>
    </CommitViewerHost>,
  )
  return { store, view }
}

async function settle() {
  await act(async () => { await vi.advanceTimersByTimeAsync(DEBOUNCE_MS + 10) })
}

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

describe('splitRefs', () => {
  it('finds #N and hash words, never inside other words or entities', () => {
    expect(splitRefs('see #147 and 08025f2c.')).toEqual([{ text: 'see ' }, { text: '#147', key: 'card:147' }, { text: ' and ' }, { text: '08025f2c', key: 'commit:08025f2c' }, { text: '.' }])
    expect(splitRefs('a#1 &#147; #1a abcdefa x/1234567 deadbee5-more')).toEqual([{ text: 'a#1 &#147; #1a abcdefa x/1234567 deadbee5-more' }])
    expect(splitRefs('#1 priority')).toEqual([{ text: '#1', key: 'card:1' }, { text: ' priority' }])
  })
})

describe('references in chat text', () => {
  it('"#1 priority" with no such card stays text, with no layout change', async () => {
    const cards = cardsReader([147])
    setup('This is our #1 priority.', { cards })
    const before = document.querySelector('.md')!.innerHTML
    await settle()
    expect(cards).toHaveBeenCalledWith([1])
    expect(screen.queryByTestId('md-ref')).toBeNull()
    expect(document.querySelector('.md')!.textContent).toBe('This is our #1 priority.')
    // The candidate's text is exactly as it was before the answer.
    expect(document.querySelector('[data-ref-candidate="card:1"]')!.getAttribute('data-ref-state')).toBe('none')
    expect(before.replace(/data-ref-state="[a-z]+"/, '')).toBe(document.querySelector('.md')!.innerHTML.replace(/data-ref-state="[a-z]+"/, ''))
  })

  it('#147 links once the card is confirmed, and opens the ticket', async () => {
    const openCard = vi.fn()
    setup('Done with #147 today.', { cards: cardsReader([147]), openCard })
    expect(screen.queryByTestId('md-ref')).toBeNull()
    await settle()
    const ref = screen.getByTestId('md-ref')
    expect(ref).toHaveTextContent('#147')
    expect(ref).toHaveAttribute('aria-label', 'Open card #147: Card 147')
    fireEvent.click(ref)
    expect(openCard).toHaveBeenCalledWith(expect.objectContaining({ key: '#147', boardId: 'b1' }))
  })

  it('a card number waits for the board: nothing is asked until the host gives a reader', async () => {
    const { store } = setup('See #147.')
    await settle()
    expect(store.calls.cards).toBe(0)
    expect(document.querySelector('[data-ref-candidate="card:147"]')!.getAttribute('data-ref-state')).toBe('pending')
    const cards = cardsReader([147])
    act(() => store.setResolvers({ cards, commit: null }))
    await settle()
    expect(cards).toHaveBeenCalledWith([147])
    expect(screen.getByTestId('md-ref')).toHaveTextContent('#147')
  })

  it('a hex word that is no commit stays text; a real sha links and opens the commit viewer', async () => {
    const commit = vi.fn(async (sha: string) => (SHA.startsWith(sha) ? SHA : null))
    setup('The hash cafe1234 is not one; `08025f2c` and 08025f2c9d are.', { commit })
    await settle()
    await act(async () => { await vi.advanceTimersByTimeAsync(200) })
    expect(commit.mock.calls.map(c => c[0]).sort()).toEqual(['08025f2c', '08025f2c9d', 'cafe1234'])
    const refs = screen.getAllByTestId('md-ref')
    expect(refs.map(r => r.textContent)).toEqual(['08025f2c', '08025f2c9d'])
    // The inline code span stays code, inside the link.
    expect(refs[0].querySelector('code')).toHaveTextContent('08025f2c')
    expect(document.querySelector('[data-ref-candidate="commit:cafe1234"]')!.getAttribute('data-ref-state')).toBe('none')
    act(() => { fireEvent.click(refs[1]) })
    expect(screen.getByTestId('commit-viewer-overlay')).toBeInTheDocument()
  })

  it('leaves fenced code blocks, links and longer inline code alone', async () => {
    const cards = cardsReader([147])
    const commit = vi.fn(async () => SHA)
    const { store } = setup('```\nfix #147 in 08025f2c\n```\n\n[#147](https://example.com) `git show 08025f2c`', { cards, commit })
    await settle()
    expect(store.calls.cards).toBe(0)
    expect(store.calls.commits).toBe(0)
    expect(screen.queryByTestId('md-ref')).toBeNull()
    expect(document.querySelector('[data-ref-candidate]')).toBeNull()
  })

  it('batches and dedupes: many mentions in many messages are one card read, one read per sha', async () => {
    const cards = cardsReader([1, 2, 3])
    const commit = vi.fn(async () => SHA)
    const store = createRefStore()
    store.setResolvers({ cards, commit })
    const ctx: RefContextValue = { store, sessionId: 's1', refs: { cards, openCard: vi.fn(), commits: true } }
    const text = '#1 #2 #3 #1 08025f2c 08025f2c'
    const { rerender } = render(<RefContext.Provider value={ctx}><Markdown text={text} /><Markdown text={`again ${text}`} /></RefContext.Provider>)
    rerender(<RefContext.Provider value={ctx}><Markdown text={text} /><Markdown text={`again ${text} more`} /></RefContext.Provider>)
    await settle()
    await act(async () => { await vi.advanceTimersByTimeAsync(200) })
    expect(cards).toHaveBeenCalledTimes(1)
    expect(cards.mock.calls[0][0].sort()).toEqual([1, 2, 3])
    expect(commit).toHaveBeenCalledTimes(1)
    // Rendering again asks nothing.
    rerender(<RefContext.Provider value={ctx}><Markdown text={`${text} once more`} /></RefContext.Provider>)
    await settle()
    expect(cards).toHaveBeenCalledTimes(1)
    expect(commit).toHaveBeenCalledTimes(1)
  })

  it.each(['#155–157', '#155-157', '#155–#157', '#155-#157'])('expands %s atomically and opens every card independently', async range => {
    const cards = cardsReader([155, 156, 157, 159])
    const openCard = vi.fn()
    setup(`See (${range}), and #159.`, { cards, openCard })
    expect(document.querySelector('.md')!.textContent).toBe(`See (${range}), and #159.`)
    await settle()
    expect(screen.getAllByTestId('md-ref').map(el => el.textContent)).toEqual(['#155', '#156', '#157', '#159'])
    expect(document.querySelector('.md')!.textContent).toBe('See (#155, #156, #157), and #159.')
    for (const el of screen.getAllByTestId('md-ref')) fireEvent.click(el)
    expect(openCard.mock.calls.map(([t]) => t.key)).toEqual(['#155', '#156', '#157', '#159'])
    expect(cards).toHaveBeenCalledTimes(1)
    expect(cards).toHaveBeenCalledWith([155, 156, 157, 159])
  })

  it.each([undefined, [], [155, 157]])('preserves the entire range without a board or any missing card: %j', async exists => {
    const { store } = setup('See #155–157.', { cards: exists && cardsReader(exists) })
    await settle()
    expect(screen.queryByTestId('md-ref')).toBeNull()
    expect(document.querySelector('.md')!.textContent).toBe('See #155–157.')
    expect(store.calls.cards).toBe(exists ? 1 : 0)
  })

  it('bounds expansion to ten cards and never looks up invalid ranges', async () => {
    const cards = cardsReader(Array.from({ length: 10 }, (_, i) => i + 1))
    setup('#1–10; #20–30; #30-20; #2--#4; #5–#6–#7; #8-foo; #9-; #0–2.', { cards })
    await settle()
    expect(cards).toHaveBeenCalledWith([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
    expect(screen.getAllByTestId('md-ref')).toHaveLength(10)
    expect(document.querySelector('.md')!.textContent).toContain('; #20–30; #30-20; #2--#4; #5–#6–#7; #8-foo; #9-; #0–2.')
  })

  it('excludes ranges in code and existing links', async () => {
    const cards = cardsReader([155, 156, 157])
    setup('```\n#155–157\n```\n\n`#155-157` [#155–#157](https://example.com)', { cards })
    await settle()
    expect(cards).not.toHaveBeenCalled()
    expect(screen.queryByTestId('md-ref')).toBeNull()
  })

  it('outside a chat that lends references (a document, another surface) the text is untouched', () => {
    render(<Markdown text="See #147 and 08025f2c." />)
    expect(document.querySelector('[data-ref-candidate]')).toBeNull()
    expect(document.querySelector('.md')!.textContent).toBe('See #147 and 08025f2c.')
  })
})

describe('board-qualified references (#184)', () => {
  it('recognizes an explicit board without mistaking its id for a commit', () => {
    expect(splitRefs('See board:example-board#7.')).toEqual([
      { text: 'See ' }, { text: 'board:example-board#7', key: 'card:example-board:7' }, { text: '.' },
    ])
  })
  it('consumes UUIDs as board ids and keeps surrounding ticket ranges intact', () => {
    expect(splitRefs('board:aaaaaaaa-0000-1111-2222-bbbbbbbbbbbb#7 and #8–10')).toEqual([
      { text: 'board:aaaaaaaa-0000-1111-2222-bbbbbbbbbbbb#7', key: 'card:aaaaaaaa-0000-1111-2222-bbbbbbbbbbbb:7' },
      { text: ' and ' }, { text: '#8–10', cards: [8, 9, 10] },
    ])
  })
  it('renders same-number cards on different boards as separate chips in a table', async () => {
    const openCard = vi.fn()
    const boardCards = vi.fn(async (board: string, numbers: number[]) => new Map(numbers.map(n => [n,
      { kind: 'card', source: 'blerg-board', key: `#${n}`, boardId: board, title: `${board} work` } as CardTarget])))
    setup('| Card |\n| --- |\n| board:created-board#7 |\n| board:other-board#7 |', { boardCards, openCard })
    await settle()
    expect(boardCards).toHaveBeenCalledWith('created-board', [7])
    expect(boardCards).toHaveBeenCalledWith('other-board', [7])
    const chips = screen.getAllByTestId('md-ref')
    expect(chips).toHaveLength(2)
    fireEvent.click(chips[1])
    expect(openCard).toHaveBeenCalledWith(expect.objectContaining({ boardId: 'other-board', key: '#7' }))
    expect(document.querySelector('a')).toBeNull()
  })
  it('keeps inaccessible cards as text and never recognizes card references inside code', async () => {
    const boardCards = vi.fn(async () => new Map<number, CardTarget>())
    setup('board:denied#7 and `board:code#8`\n\n```\nboard:code#9\n```', { boardCards })
    await settle()
    expect(boardCards).toHaveBeenCalledTimes(1)
    expect(boardCards).toHaveBeenCalledWith('denied', [7])
    expect(screen.queryByTestId('md-ref')).toBeNull()
  })
})

describe('changing the single in-use board', () => {
  it('rechecks a bare reference that was absent on the old board', async () => {
    const { store } = setup('Next: #7.', { cards: cardsReader([]) })
    await settle()
    expect(screen.queryByTestId('md-ref')).toBeNull()
    act(() => store.setResolvers({ cards: cardsReader([7]), commit: null }))
    await settle()
    expect(screen.getByTestId('md-ref')).toHaveTextContent('#7')
  })
})

it('renders key chips across boards and canonicalizes both legacy and renamed references', async () => {
  const openCard = vi.fn()
  const boardCards = vi.fn(async (board: string, ns: number[]) => new Map(ns.map(n => [n, {
    kind: 'card' as const, source: 'blerg-board', boardId: board === 'AD' ? 'board-b' : 'board-a',
    key: `${board === 'AD' ? 'AD' : 'NEW'}-${n}`, title: 'Work',
  }])))
  setup('(BLG-205/AD-6) and board:board-a#205', { boardCards, openCard })
  await settle()
  expect(screen.getAllByTestId('md-ref').map(b => b.textContent)).toEqual(['NEW-205', 'AD-6', 'NEW-205'])
  fireEvent.click(screen.getByRole('button', { name: 'Open card AD-6: Work' }))
  expect(openCard).toHaveBeenCalledWith(expect.objectContaining({ boardId: 'board-b', key: 'AD-6' }))
})


it.each([false, true])('renders canonical card chips in commit subjects and bodies (patch: %s)', async withPatch => {
  const store = createRefStore()
  const cards = async (_board: string, ns: number[]) => new Map(ns.map(n => [n, { kind: 'card' as const, source:'blerg-board', boardId:'b1', key:`BLG-${n}`, title:'Work' }]))
  store.setResolvers({ boardCards: cards, commit:null })
  const refs: ChatRefs = { boardCards:cards, openCard:vi.fn() }
  const text = `commit ${SHA}\nAuthor: Dev Example <dev@example.com>\nDate: Today\n\n    Fix OLD-205\n\n    Follow up on BLG-206\n\ndiff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n`
  render(<RefContext.Provider value={{ store, sessionId:'s', refs }}><DiffView text={withPatch ? text : text.split('diff --git')[0]} /></RefContext.Provider>)
  await settle()
  expect(screen.getAllByTestId('md-ref').map(b => b.textContent)).toEqual(['BLG-205', 'BLG-206'])
})
