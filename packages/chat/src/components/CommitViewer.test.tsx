// A commit chip with no published diff opens the commit's diff, generated on demand from the
// session's workspace, in Blerg's own diff viewer (card #131).
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import * as patchModel from '../model/patch'
import { CommitChips } from './CommitChips'
import { CommitViewerHost } from './CommitViewer'
import type { CommitDiff, TransportFiles } from '../transport/types'

const SHA = 'a'.repeat(40)
const PATCH = [
  `commit ${SHA}`, 'Author: Pat Example <pat@example.com>', 'Date:   Wed, 21 Oct 2026 10:00:00 +0000', '', '    Read commits on demand', '',
  'diff --git a/lookup.go b/lookup.go', '--- a/lookup.go', '+++ b/lookup.go', '@@ -1 +1,2 @@', '-old', '+new', '+more', '',
].join('\n')

function filesWith(diff: (s: string, sha: string) => Promise<CommitDiff>): TransportFiles & { commitDiff: ReturnType<typeof vi.fn> } {
  return {
    list: vi.fn(), raw: vi.fn(), download: vi.fn(), remove: vi.fn(), upload: vi.fn(),
    commit: vi.fn(async () => null), commitDiff: vi.fn(diff),
  } as unknown as TransportFiles & { commitDiff: ReturnType<typeof vi.fn> }
}

async function settle() {
  await act(async () => { await Promise.resolve(); await new Promise(r => setTimeout(r, 0)) })
}

beforeEach(() => localStorage.clear())

describe('CommitViewer: a commit chip with no published diff', () => {
  it('opens the commit\'s diff, read through the session, in the diff viewer', async () => {
    const files = filesWith(async () => ({ text: PATCH }))
    render(<CommitViewerHost files={files} repo={() => 'repos/blerg'}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>)
    fireEvent.click(screen.getByRole('button', { name: 'Open commit aaaaaaa in the diff' }))
    expect(screen.getByRole('dialog', { name: 'Commit aaaaaaa' })).toBeInTheDocument()
    expect(screen.getByTestId('commit-loading')).toBeInTheDocument()
    await settle()
    expect(files.commitDiff).toHaveBeenCalledWith('s1', SHA, expect.any(AbortSignal))
    const dialog = screen.getByRole('dialog', { name: 'Commit aaaaaaa' })
    expect(dialog).toHaveTextContent('lookup.go')
    expect(dialog).toHaveTextContent('From the session’s workspace · blerg')
    expect(screen.queryByTestId('commit-none')).toBeNull()
    // Nothing links out of Blerg.
    expect(dialog.querySelector('a[href^="http"]')).toBeNull()
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('hidden characters in the commit\'s header and message are shown, not obeyed; the code is left as it is', async () => {
    const tricky = PATCH.replace('Pat Example', 'Pat\u202E Example').replace('Read commits on demand', 'Read\u2028 commits\u{E0041} on demand').replace('+new', '+new\u202E')
    const files = filesWith(async () => ({ text: tricky }))
    render(<CommitViewerHost files={files} repo={() => 'repo\u202Ename'}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>)
    fireEvent.click(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    await settle()
    const dialog = screen.getByRole('dialog', { name: 'Commit aaaaaaa' })
    expect(dialog.querySelector('.artifact-meta')!.textContent).toContain('repo\\u{202E}name')
    const text = dialog.textContent ?? ''
    expect(text).toContain('Read\\u{2028} commits\\u{E0041} on demand')
    expect(text.includes('Read\u2028') || text.includes('Pat\u202E')).toBe(false)
  })

  it('a diff over the cap: says it is too large, draws nothing', async () => {
    const files = filesWith(async () => 'large')
    render(<CommitViewerHost files={files}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>)
    fireEvent.click(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    await settle()
    expect(screen.getByTestId('commit-large')).toHaveTextContent('too large')
  })

  it('a commit that cannot be read: one short neutral note, no error text', async () => {
    for (const read of [async () => null, async () => { throw new TypeError('Failed to fetch') }]) {
      const files = filesWith(read)
      const { unmount } = render(<CommitViewerHost files={files}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>)
      fireEvent.click(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
      await settle()
      expect(screen.getByTestId('commit-none').textContent).toBe('This commit can’t be read from the session’s workspace right now.')
      unmount()
    }
  })

  it('the host opens a published diff that holds the commit instead', async () => {
    const files = filesWith(async () => ({ text: PATCH }))
    const opened = vi.fn(() => true)
    render(<CommitViewerHost files={files} opened={opened}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>)
    fireEvent.click(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    await settle()
    expect(opened).toHaveBeenCalledWith('s1', SHA)
    expect(files.commitDiff).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('a chip with its own diff still opens that one; without a host (or the read) a chip copies its sha', async () => {
    const files = filesWith(async () => ({ text: PATCH }))
    const onOpen = vi.fn()
    const writeText = vi.fn(async () => {})
    Object.assign(navigator, { clipboard: { writeText } })
    const { unmount } = render(<CommitViewerHost files={files}><CommitChips commits={[{ sha: SHA }]} onOpen={onOpen} hover={{ sessionId: 's1', diff: 'd1' }} /></CommitViewerHost>)
    fireEvent.click(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    expect(onOpen).toHaveBeenCalledWith(SHA)
    expect(files.commitDiff).not.toHaveBeenCalled()
    unmount()
    const noRead = { ...files, commitDiff: undefined }
    render(<>
      <CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} />
      <CommitViewerHost files={noRead}><CommitChips commits={[{ sha: 'b'.repeat(40) }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>
    </>)
    fireEvent.click(screen.getByRole('button', { name: `Copy commit ${SHA}` }))
    fireEvent.click(screen.getByRole('button', { name: `Copy commit ${'b'.repeat(40)}` }))
    expect(writeText).toHaveBeenCalledTimes(2)
  })
})

it('carries a chip member and launch to its on-demand diff reader', async () => {
  const member = { member_id: 'second', launch_generation: 'launch', generation: 7 }
  const files = filesWith(async () => ({ text: PATCH }))
  render(<CommitViewerHost files={files}><CommitChips commits={[{ sha: SHA, member }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>)
  fireEvent.click(screen.getByRole('button', { name: `Open commit ${SHA.slice(0, 7)} in the diff` }))
  await waitFor(() => expect(files.commitDiff).toHaveBeenCalledWith('s1', SHA, expect.any(AbortSignal), member))
})

describe('large commit rendering (#215)', () => {
  const patch = (files: number, lines: number) => Array.from({ length: files }, (_, i) => [
    `diff --git a/file${i}.txt b/file${i}.txt`, `--- a/file${i}.txt`, `+++ b/file${i}.txt`, `@@ -0,0 +1,${lines} @@`,
    ...Array.from({ length: lines }, (_, n) => `+line ${n}`), '',
  ].join('\n')).join('')
  it.each([[60, 1], [1, 401]])('starts %i files with %i changed lines collapsed', async (count, lines) => {
    const buildRows = vi.spyOn(patchModel, 'foldContext')
    const files = filesWith(async () => ({ text: patch(count, lines) }))
    render(<CommitViewerHost files={files}><CommitChips commits={[{ sha: SHA }]} hover={{ sessionId: 's1' }} /></CommitViewerHost>)
    fireEvent.click(screen.getByRole('button', { name: /Open commit aaaaaaa/ }))
    await screen.findByTestId('diff-view')
    const built = buildRows.mock.calls.length
    buildRows.mockRestore()
    expect(built).toBe(0) // Collapsed bodies must skip row construction as well as DOM mounting.
    expect(document.querySelectorAll('.diff-file-body')).toHaveLength(0)
    fireEvent.click(document.querySelector('.diff-file-toggle')!)
    expect(document.querySelectorAll('.diff-file-body')).toHaveLength(1)
    expect(document.querySelectorAll('.diff-row').length).toBeLessThanOrEqual(400)
    if (lines > 400) {
      fireEvent.click(screen.getByRole('button', { name: 'Show full file' }))
      expect(document.querySelectorAll('.diff-row')).toHaveLength(lines)
    }
    fireEvent.click(document.querySelector('.diff-file-toggle')!)
    expect(document.querySelectorAll('.diff-row')).toHaveLength(0)
  })
})
