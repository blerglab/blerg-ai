// Hovercards (card #127): one shared component on every chip. It opens after the configured delay
// of hover (or keyboard focus), stays while the pointer moves into it, closes on leave and Esc, is
// only for devices that can hover, reads its data on first open (never for a chip not hovered) and
// keeps it; one switch turns it off, and one object holds the delay and the fields.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import {
  COMMIT_MISS_MS, HOVERCARD, Hover, HovercardProvider, clearHovercardCache, headsFor, placeCard, setHovercardClock, setHovercardsEnabled, type CardPreview, type CardTarget, type HovercardSources,
} from './Hovercard'
import { CommitChips } from './CommitChips'
import { INLINE_TEXT_MAX, clearPreviewCache } from '../model/artifactPreview'
import { ChatContext, type ChatContextValue } from './context'
import { ChecklistItems, TicketCard } from './cards/WorkCards'
import type { AgentEvent } from '../types'
import type { ArtifactInfo, CommitDetails, TransportFiles } from '../transport/types'

function hoverDevice(on: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: on && query === '(hover: hover)', media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

/** A transport over `bytes`; raw() counts every read, rawCapped() (unless left out) reads through it
 *  and answers null past the cap, as the real one does. */
function filesWith(bytes: Record<string, string | Blob>, capped = true): TransportFiles & { raw: ReturnType<typeof vi.fn> } {
  const raw = vi.fn(async (_s: string, id: string) => {
    const b = bytes[id]
    if (b === undefined) throw new Error('no such file')
    return typeof b === 'string' ? new Blob([b]) : b
  })
  return {
    list: vi.fn(), download: vi.fn(), remove: vi.fn(), upload: vi.fn(), raw,
    ...(capped ? { rawCapped: async (s: string, id: string, max: number) => { const b = await raw(s, id); return b.size > max ? null : b } } : {}),
  } as unknown as TransportFiles & { raw: ReturnType<typeof vi.fn> }
}

const card: CardTarget = { kind: 'card', source: 'blerg-board', key: '#122', boardId: 'b1', title: 'Build History fold', status: 'Ready' }

function show(sources: HovercardSources, target = card as Parameters<typeof Hover>[0]['target']) {
  render(
    <HovercardProvider value={sources}>
      <Hover target={target}><button type="button" title="native tip">chip</button></Hover>
    </HovercardProvider>,
  )
  return screen.getByRole('button', { name: 'chip' })
}

async function flush() {
  await act(async () => { await Promise.resolve(); await Promise.resolve(); await new Promise(r => setTimeout(r, 0)) })
}

const DEFAULT_FIELDS = structuredClone(HOVERCARD.fields)
const DELAY = HOVERCARD.openDelayMs

beforeEach(() => {
  hoverDevice(true)
  localStorage.clear()
  clearHovercardCache()
  clearPreviewCache()
})
afterEach(() => {
  vi.useRealTimers()
  HOVERCARD.fields = structuredClone(DEFAULT_FIELDS)
  HOVERCARD.openDelayMs = DELAY
})

describe('Hover: when it opens and closes', () => {
  it('opens after the delay of hover, reads only then, and stays while the pointer is on it', async () => {
    vi.useFakeTimers()
    const read = vi.fn(async (): Promise<CardPreview> => ({ status: 'In progress', priority: 'High', type: 'Feature', tags: ['ui', 'panel'], description: 'Line one\nLine two' }))
    const chip = show({ card: read })
    expect(read).not.toHaveBeenCalled()
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs - 10) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    expect(read).not.toHaveBeenCalled()
    act(() => { vi.advanceTimersByTime(20) })
    const hc = screen.getByTestId('hovercard')
    // In a portal on the body (never clipped by the panel), described by the chip, its own tooltip gone.
    expect(hc.parentElement).toBe(document.body)
    expect(hc).toHaveStyle({ position: 'fixed' })
    expect(chip).toHaveAttribute('aria-describedby', hc.id)
    expect(chip).not.toHaveAttribute('title')
    expect(read).toHaveBeenCalledTimes(1)
    await act(async () => { await Promise.resolve() })
    expect(screen.getByTestId('hovercard-key')).toHaveTextContent('#122')
    expect(screen.getByTestId('hovercard-title')).toHaveTextContent('Build History fold')
    expect(screen.getByTestId('hovercard-status')).toHaveTextContent('In progress')
    expect(screen.getByTestId('hovercard-priority')).toHaveTextContent('High')
    expect(screen.getByTestId('hovercard-type')).toHaveTextContent('Feature')
    expect(screen.getByTestId('hovercard-tags')).toHaveTextContent('uipanel')
    expect(screen.getByTestId('hovercard-description')).toHaveTextContent('Line one Line two')
    // Leaving the chip for the card keeps it open; leaving the card closes it.
    fireEvent.pointerLeave(chip, { pointerType: 'mouse' })
    fireEvent.pointerEnter(hc, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(1000) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
    fireEvent.pointerLeave(hc, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.closeDelayMs + 10) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
  })

  it('a pointer that leaves before the delay opens nothing and reads nothing', () => {
    vi.useFakeTimers()
    const read = vi.fn(async () => null)
    const chip = show({ card: read })
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs / 2) })
    fireEvent.pointerLeave(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    expect(read).not.toHaveBeenCalled()
  })

  it('closes on Esc, and when the chip is pressed', () => {
    vi.useFakeTimers()
    const chip = show({})
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
    fireEvent.pointerDown(chip)
    expect(screen.queryByTestId('hovercard')).toBeNull()
  })

  it('a press, a text selection or a scroll inside the open card keeps it open', () => {
    vi.useFakeTimers()
    const chip = show({})
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    const hc = screen.getByTestId('hovercard')
    fireEvent.pointerLeave(chip, { pointerType: 'mouse' })
    fireEvent.pointerEnter(hc, { pointerType: 'mouse' })
    const title = screen.getByTestId('hovercard-title')
    fireEvent.pointerDown(title, { pointerType: 'mouse' })
    fireEvent.mouseDown(title)
    const range = document.createRange()
    range.selectNodeContents(title)
    window.getSelection()?.addRange(range)
    fireEvent.mouseUp(title)
    fireEvent.pointerUp(title, { pointerType: 'mouse' })
    fireEvent.scroll(hc)
    fireEvent.pointerDown(hc, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.getByTestId('hovercard')).toBe(hc)
    expect(window.getSelection()?.toString()).toBe('Build History fold')
  })

  it('a press or a click inside the card does not reach the chip\'s ancestors (React carries portal events up)', () => {
    vi.useFakeTimers()
    const row = vi.fn()
    render(<div onClick={row} onPointerDown={row} onMouseDown={row}><Hover target={card}><button type="button">row chip</button></Hover></div>)
    const chip = screen.getByRole('button', { name: 'row chip' })
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    const hc = screen.getByTestId('hovercard')
    fireEvent.pointerDown(hc)
    fireEvent.mouseDown(hc)
    fireEvent.click(hc)
    expect(row).not.toHaveBeenCalled()
    expect(screen.getByTestId('hovercard')).toBe(hc)
  })

  it('Esc closes it and reaches what has the focus (the composer); on the chip itself Esc is the card\'s', () => {
    vi.useFakeTimers()
    const outside = vi.fn()
    render(<div onKeyDown={e => { if (e.key === 'Escape') outside() }}><textarea aria-label="composer" /></div>)
    const chip = show({})
    const box = screen.getByRole('textbox', { name: 'composer' })
    act(() => box.focus())
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    fireEvent.keyDown(box, { key: 'Escape' })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    expect(outside).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(document.body, { key: 'Tab' })
    act(() => chip.focus())
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    const seen = vi.fn()
    window.addEventListener('keydown', seen)
    fireEvent.keyDown(chip, { key: 'Escape' })
    window.removeEventListener('keydown', seen)
    expect(screen.queryByTestId('hovercard')).toBeNull()
    expect(seen).not.toHaveBeenCalled()
  })

  it('focus handed back to the chip after a dialog closed with Esc does not open it', () => {
    vi.useFakeTimers()
    // As in a browser: focus put back after a key is visible focus.
    const matches = Element.prototype.matches
    const spy = vi.spyOn(Element.prototype, 'matches').mockImplementation(function (this: Element, q: string) { return q === ':focus-visible' ? this === document.activeElement : matches.call(this, q) })
    const chip = show({})
    fireEvent.keyDown(document.body, { key: 'Escape' })
    act(() => chip.focus())
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    act(() => chip.blur())
    fireEvent.keyDown(document.body, { key: 'Tab' })
    act(() => chip.focus())
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
    spy.mockRestore()
  })

  it('opens on keyboard focus too', () => {
    vi.useFakeTimers()
    const chip = show({})
    fireEvent.keyDown(document.body, { key: 'Tab' })
    act(() => { chip.focus() })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
    act(() => { chip.blur() })
    act(() => { vi.advanceTimersByTime(HOVERCARD.closeDelayMs) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
  })

  it('a click that focuses the chip does not open it by focus', () => {
    vi.useFakeTimers()
    const chip = show({})
    fireEvent.pointerDown(chip, { pointerType: 'mouse' })
    act(() => { chip.focus() })
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
  })

  it('a new clock forgets the warm window: the next card waits the full delay', () => {
    vi.useFakeTimers()
    render(<><Hover target={card}><button type="button">a</button></Hover><Hover target={card}><button type="button">b</button></Hover></>)
    fireEvent.pointerEnter(screen.getByRole('button', { name: 'a' }), { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    fireEvent.keyDown(document.body, { key: 'Escape' })
    setHovercardClock(null)
    fireEvent.pointerEnter(screen.getByRole('button', { name: 'b' }), { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.warmDwellMs + 10) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
  })

  it('the delay is the one in HOVERCARD', () => {
    vi.useFakeTimers()
    HOVERCARD.openDelayMs = 1000
    const chip = show({})
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(600) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    act(() => { vi.advanceTimersByTime(400) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
  })

  it('a touch pointer never opens one', () => {
    vi.useFakeTimers()
    const chip = show({})
    fireEvent.pointerEnter(chip, { pointerType: 'touch' })
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
  })

  it('a device that cannot hover gets the chip alone, its tooltip kept', () => {
    hoverDevice(false)
    const chip = show({})
    expect(chip.parentElement?.classList.contains('hc-anchor')).toBe(false)
    expect(chip).toHaveAttribute('title', 'native tip')
  })

  it('the switch turns every hovercard off, and back on', () => {
    vi.useFakeTimers()
    setHovercardsEnabled(false)
    const chip = show({})
    expect(chip.parentElement?.classList.contains('hc-anchor')).toBe(false)
    fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    act(() => setHovercardsEnabled(true))
    expect(screen.getByRole('button', { name: 'chip' }).parentElement?.classList.contains('hc-anchor')).toBe(true)
  })
})

async function openOn(chip: HTMLElement) {
  fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
  await act(async () => { await new Promise(r => setTimeout(r, HOVERCARD.openDelayMs + 20)) })
  await flush()
  return screen.getByTestId('hovercard')
}

describe('Hover: what a card shows', () => {
  it('the worker on it, and only the fields HOVERCARD names', async () => {
    HOVERCARD.fields.card = ['status']
    const chip = show({ card: async () => ({ priority: 'High', description: 'Body' }), worker: () => 'Build History fold' })
    await openOn(chip)
    expect(screen.getByTestId('hovercard-status')).toHaveTextContent('Ready')
    expect(screen.queryByTestId('hovercard-priority')).toBeNull()
    expect(screen.queryByTestId('hovercard-description')).toBeNull()
    expect(screen.queryByTestId('hovercard-worker')).toBeNull()
  })

  it('says when the card was read live from the board (the rows have no mark for it)', async () => {
    const chip = show({ card: async () => ({ status: 'In progress', live: true }) })
    await openOn(chip)
    expect(screen.getByTestId('hovercard-live')).toHaveTextContent('Live from the board')
  })

  it('a card from the agent\'s copy alone says nothing of the board', async () => {
    const chip = show({ card: async () => null })
    await openOn(chip)
    expect(screen.queryByTestId('hovercard-live')).toBeNull()
  })

  it('names the worker on it in place of the assignee', async () => {
    const chip = show({ worker: () => 'Port the parser' }, { ...card, ticket: { assignee: 'Someone' } })
    await openOn(chip)
    expect(screen.getByTestId('hovercard-worker')).toHaveTextContent('↳ Port the parser')
    expect(screen.queryByTestId('hovercard-assignee')).toBeNull()
  })
})

const art = (o: Partial<ArtifactInfo>): ArtifactInfo => ({ id: 'a1', name: 'notes.md', size: 2048, content_type: 'text/markdown', view: 'markdown', version: 2, latest_version: 3, origin: 'agent', created_at: '2026-10-20T10:00:00Z', ...o })

describe('Hover: what a file shows', () => {
  it('a text file: its first lines, and name, version, size, who and when, an open review', async () => {
    const text = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join('\n')
    const files = filesWith({ a1: text })
    const chip = show({ files, reviewOpen: (_s, id) => id === 'a1' }, { kind: 'file', sessionId: 's1', id: 'a1', artifact: art({}) })
    expect(files.raw).not.toHaveBeenCalled()
    await openOn(chip)
    const lines = screen.getByTestId('hovercard-lines')
    expect(lines.textContent!.split('\n')).toHaveLength(HOVERCARD.excerptLines)
    expect(lines).toHaveClass('has-more')
    expect(screen.getByTestId('hovercard-name')).toHaveTextContent('notes.md')
    expect(screen.getByTestId('hovercard-version')).toHaveTextContent('v2 of 3')
    expect(screen.getByTestId('hovercard-size')).toHaveTextContent('2.0 KB')
    expect(screen.getByTestId('hovercard-publisher')).toHaveTextContent('by the agent')
    expect(screen.getByTestId('hovercard-review')).toHaveTextContent('review open')
  })

  it('an image: a thumbnail, its object URL revoked when the card closes; only the last few kept', async () => {
    const shots = Object.fromEntries(Array.from({ length: 6 }, (_, i) => [`i${i}`, new Blob(['png'], { type: 'image/png' })]))
    const files = filesWith(shots)
    let n = 0
    URL.createObjectURL = vi.fn(() => `blob:thumb-${++n}`)
    URL.revokeObjectURL = vi.fn()
    const chip = show({ files }, { kind: 'file', sessionId: 's1', id: 'i0', artifact: art({ id: 'i0', name: 'shot.png', view: 'image', content_type: 'image/png', origin: 'user' }) })
    await openOn(chip)
    expect(screen.getByRole('img', { name: 'shot.png' })).toHaveAttribute('src', 'blob:thumb-1')
    expect(screen.getByTestId('hovercard-publisher')).toHaveTextContent('by you')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:thumb-1')
    // Five more images hovered: the first is out of the cache and read again.
    for (let i = 1; i < 6; i++) {
      const { unmount } = render(<HovercardProvider value={{ files }}><Hover target={{ kind: 'file', sessionId: 's1', id: `i${i}`, artifact: art({ id: `i${i}`, name: `s${i}.png`, view: 'image', content_type: 'image/png' }) }}><button type="button">{`img ${i}`}</button></Hover></HovercardProvider>)
      await openOn(screen.getByRole('button', { name: `img ${i}` }))
      fireEvent.keyDown(document.body, { key: 'Escape' })
      unmount()
    }
    await openOn(chip)
    expect(files.raw.mock.calls.filter(c => c[1] === 'i0')).toHaveLength(2)
  })

  it('a diff: its files with +/- counts', async () => {
    const patch = ['diff --git a/x.ts b/x.ts', '--- a/x.ts', '+++ b/x.ts', '@@ -1,2 +1,2 @@', '-a', '+b', '+c', 'diff --git a/y.go b/y.go', '--- a/y.go', '+++ b/y.go', '@@ -1 +1 @@', '-q', '+r', ''].join('\n')
    const files = filesWith({ d1: patch })
    const chip = show({ files }, { kind: 'file', sessionId: 's1', id: 'd1', artifact: art({ id: 'd1', name: 'change.diff', view: 'diff', content_type: 'text/x-diff' }) })
    await openOn(chip)
    const diff = screen.getByTestId('hovercard-diff')
    expect(diff).toHaveTextContent('2 files · +3 −2')
    expect(diff).toHaveTextContent('x.ts+2 −1')
    expect(diff).toHaveTextContent('y.go+1 −1')
  })

  it('a Mermaid file and a Vega-Lite chart: a small render (the viewer\'s renderer)', async () => {
    const files = filesWith({ m1: 'graph TD; A-->B', v1: '{"$schema":"https://vega.github.io/schema/vega-lite/v5.json","mark":"bar"}' })
    const { unmount } = render(<HovercardProvider value={{ files }}>
      <Hover target={{ kind: 'file', sessionId: 's1', id: 'm1', artifact: art({ id: 'm1', name: 'flow.mmd', view: 'text', content_type: 'text/plain' }) }}><button type="button">m</button></Hover>
      <Hover target={{ kind: 'file', sessionId: 's1', id: 'v1', artifact: art({ id: 'v1', name: 'chart.json', view: 'json', content_type: 'application/json' }) }}><button type="button">v</button></Hover>
    </HovercardProvider>)
    await openOn(screen.getByRole('button', { name: 'm' }))
    expect(screen.getByTestId('visual-thumb')).toHaveAttribute('data-kind', 'mermaid')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(screen.getByRole('button', { name: 'v' }))
    expect(screen.getByTestId('visual-thumb')).toHaveAttribute('data-kind', 'vega-lite')
    unmount()
  })

  it('a file named by id only is found in the session\'s list; a second hover reads nothing again', async () => {
    const files = filesWith({ a1: 'hello\n' })
    const chip = show({ files, artifact: (_s, id) => (id === 'a1' ? art({}) : undefined) }, { kind: 'file', sessionId: 's1', id: 'a1' })
    await openOn(chip)
    expect(screen.getByTestId('hovercard-lines')).toHaveTextContent('hello')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(chip)
    expect(files.raw).toHaveBeenCalledTimes(1)
  })
})

const SHA = 'a'.repeat(40)
const COMMIT_PATCH = [
  `commit ${SHA}`, 'Author: Pat Example', 'Date:   2026-10-20T10:00:00+00:00', '', '    Build the History fold', '', '    Folds the done items into one row.', '',
  'diff --git a/h.tsx b/h.tsx', '--- a/h.tsx', '+++ b/h.tsx', '@@ -1 +1,2 @@', '-x', '+y', '+z', '',
].join('\n')

describe('Hover: what a commit shows', () => {
  it('its message, author and files, from the published diff; read once', async () => {
    const files = filesWith({ d1: COMMIT_PATCH })
    render(<HovercardProvider value={{ files }}><CommitChips commits={[{ sha: SHA }]} onOpen={() => {}} hover={{ sessionId: 's1', diff: 'd1' }} /></HovercardProvider>)
    const chip = screen.getByRole('button', { name: /Open commit aaaaaaa/ })
    expect(files.raw).not.toHaveBeenCalled()
    await openOn(chip)
    expect(screen.getByTestId('hovercard-sha')).toHaveTextContent('aaaaaaa')
    expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Build the History fold')
    expect(screen.getByTestId('hovercard')).toHaveTextContent('Folds the done items into one row.')
    expect(screen.getByTestId('hovercard-author')).toHaveTextContent('Pat Example')
    expect(screen.getByTestId('hovercard-diff')).toHaveTextContent('h.tsx+2 −1')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(chip)
    expect(files.raw).toHaveBeenCalledTimes(1)
  })

  it('a diff over the preview limit is never read: by the list\'s size, or dropped unparsed when larger than said', async () => {
    const files = filesWith({ d1: COMMIT_PATCH, d2: 'x'.repeat(INLINE_TEXT_MAX + 1) })
    render(<HovercardProvider value={{ files, artifact: (_s, id) => (id === 'd1' ? art({ id: 'd1', name: 'big.diff', view: 'diff', size: INLINE_TEXT_MAX + 1 }) : undefined) }}>
      <CommitChips commits={[{ sha: SHA }]} onOpen={() => {}} hover={{ sessionId: 's1', diff: 'd1' }} />
      <CommitChips commits={[{ sha: 'b'.repeat(40) }]} onOpen={() => {}} hover={{ sessionId: 's1', diff: 'd2' }} />
    </HovercardProvider>)
    await openOn(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    expect(screen.getByTestId('hovercard-commit-none')).toHaveTextContent('too large')
    expect(files.raw).not.toHaveBeenCalled()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(screen.getByRole('button', { name: /Open commit bbbbbbb/ }))
    expect(screen.getByTestId('hovercard-commit-none')).toHaveTextContent('too large')
  })

  it('every commit of one diff reuses its one read', async () => {
    const SHA2 = 'b'.repeat(40)
    const two = COMMIT_PATCH + COMMIT_PATCH.replace(SHA, SHA2).replace('Build the History fold', 'Second commit')
    const files = filesWith({ d1: two })
    render(<HovercardProvider value={{ files }}>
      <CommitChips commits={[{ sha: SHA }, { sha: SHA2 }]} onOpen={() => {}} hover={{ sessionId: 's1', diff: 'd1' }} />
    </HovercardProvider>)
    await openOn(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Build the History fold')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(screen.getByRole('button', { name: /Open commit bbbbbbb/ }))
    expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Second commit')
    expect(files.raw).toHaveBeenCalledTimes(1)
  })

  it('a transport without a capped read: a diff of unknown size is not read; a listed small one is', async () => {
    const files = filesWith({ d1: COMMIT_PATCH, d2: COMMIT_PATCH }, false)
    render(<HovercardProvider value={{ files, artifact: (_s, id) => (id === 'd2' ? art({ id: 'd2', name: 'small.diff', view: 'diff', size: COMMIT_PATCH.length }) : undefined) }}>
      <CommitChips commits={[{ sha: SHA }]} onOpen={() => {}} hover={{ sessionId: 's1', diff: 'd1' }} />
      <CommitChips commits={[{ sha: 'a'.repeat(39) + 'b' }]} onOpen={() => {}} hover={{ sessionId: 's1', diff: 'd2' }} />
    </HovercardProvider>)
    const [unknown, listed] = screen.getAllByRole('button', { name: /Open commit/ })
    await openOn(unknown)
    expect(screen.getByTestId('hovercard-commit-none')).toBeInTheDocument()
    expect(files.raw).not.toHaveBeenCalled()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(listed)
    expect(files.raw).toHaveBeenCalledTimes(1)
  })

  it('without a diff and no way to read the session: the short sha alone, no dead-end text', async () => {
    render(<CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} />)
    const hc = await openOn(screen.getByRole('button', { name: `Copy commit ${SHA}` }))
    expect(screen.getByTestId('hovercard-sha')).toHaveTextContent('aaaaaaa')
    expect(screen.queryByTestId('hovercard-commit-none')).toBeNull()
    expect(hc.textContent).toBe('aaaaaaa')
  })
})

const DETAILS: CommitDetails = {
  sha: SHA, short: 'aaaaaaa', subject: 'Read commits from the workspace', body: 'No published diff needed.', author: 'Pat Example',
  author_date: '2026-10-21T10:00:00Z', parents: ['b'.repeat(40)], files: [{ path: 'lookup.go', adds: 40, dels: 2 }, { path: 'old.go', old_path: 'older.go', adds: 0, dels: 0 }], adds: 40, dels: 2,
}

function filesWithCommits(bytes: Record<string, string>, read: (s: string, sha: string) => Promise<CommitDetails | null>) {
  const commit = vi.fn(read)
  return Object.assign(filesWith(bytes), { commit, commitDiff: vi.fn(async () => null) })
}

describe('Hover: a commit read from the session\'s workspace (card #131)', () => {
  it('no published diff: the commit read through the session, once, with the repository\'s name', async () => {
    const files = filesWithCommits({}, async () => DETAILS)
    render(<HovercardProvider value={{ files, repo: () => '/home/someone/repos/blerg' }}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></HovercardProvider>)
    const chip = screen.getByRole('button', { name: `Copy commit ${SHA}` })
    expect(files.commit).not.toHaveBeenCalled()
    await openOn(chip)
    expect(files.commit).toHaveBeenCalledWith('s1', SHA)
    expect(screen.getByTestId('hovercard-repo')).toHaveTextContent(/^blerg$/)
    expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Read commits from the workspace')
    expect(screen.getByTestId('hovercard')).toHaveTextContent('No published diff needed.')
    expect(screen.getByTestId('hovercard-author')).toHaveTextContent('Pat Example')
    expect(screen.getByTestId('hovercard-diff')).toHaveTextContent('2 files')
    expect(screen.getByTestId('hovercard-diff')).toHaveTextContent('lookup.go+40 −2')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(chip)
    expect(files.commit).toHaveBeenCalledTimes(1)
  })

  it('a published diff that does not hold the commit: read through the session instead', async () => {
    const files = filesWithCommits({ d1: COMMIT_PATCH }, async () => ({ ...DETAILS, sha: 'c'.repeat(40) }))
    render(<HovercardProvider value={{ files }}><CommitChips commits={[{ sha: 'c'.repeat(40) }]} onOpen={() => {}} hover={{ sessionId: 's1', diff: 'd1' }} /></HovercardProvider>)
    await openOn(screen.getByRole('button', { name: /Open commit ccccccc/ }))
    expect(files.raw).toHaveBeenCalledTimes(1)
    expect(files.commit).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Read commits from the workspace')
  })

  it('a commit that cannot be read: the short sha and the repository, nothing else; asked again only after a minute', async () => {
    const files = filesWithCommits({}, async () => null)
    render(<HovercardProvider value={{ files, repo: () => 'blerg' }}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></HovercardProvider>)
    const chip = screen.getByRole('button', { name: `Copy commit ${SHA}` })
    const hc = await openOn(chip)
    expect(hc.textContent).toBe('aaaaaaablerg')
    expect(screen.queryByTestId('hovercard-commit-none')).toBeNull()
    expect(screen.queryByTestId('hovercard-subject')).toBeNull()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(chip)
    expect(files.commit).toHaveBeenCalledTimes(1)
    const now = Date.now()
    const later = vi.spyOn(Date, 'now').mockReturnValue(now + COMMIT_MISS_MS + 1)
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(chip)
    later.mockRestore()
    expect(files.commit).toHaveBeenCalledTimes(2)
  })

  it('another account signed in: what was read under the first one is read again', async () => {
    const files = filesWithCommits({}, async () => DETAILS)
    let who = 'alice'
    render(<HovercardProvider value={{ files, account: () => who }}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></HovercardProvider>)
    const chip = screen.getByRole('button', { name: `Copy commit ${SHA}` })
    await openOn(chip)
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(chip)
    expect(files.commit).toHaveBeenCalledTimes(1)
    who = 'bob'
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(chip)
    expect(files.commit).toHaveBeenCalledTimes(2)
  })

  it('control and bidirectional characters in the subject, author and file names are shown, not obeyed', async () => {
    const files = filesWithCommits({}, async () => ({
      ...DETAILS, subject: 'Fix \u202Etxt.exe', author: 'Pat\u0007 Example',
      files: [{ path: 'evil\u202Egpj.exe', adds: 1, dels: 0 }],
    }))
    render(<HovercardProvider value={{ files }}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></HovercardProvider>)
    await openOn(screen.getByRole('button', { name: `Copy commit ${SHA}` }))
    expect(screen.getByTestId('hovercard-subject').textContent).toBe('Fix \\u{202E}txt.exe')
    expect(screen.getByTestId('hovercard-author').textContent).toContain('Pat\\u{0007} Example')
    const path = screen.getByTestId('hovercard-diff').querySelector('.hc-path')!
    expect(path.tagName).toBe('BDI')
    expect(path.textContent).toBe('evil\\u{202E}gpj.exe')
    const text = screen.getByTestId('hovercard').textContent ?? ''
    expect(text.includes('\u202E') || text.includes('\u0007')).toBe(false)
  })

  it('a read that fails outright reads the same as one that found nothing', async () => {
    const files = filesWithCommits({}, async () => { throw new TypeError('offline') })
    render(<HovercardProvider value={{ files, repo: () => 'blerg' }}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></HovercardProvider>)
    const hc = await openOn(screen.getByRole('button', { name: `Copy commit ${SHA}` }))
    expect(hc.textContent).toBe('aaaaaaablerg')
  })

  it('a chip whose sha is not hex is never asked about', async () => {
    const files = filesWithCommits({}, async () => DETAILS)
    render(<HovercardProvider value={{ files }}><CommitChips commits={[{ sha: 'HEAD~1' }]} hover={{ sessionId: 's1' }} /></HovercardProvider>)
    await openOn(screen.getByRole('button', { name: 'Copy commit HEAD~1' }))
    expect(files.commit).not.toHaveBeenCalled()
  })
})

describe('Hover: in the chat', () => {
  it('a pinned ticket\'s card (as the host reads it) and a checklist item\'s commit (from its diff, by the chat\'s files)', async () => {
    const files = filesWith({ d1: COMMIT_PATCH })
    const chat = { sessionId: 's1', files, view: () => {}, latestVersion: () => undefined, byName: () => undefined, reviewFor: async () => null, submitFeedback: async () => true } as unknown as ChatContextValue
    const ev = { type: 'agent_event', session_id: 's1', client_event_id: 'ev1', ts: '2026-10-21T10:00:00Z', kind: 'ticket',
      payload: { action: 'pin', ticket: { source: 'blerg-board', key: '#122', board_id: 'b1', title: 'Build History fold', status: 'In progress' } } } as AgentEvent
    const read = vi.fn(async (): Promise<CardPreview> => ({ status: 'Done', done: true }))
    render(<ChatContext.Provider value={chat}><HovercardProvider value={{ card: read }}>
      <TicketCard ev={ev} />
      <ChecklistItems items={[{ id: 'i1', text: 'Fold', done: true, commits: [SHA], diff: 'd1' }]} onViewDiff={() => {}} />
    </HovercardProvider></ChatContext.Provider>)
    await openOn(screen.getByTestId('ticket-chip'))
    expect(read).toHaveBeenCalledWith(expect.objectContaining({ kind: 'card', key: '#122', boardId: 'b1' }))
    expect(screen.getByTestId('hovercard-status')).toHaveTextContent('Done')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await openOn(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Build the History fold')
  })
})

const rect = (left: number, top: number, width: number, height: number) => ({ left, top, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) }) as DOMRect

describe('placeCard: beside the list, never over the rows below', () => {
  it('a chip in the side panel: left of the panel, top at the chip', () => {
    const panel = rect(940, 0, 340, 900)
    const chip = rect(960, 200, 120, 20)
    expect(placeCard(chip, [panel, chip], 360, 200, 1280, 900)).toEqual({ side: 'left', left: 940 - 8 - 360, top: 194, maxHeight: 884 })
  })
  it('a drawer at a narrow desktop width: left of the drawer, over the backdrop', () => {
    const drawer = rect(1023 - 380, 0, 380, 700)
    const chip = rect(670, 100, 100, 20)
    expect(placeCard(chip, [drawer, chip], 360, 200, 1023, 700).side).toBe('left')
  })
  it('390 px: no room beside anything, so below the chip (above when below has less room), on screen', () => {
    const chip = rect(20, 100, 120, 20)
    const below = placeCard(chip, [rect(0, 0, 390, 844), chip], 374, 200, 390, 844)
    expect(below).toMatchObject({ side: 'below', top: 128, left: 8 })
    const low = rect(20, 780, 120, 20)
    expect(placeCard(low, [low], 374, 300, 390, 844).side).toBe('above')
  })
  it('a card taller than the viewport is capped and kept on screen', () => {
    const panel = rect(940, 0, 340, 900)
    const chip = rect(960, 700, 120, 20)
    const p = placeCard(chip, [panel, chip], 360, 5000, 1280, 900)
    expect(p.maxHeight).toBe(884)
    expect(p.top).toBe(8)
  })
  it('a chip partly scrolled off the top: the card starts on screen', () => {
    const panel = rect(940, 0, 340, 900)
    const chip = rect(960, -10, 120, 20)
    expect(placeCard(chip, [panel, chip], 360, 200, 1280, 900).top).toBe(8)
  })
  it('a wide row with the chip at its far end: beside the chip, not the row\'s far edge', () => {
    const row = rect(100, 300, 3000, 24)
    const chip = rect(2900, 302, 60, 20)
    const p = placeCard(chip, [row, chip], 360, 200, 3840, 2160)
    expect(p.side).toBe('right')
    expect(p.left).toBe(3100 + 8)
    const far = rect(150, 302, 60, 20)
    expect(placeCard(far, [row, far], 360, 200, 3840, 2160)).toMatchObject({ side: 'right', left: 210 + 8 })
  })
})

describe('headsFor: direction over a window, with tolerance', () => {
  const card = rect(100, 0, 360, 600)
  it('toward the card within the angle; straight down along it is not; a tiny move is not judged', () => {
    expect(headsFor({ x: 500, y: 210 }, { x: 510, y: 200 }, card)).toBe(true)
    expect(headsFor({ x: 510, y: 240 }, { x: 510, y: 200 }, card)).toBe(false)
    expect(headsFor({ x: 510, y: 202 }, { x: 510, y: 200 }, card)).toBe(true)
    expect(headsFor({ x: 520, y: 200 }, { x: 510, y: 200 }, card)).toBe(false)
    // A staircase down toward the card's lower half (1 left for 3 down) heads for it; a drift of
    // 1 left for 6 down along it does not.
    expect(headsFor({ x: 507, y: 209 }, { x: 510, y: 200 }, card)).toBe(true)
    expect(headsFor({ x: 509, y: 206 }, { x: 510, y: 200 }, card)).toBe(false)
  })
})

it('keeps same-SHA hover reads separate for each member and runtime generation', async () => {
  const first = { member_id: 'one', launch_generation: 'launch', generation: 7 }
  const second = { member_id: 'two', launch_generation: 'launch', generation: 7 }
  const commit = vi.fn(async (_session: string, _sha: string, _signal?: AbortSignal, member?: typeof first) => ({ ...DETAILS, ...member, subject: `Member ${member?.member_id} generation ${member?.generation}` }))
  const files = Object.assign(filesWith({ d1: COMMIT_PATCH }), { commit })
  const { rerender } = render(<HovercardProvider value={{ files }}><CommitChips commits={[{ sha: SHA, member: first }, { sha: SHA, member: second }]} hover={{ sessionId: 's1', diff: 'd1' }} /></HovercardProvider>)
  const chips = screen.getAllByRole('button', { name: `Copy commit ${SHA}` })
  await openOn(chips[0]!)
  expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Member one generation 7')
  fireEvent.keyDown(document.body, { key: 'Escape' })
  await openOn(chips[1]!)
  expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Member two generation 7')
  expect(commit).toHaveBeenCalledWith('s1', SHA, undefined, first)
  expect(commit).toHaveBeenCalledWith('s1', SHA, undefined, second)
  fireEvent.keyDown(document.body, { key: 'Escape' })
  const replacement = { ...second, generation: 8 }
  rerender(<HovercardProvider value={{ files }}><CommitChips commits={[{ sha: SHA, member: replacement }]} hover={{ sessionId: 's1', diff: 'd1' }} /></HovercardProvider>)
  await openOn(screen.getByRole('button', { name: `Copy commit ${SHA}` }))
  expect(screen.getByTestId('hovercard-subject')).toHaveTextContent('Member two generation 8')
  expect(commit).toHaveBeenCalledTimes(3)
})
