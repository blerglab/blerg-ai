import {fireEvent, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import DiffView, {DIFF_FILE_LINES} from './DiffView'
import type {Anchor, ReviewRequest} from '../model/review'

const PATCH = 'diff --git a/f.ts b/f.ts\n--- a/f.ts\n+++ b/f.ts\n@@ -1,4 +1,4 @@\n same\n-oldA\n-oldB\n+newA\n+newB\n tail\n'
const anchor = (side: 'old' | 'new' = 'new', start = 2, end = 3): Anchor => ({kind: 'text', quote: side === 'new' ? 'newA\nnewB' : 'oldA\noldB', diff: {file: 'f.ts', side, start, end, hunk: 1, fileIndex: 1}})
const request = (id = 'request-1'): ReviewRequest => ({id, anchor: anchor(), text: 'Explain this change', status: 'open', createdAt: '2026-10-18T12:00:00Z'})
afterEach(() => vi.unstubAllGlobals())

describe('inline diff comment slots', () => {
  it('opens a comment directly from a gutter plus button', () => {
    const onComment = vi.fn()
    render(<DiffView text={PATCH} onComment={onComment} />)
    fireEvent.click(screen.getByRole('button', {name: 'Comment on new line 2 in f.ts'}))
    expect(onComment).toHaveBeenCalledWith(expect.objectContaining({quote: 'newA', diff: expect.objectContaining({side: 'new', start: 2, end: 2})}))
  })

  it('comments on the selected range when its gutter plus is clicked', () => {
    const onComment = vi.fn()
    render(<DiffView text={PATCH} selection={anchor()} onComment={onComment} />)
    fireEvent.click(screen.getByRole('button', {name: 'Comment on new line 3 in f.ts'}))
    expect(onComment).toHaveBeenCalledWith(anchor())
  })

  it.each(['unified', 'split'])('places full-width saved threads below the range end in %s', mode => {
    vi.stubGlobal('matchMedia', () => ({matches: true, addEventListener: () => {}, removeEventListener: () => {}}))
    const {container} = render(<DiffView text={PATCH} requests={[request()]} renderThread={r => <article data-testid="thread">{r.text}</article>} />)
    if (mode === 'split') fireEvent.click(screen.getByRole('button', {name: 'Side by side'}))
    const thread = screen.getByTestId('thread')
    const row = thread.closest('tr')!
    expect(row.previousElementSibling?.textContent).toContain('newB')
    expect(thread.closest('td')).toHaveAttribute('colspan', mode === 'split' ? '4' : '3')
    expect(container.querySelectorAll('[data-testid="thread"]')).toHaveLength(1)
  })

  it('places the draft below the chosen final line', () => {
    render(<DiffView text={PATCH} draftAnchor={anchor()} renderDraft={a => <textarea aria-label="Inline comment" defaultValue={a.quote} />} />)
    const draft = screen.getByRole('textbox', {name: 'Inline comment'})
    expect(draft.closest('tr')?.previousElementSibling?.textContent).toContain('newB')
    expect(draft.closest('td')).toHaveAttribute('colspan', '3')
  })

  it('does not duplicate a context-line thread shared by old and new columns', () => {
    const context = {...request(), anchor: {kind: 'text' as const, quote: 'same', diff: {file: 'f.ts', side: 'new' as const, start: 1, end: 1, hunk: 1, fileIndex: 1}}}
    render(<DiffView text={PATCH} requests={[context]} renderThread={() => <div data-testid="thread">One comment</div>} />)
    expect(screen.getAllByTestId('thread')).toHaveLength(1)
  })

  it('never renders stale or malformed anchor slots', () => {
    const stale = {...anchor(), quote: 'code from another patch'}
    const renderThread = vi.fn(() => <div>Wrong thread</div>)
    const renderDraft = vi.fn(() => <textarea />)
    render(<DiffView text={PATCH} requests={[{...request(), anchor: stale}]} renderThread={renderThread} draftAnchor={stale} renderDraft={renderDraft} />)
    expect(renderThread).not.toHaveBeenCalled()
    expect(renderDraft).not.toHaveBeenCalled()
  })

  it('retains a folded-line thread once without expanding unrelated context', () => {
    const lines = Array.from({length: 30}, (_, index) => ` context${index + 2}`).join('\n')
    const text = `--- a/f.ts\n+++ b/f.ts\n@@ -1,32 +1,32 @@\n-old\n+new\n${lines}\n-end\n+END\n`
    const hidden = {...request(), anchor: {kind: 'text' as const, quote: 'context15', diff: {file: 'f.ts', side: 'new' as const, start: 15, end: 15, fileIndex: 1, hunk: 1}}}
    const {container} = render(<DiffView text={text} requests={[hidden]} renderThread={() => <article data-testid="thread">Folded comment</article>} />)
    expect(screen.getAllByTestId('thread')).toHaveLength(1)
    expect(screen.getByText('f.ts · new line 15 · Code folded')).toBeInTheDocument()
    expect(container.querySelectorAll('.diff-row').length).toBeLessThan(20)
    fireEvent.click(screen.getByRole('button', {name: /unchanged lines/}))
    expect(screen.getAllByTestId('thread')).toHaveLength(1)
    expect(screen.queryByText(/Code folded/)).toBeNull()
    expect(screen.getByTestId('thread').closest('tr')?.previousElementSibling?.textContent).toContain('context15')
  })

  it('keeps the code-row cap and shows a focused thread beyond it exactly once', () => {
    const count = DIFF_FILE_LINES + 20
    const text = '--- /dev/null\n+++ b/large.txt\n@@ -0,0 +1,' + count + ' @@\n' + Array.from({length: count}, (_, index) => '+value' + (index + 1)).join('\n')
    const last = {...request(), anchor: {kind: 'text' as const, quote: 'value' + count, diff: {file: 'large.txt', side: 'new' as const, start: count, end: count, hunk: 1, fileIndex: 1}}}
    const renderThread = () => <article data-testid="thread">Last-line discussion</article>
    const {container, rerender} = render(<DiffView text={text} requests={[last]} renderThread={renderThread} />)
    expect(screen.getAllByTestId('thread')).toHaveLength(1)
    expect(screen.getByText(`large.txt · new line ${count} · Code not shown`)).toBeInTheDocument()
    expect(container.querySelectorAll('.diff-row').length).toBeLessThanOrEqual(DIFF_FILE_LINES)
    rerender(<DiffView text={text} requests={[last]} focusedId={last.id} renderThread={renderThread} />)
    expect(screen.getAllByTestId('thread')).toHaveLength(1)
    expect(screen.queryByText(/Code not shown/)).toBeNull()
    expect(screen.getByTestId('thread').closest('tr')?.previousElementSibling?.textContent).toContain('value' + count)
    expect(container.querySelectorAll('.diff-row').length).toBeLessThanOrEqual(DIFF_FILE_LINES + 7)
  }, 15_000)
})
