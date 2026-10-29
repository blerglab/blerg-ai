// #281: a session's Files list and the contents of its open file, artifact and commit tabs are kept
// in memory, so coming back draws them at once with no request before the first paint; a workspace
// file is read again behind them and replaced only if it changed. Bounded by bytes; one sign-in's.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, render, renderHook, screen, waitFor } from '@testing-library/react'
import { VIEW_CACHE_BYTES, VIEW_CACHE_ENTRY_BYTES, VIEW_CACHE_LISTS, clearViewCache, contentKeeper, forgetViewCacheSession, listKeeper, peekContent, peekList, viewCacheBytes } from './viewCache'
import { useArtifacts } from '../hooks/useArtifacts'
import WorkspaceFileViewer from '../components/WorkspaceFileViewer'
import ArtifactViewer from '../components/ArtifactViewer'
import CommitViewer from '../components/CommitViewer'
import type { ArtifactInfo, TransportFiles, WorkspaceFile } from '../transport/types'

afterEach(() => { clearViewCache(); vi.restoreAllMocks() })

const settle = () => act(async () => { for (let i = 0; i < 5; i++) await new Promise(r => setTimeout(r, 0)) })

describe('viewCache', () => {
  it('keeps contents within the byte bound, least recently viewed out first, and skips a very large file', () => {
    const files = {}
    const size = VIEW_CACHE_ENTRY_BYTES - 1 // four fit in VIEW_CACHE_BYTES, a fifth does not
    for (const id of ['a', 'b', 'c', 'd']) contentKeeper(files, 's', id)(id, size)
    expect(peekContent(files, 's', 'a')).toBe('a') // a is now the most recently viewed
    contentKeeper(files, 's', 'e')('e', size)
    expect(viewCacheBytes()).toBeLessThanOrEqual(VIEW_CACHE_BYTES)
    expect(peekContent(files, 's', 'b')).toBeUndefined()
    expect(peekContent(files, 's', 'a')).toBe('a')
    expect(peekContent(files, 's', 'e')).toBe('e')
    contentKeeper(files, 's', 'huge')('huge', VIEW_CACHE_ENTRY_BYTES + 1)
    expect(peekContent(files, 's', 'huge')).toBeUndefined()
  })

  it('keeps the last Files lists by session', () => {
    const files = {}
    for (let i = 0; i <= VIEW_CACHE_LISTS; i++) listKeeper(files, `s${i}`)([i])
    expect(peekList(files, 's0')).toBeUndefined()
    expect(peekList(files, `s${VIEW_CACHE_LISTS}`)).toEqual([VIEW_CACHE_LISTS])
  })

  it('never shows what one sign-in read to another, nor a read that began before a clear', () => {
    const before = {}, after = {}
    contentKeeper(before, 's', 'f')('secret', 10)
    // Another account or authority: the host makes a new transport (another files object).
    expect(peekContent(after, 's', 'f')).toBeUndefined()
    const late = contentKeeper(before, 's', 'g')
    clearViewCache()
    late('late', 10)
    expect(peekContent(before, 's', 'g')).toBeUndefined()
    expect(peekContent(before, 's', 'f')).toBeUndefined()
  })

  it('a read that began before its session was forgotten keeps nothing, and an ended session keeps nothing more (review 4)', () => {
    const files = {}
    const content = contentKeeper(files, 's', 'f'), list = listKeeper(files, 's')
    forgetViewCacheSession('s')
    content('late private bytes', 18); list(['late-file'])
    expect(peekContent(files, 's', 'f')).toBeUndefined()
    expect(peekList(files, 's')).toBeUndefined()
    expect(viewCacheBytes()).toBe(0)
    // Evicted, not ended: a later visit keeps again.
    contentKeeper(files, 's', 'f')('again', 5)
    expect(peekContent(files, 's', 'f')).toBe('again')
    forgetViewCacheSession('s', { ended: true })
    contentKeeper(files, 's', 'f')('after end', 9); listKeeper(files, 's')(['after end'])
    expect(peekContent(files, 's', 'f')).toBeUndefined()
    expect(peekList(files, 's')).toBeUndefined()
  })

  it('an older read never replaces what a newer read kept (review 5)', () => {
    const files = {}
    const oldList = listKeeper(files, 's'), newList = listKeeper(files, 's')
    newList(['new']); oldList(['old'])
    expect(peekList(files, 's')).toEqual(['new'])
    const oldBody = contentKeeper(files, 's', 'f'), newBody = contentKeeper(files, 's', 'f')
    newBody('new', 3); oldBody('old', 3)
    expect(peekContent(files, 's', 'f')).toBe('new')
  })

  it('forgets one session', () => {
    const files = {}
    contentKeeper(files, 's1', 'f')(1, 10)
    contentKeeper(files, 's2', 'f')(2, 10)
    listKeeper(files, 's1')([1])
    forgetViewCacheSession('s1')
    expect(peekContent(files, 's1', 'f')).toBeUndefined()
    expect(peekList(files, 's1')).toBeUndefined()
    expect(peekContent(files, 's2', 'f')).toBe(2)
  })
})

const art = (over: Partial<ArtifactInfo> = {}): ArtifactInfo => ({ id: 'a1', name: 'notes.md', size: 7, content_type: 'text/markdown', view: 'markdown', version: 1, ...over })
function wsFile(text: string, mtime = '2026-10-27T10:00:00Z'): WorkspaceFile {
  return { path: 'docs/notes.md', size: text.length, view: 'text', content_type: 'text/plain', mtime, blob: new Blob([text]) }
}
function transport(over: Partial<TransportFiles> = {}) {
  return { list: vi.fn(async () => [art()]), raw: vi.fn(async () => new Blob(['kept body'])), download: vi.fn(), remove: vi.fn(), upload: vi.fn(), ...over } as unknown as TransportFiles & Record<string, ReturnType<typeof vi.fn>>
}

describe('a return draws the open tabs and Files at once', () => {
  it('the Files list is ready in the first render, and read again behind it', async () => {
    const files = transport()
    const first = renderHook(() => useArtifacts('s', files, []))
    await waitFor(() => expect(first.result.current.status).toBe('ready'))
    first.unmount()
    const second = renderHook(() => useArtifacts('s', files, []))
    expect(second.result.current.status).toBe('ready')
    expect(second.result.current.items.map(a => a.id)).toEqual(['a1'])
    await settle()
    expect(files.list).toHaveBeenCalledTimes(2)
  })

  it('a workspace file shows its kept text with no request before the paint; unchanged, nothing is replaced', async () => {
    let current = wsFile('first text')
    const workspaceFile = vi.fn(async () => current)
    const files = transport({ workspaceFile })
    const first = render(<WorkspaceFileViewer sessionId="s" files={files} path="docs/notes.md" onClose={() => {}} embedded />)
    await screen.findByText('first text')
    first.unmount()
    workspaceFile.mockClear()
    const second = render(<WorkspaceFileViewer sessionId="s" files={files} path="docs/notes.md" onClose={() => {}} embedded />)
    // First paint, before any effect: the kept contents, no loading line.
    expect(screen.getByText('first text')).toBeInTheDocument()
    expect(second.container.textContent).not.toMatch(/Opening|Loading|Reading/)
    const node = screen.getByText('first text')
    await settle()
    expect(workspaceFile).toHaveBeenCalledTimes(1)
    expect(screen.getByText('first text')).toBe(node)
    second.unmount()
    // Changed on disk: the background read replaces the contents in place.
    current = wsFile('second text', '2026-10-27T11:00:00Z')
    render(<WorkspaceFileViewer sessionId="s" files={files} path="docs/notes.md" onClose={() => {}} embedded />)
    expect(screen.getByText('first text')).toBeInTheDocument()
    expect(await screen.findByText('second text')).toBeInTheDocument()
  })

  it('an artifact version and a commit diff are shown from memory and not read again', async () => {
    const files = transport({ commitDiff: vi.fn(async () => ({ text: 'diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n' })) } as Partial<TransportFiles>)
    const a = render(<ArtifactViewer sessionId="s" files={files} artifact={art()} onClose={() => {}} embedded />)
    await screen.findByText('kept body')
    const c = render(<CommitViewer sessionId="s" files={files} sha={'b'.repeat(40)} onClose={() => {}} embedded />)
    await waitFor(() => expect(screen.queryByTestId('commit-loading')).toBeNull())
    a.unmount(); c.unmount()
    render(<ArtifactViewer sessionId="s" files={files} artifact={art()} onClose={() => {}} embedded />)
    render(<CommitViewer sessionId="s" files={files} sha={'b'.repeat(40)} onClose={() => {}} embedded />)
    expect(screen.getByText('kept body')).toBeInTheDocument()
    expect(screen.queryByTestId('commit-loading')).toBeNull()
    await settle()
    expect(files.raw).toHaveBeenCalledTimes(1)
    expect(files.commitDiff).toHaveBeenCalledTimes(1)
  })
})
