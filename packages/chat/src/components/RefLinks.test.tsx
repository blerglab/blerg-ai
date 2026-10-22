// Card numbers and commit hashes in chat text (card #153): candidates are drawn as the text they are
// until confirmed, then link in place; prose that names no card, a hex word that is no commit, and
// anything in a fenced code block stay text; lookups are batched and asked once.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import Markdown from './Markdown'
import { RefContext, type RefContextValue } from './RefLinks'
import { CommitViewerHost } from './CommitViewer'
import { createRefStore, DEBOUNCE_MS, splitRefs, type ChatRefs, type RefStore } from '../model/refs'
import type { CardTarget } from './Hovercard'
import type { TransportFiles } from '../transport/types'

const SHA = '08025f2c9d1e4b7a0c3f5e6d7a8b9c0d1e2f3a4b'

function cardsReader(exists: number[]) {
  return vi.fn(async (numbers: number[]) => new Map(numbers.filter(n => exists.includes(n)).map(n => [n, { kind: 'card', source: 'blerg-board', key: `#${n}`, boardId: 'b1', title: `Card ${n}` } as CardTarget])))
}

function setup(text: string, opts: { cards?: ChatRefs['cards']; commit?: (sha: string) => Promise<string | null>; openCard?: ChatRefs['openCard'] } = {}) {
  const store: RefStore = createRefStore()
  store.setResolvers({ cards: opts.cards, commit: opts.commit ?? null })
  const refs: ChatRefs = { cards: opts.cards, openCard: opts.openCard ?? vi.fn(), commits: !!opts.commit }
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

  it('outside a chat that lends references (a document, another surface) the text is untouched', () => {
    render(<Markdown text="See #147 and 08025f2c." />)
    expect(document.querySelector('[data-ref-candidate]')).toBeNull()
    expect(document.querySelector('.md')!.textContent).toBe('See #147 and 08025f2c.')
  })
})
