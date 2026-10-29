// A Blerg notice row (a child's turn, a review outcome) draws its text as inline chat Markdown with
// references, and treats it as untrusted: nothing but the inline subset renders (card #275).
import type { CardTarget } from './Hovercard'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import Markdown from './Markdown'
import { CommitViewerHost } from './CommitViewer'
import type { TransportFiles } from '../transport/types'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import { DEBOUNCE_MS } from '../model/refs'
import type { AgentEvent } from '../types'

const SHA = '3e6f638c9d1e4b7a0c3f5e6d7a8b9c0d1e2f3a4b'

function note(text: string): AgentEvent {
  return mk({ seq: 1, kind: 'user_message', ts: '2026-10-14T00:00:00Z', payload: { text, source: 'child_turn' } })
}

function mount(text: string) {
  seed(note(text))
  const t = createFakeTransport()
  const commit = vi.fn(async (_s: string, sha: string) => (SHA.startsWith(sha.toLowerCase()) ? { sha: SHA } : null))
  const commitDiff = vi.fn(() => new Promise<never>(() => {}))
  const files = { ...(t.transport.files as object), commit, commitDiff } as unknown as TransportFiles
  const transport = { ...t.transport, files } as typeof t.transport
  const target: CardTarget = { kind: 'card', source: 'blerg-board', key: '#258', boardId: 'b1', title: 'Search ignores' }
  const cards = vi.fn(async (numbers: number[]) => new Map(numbers.filter(n => n === 258).map(n => [n, target])))
  const openCard = vi.fn()
  render(
    <CommitViewerHost files={files}>
      <ChatView session={SESSION} transport={transport} meta={meta({ status: 'idle', started_at: '2026-10-14T00:00:00Z' })} refs={{ cards, openCard, commits: true }} />
    </CommitViewerHost>,
  )
  return { commit, commitDiff, openCard }
}

async function settle() {
  await act(async () => { await vi.advanceTimersByTimeAsync(DEBOUNCE_MS + 10) })
}

describe('Blerg notice rows are inline Markdown', () => {
  beforeEach(() => { resetChatState(); vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('backticked hashes are code, and bold is bold', async () => {
    mount('Child "Fix" finished its turn: "Head: `3e6f638c`. **Redesign — 3 high** done."')
    await settle()
    await settle()
    const row = screen.getByTestId('system-note')
    expect(row.textContent).not.toContain('`')
    expect(row.textContent).not.toContain('**')
    expect(row.querySelector('code')?.textContent).toBe('3e6f638c')
    expect(row.querySelector('strong')?.textContent).toBe('Redesign — 3 high')
    expect(screen.getByTestId('md-ref').querySelector('code')?.textContent).toBe('3e6f638c')
  })

  it('confirmed card and commit references are buttons that open the card and the commit', async () => {
    const { commitDiff, openCard } = mount('Child "#258 fix" finished its turn: "Head: `3e6f638c`."')
    await settle()
    await settle()
    const refs = screen.getAllByTestId('md-ref')
    expect(refs.map(r => r.textContent)).toEqual(['#258', '3e6f638c'])
    fireEvent.click(refs[0])
    expect(openCard).toHaveBeenCalledWith(expect.objectContaining({ key: '#258', boardId: 'b1' }))
    await act(async () => { fireEvent.click(refs[1]) })
    expect(screen.getByTestId('commit-viewer-overlay')).toBeInTheDocument()
    expect(commitDiff).toHaveBeenCalled()
    expect((commitDiff.mock.calls[0] as unknown[])[1]).toBe(SHA)
  })

  it('only the inline subset renders: no HTML, images, headings, lists or code blocks', () => {
    mount('Child finished: <script>window.__x=1</script><b>raw</b> ![pic](https://example.com/a.png) # Heading\n\n- item\n\n```\nblock\n```')
    const row = screen.getByTestId('system-note')
    expect(row.querySelector('script, b, img, h1, ul, li, pre')).toBeNull()
    expect((window as unknown as { __x?: number }).__x).toBeUndefined()
    expect(row.textContent).toContain('<script>')
  })
})

describe('Markdown inline drops block structure but keeps the words', () => {
  const BLOCKS = 'h1, h2, h3, ul, ol, li, blockquote, table, tr, td, th, pre, input, hr'
  function text(md: string) {
    const { container } = render(<Markdown inline text={md} />)
    return container
  }

  it.each([
    ['heading', '# Big heading\n\nafter', ['Big heading', 'after']],
    ['list', '- first item\n- second item', ['first item', 'second item']],
    ['task list', '- [x] done thing\n- [ ] open thing', ['done thing', 'open thing']],
    ['quote', '> quoted words', ['quoted words']],
    ['table', '| a | b |\n|---|---|\n| cell1 | cell2 |', ['cell1', 'cell2']],
    ['fenced block', '```js\nconst fenced = 1\n```', ['const fenced = 1']],
  ])('%s', (_n, md, kept) => {
    const c = text(md)
    expect(c.querySelector(BLOCKS)).toBeNull()
    for (const k of kept) expect(c.textContent).toContain(k)
  })

  it('raw HTML stays literal text', () => {
    const c = text('<script>window.__y=1</script> <b>raw</b>')
    expect(c.querySelector('script, b')).toBeNull()
    expect(c.textContent).toContain('<b>raw</b>')
  })
})
