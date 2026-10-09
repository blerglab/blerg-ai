import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import FilesPanel from './FilesPanel'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'

const items: ArtifactInfo[] = [
  { id: 'new1', name: 'report.md', size: 2048, content_type: 'text/markdown', view: 'markdown', created_at: '2026-09-30T12:00:00Z' },
  { id: 'old1', name: 'memo.docx', size: 245760, content_type: 'application/x', view: 'none', created_at: '2026-09-30T11:00:00Z' },
]

let files: FakeFiles

function setup(list: ArtifactInfo[] = items, status: 'loading' | 'ready' | 'error' = 'ready') {
  const p = { onClose: vi.fn(), onView: vi.fn(), onChanged: vi.fn(), onAttach: vi.fn() }
  render(<FilesPanel sessionId="s1" files={files} items={list} status={status} {...p} />)
  return p
}

beforeEach(() => {
  files = createFakeFiles()
})

describe('FilesPanel', () => {
  it('lists the files in the order given (newest first) with size and kind', () => {
    setup()
    const rows = screen.getAllByTestId('artifact-row')
    expect(rows.map(r => within(r).getByTestId('artifact-name').textContent)).toEqual(['report.md', 'memo.docx'])
    expect(rows[0]).toHaveTextContent('2.0 KB')
    expect(rows[1]).toHaveTextContent('240 KB')
    expect(screen.getByRole('dialog', { name: /Files/ })).toBeInTheDocument()
  })

  it('puts files you uploaded in their own section, marked as yours', () => {
    const mine: ArtifactInfo = { id: 'up1', name: 'brief.pdf', size: 1258291, content_type: 'application/pdf', view: 'pdf', origin: 'user', created_at: '2026-09-30T12:30:00Z' }
    setup([mine, ...items])
    const uploaded = screen.getByRole('list', { name: 'Uploaded by you' })
    expect(within(uploaded).getByTestId('artifact-name')).toHaveTextContent('brief.pdf')
    expect(uploaded).toHaveTextContent('uploaded by you')
    const agent = screen.getByRole('list', { name: 'Published by the agent' })
    expect(within(agent).getAllByTestId('artifact-row')).toHaveLength(2)
    expect(agent).not.toHaveTextContent('uploaded by you')
  })

  it('deletes an uploaded file through the same operation', async () => {
    const mine: ArtifactInfo = { id: 'up1', name: 'brief.pdf', size: 10, content_type: 'application/pdf', view: 'pdf', origin: 'user' }
    const p = setup([mine])
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, delete' }))
    await waitFor(() => expect(p.onChanged).toHaveBeenCalled())
    expect(files.removed).toEqual(['up1'])
  })

  it('offers View only for a file the app can show', () => {
    setup()
    const [md, docx] = screen.getAllByTestId('artifact-row')
    expect(within(md).getByRole('button', { name: 'View' })).toBeInTheDocument()
    expect(within(docx).queryByRole('button', { name: 'View' })).toBeNull()
    expect(within(docx).getByRole('button', { name: 'Download' })).toBeInTheDocument()
  })

  it('opens a file in the viewer', () => {
    const p = setup()
    fireEvent.click(within(screen.getAllByTestId('artifact-row')[0]).getByRole('button', { name: 'View' }))
    expect(p.onView).toHaveBeenCalledWith(items[0])
  })

  it('has an empty state that says how files get here', () => {
    setup([])
    const empty = screen.getByTestId('artifacts-empty')
    expect(empty).toHaveTextContent('No files yet')
    expect(empty).toHaveTextContent('blerg-runner publish')
    expect(screen.queryAllByTestId('artifact-row')).toHaveLength(0)
  })

  it('says how the agent publishes in the host’s own words', () => {
    render(<FilesPanel sessionId="s1" files={files} items={[]} status="ready" onClose={vi.fn()} onView={vi.fn()} onChanged={vi.fn()} publishHint="acme share <file>" />)
    expect(screen.getByTestId('artifacts-empty')).toHaveTextContent('acme share <file>')
    expect(screen.getByTestId('artifacts-empty')).not.toHaveTextContent('blerg-runner publish')
  })

  it('offers to attach files from the empty state, in a compact panel', () => {
    const p = setup([])
    expect(screen.getByTestId('artifacts-panel').classList.contains('is-empty')).toBe(true)
    expect(screen.getByTestId('artifacts-empty')).toHaveTextContent('Files you attach')
    fireEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    expect(p.onAttach).toHaveBeenCalledTimes(1)
  })

  it('offers no Attach files when the host gives no way to attach', () => {
    render(<FilesPanel sessionId="s1" files={files} items={[]} status="ready" onClose={vi.fn()} onView={vi.fn()} onChanged={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'Attach files' })).toBeNull()
  })

  it('shows the count in the header and is full width with files', () => {
    setup()
    expect(screen.getByRole('dialog', { name: /Files/ })).toHaveTextContent('2')
    expect(screen.getByTestId('artifacts-panel').classList.contains('is-empty')).toBe(false)
    expect(screen.queryByRole('button', { name: 'Attach files' })).toBeNull()
  })

  it('says when it is loading', () => {
    setup([], 'loading')
    expect(screen.getByTestId('artifacts-loading')).toBeInTheDocument()
  })

  it('says when the list could not be read', () => {
    setup([], 'error')
    expect(screen.getByRole('alert')).toHaveTextContent(/Couldn.t load the files/)
  })

  it('downloads through the transport', async () => {
    setup()
    fireEvent.click(within(screen.getAllByTestId('artifact-row')[1]).getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(files.downloads).toEqual(['old1']))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('reports a download that fails on its row', async () => {
    files.downloadError = new Error('This file is no longer available.')
    setup()
    const row = screen.getAllByTestId('artifact-row')[1]
    fireEvent.click(within(row).getByRole('button', { name: 'Download' }))
    expect(await within(row).findByRole('alert')).toHaveTextContent('no longer available')
  })

  it('asks before deleting, in the page, and deletes nothing on Cancel', () => {
    setup()
    const row = screen.getAllByTestId('artifact-row')[0]
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
    expect(within(row).getByText(/Delete this file\?/)).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Cancel' }))
    expect(within(row).queryByText(/Delete this file\?/)).toBeNull()
    expect(files.removed).toHaveLength(0)
  })

  it('deletes after the confirmation and reloads the list', async () => {
    const p = setup()
    const row = screen.getAllByTestId('artifact-row')[0]
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Yes, delete' }))
    await waitFor(() => expect(p.onChanged).toHaveBeenCalled())
    expect(files.removed).toEqual(['new1'])
  })

  it('keeps the file and says so when the delete fails', async () => {
    files.removeError = new Error('HTTP 500')
    const p = setup()
    const row = screen.getAllByTestId('artifact-row')[0]
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Yes, delete' }))
    expect(await within(row).findByRole('alert')).toHaveTextContent(/Couldn.t delete/)
    expect(p.onChanged).not.toHaveBeenCalled()
  })

  it('closes on Escape', () => {
    const p = setup()
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(p.onClose).toHaveBeenCalled()
  })
})

// ─── versions ─────────────────────────────────────────────────────────────────
describe('FilesPanel: versions', () => {
  const v = (id: string, name: string, version: number, over: Partial<ArtifactInfo> = {}): ArtifactInfo => ({
    id, name, size: 1024 * version, content_type: 'text/markdown', view: 'markdown', version, latest_version: 3,
    created_at: `2026-09-30T1${version}:00:00Z`, ...over,
  })
  const report3 = [v('r3', 'report.md', 3), v('r2', 'report.md', 2), v('r1', 'report.md', 1)]
  const memo: ArtifactInfo = { id: 'm1', name: 'memo.docx', size: 10, content_type: 'application/x', view: 'none', version: 1, latest_version: 1, created_at: '2026-09-30T09:00:00Z' }

  it('shows ONE row per file: the latest version, with a v-badge', () => {
    setup([...report3, memo])
    const rows = screen.getAllByTestId('artifact-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByTestId('artifact-name')).toHaveTextContent('report.md')
    expect(within(rows[0]).getByTestId('artifact-version')).toHaveTextContent('v3')
    expect(rows[0]).toHaveTextContent('3.0 KB') // the latest version's own size
    expect(screen.getByRole('dialog', { name: /Files/ })).toHaveTextContent('2') // two files, not four versions
  })

  it('leaves out the badge for a file with a single version 1, and shows it for a lone later one', () => {
    setup([v('m1', 'memo.docx', 1, { latest_version: 1, view: 'none' }), v('s2', 'solo.md', 2, { latest_version: 2 })])
    const [first, second] = screen.getAllByTestId('artifact-row')
    expect(within(first).queryByTestId('artifact-version')).toBeNull()
    expect(within(first).queryByRole('button', { name: /Earlier versions/ })).toBeNull()
    expect(within(second).getByTestId('artifact-version')).toHaveTextContent('v2')
    expect(within(second).queryByRole('button', { name: /Earlier versions/ })).toBeNull()
  })

  it('keeps earlier versions behind an Earlier versions (N) disclosure', () => {
    setup(report3)
    const row = screen.getByTestId('artifact-row')
    const toggle = within(row).getByRole('button', { name: 'Earlier versions (2)' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(within(row).queryAllByTestId('artifact-version-row')).toHaveLength(0)
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const versions = within(row).getAllByTestId('artifact-version-row')
    expect(versions.map(r => within(r).getByTestId('artifact-version').textContent)).toEqual(['v2', 'v1'])
    expect(versions[0]).toHaveTextContent('2.0 KB')
    expect(versions[1]).toHaveTextContent('1.0 KB')
    expect(versions[0].querySelector('time')).not.toBeNull() // its own time
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(within(row).queryAllByTestId('artifact-version-row')).toHaveLength(0)
  })

  it('an earlier version has its own Download, View and Delete', async () => {
    const p = setup(report3)
    fireEvent.click(screen.getByRole('button', { name: 'Earlier versions (2)' }))
    const [v2, v1] = screen.getAllByTestId('artifact-version-row')
    fireEvent.click(within(v1).getByRole('button', { name: 'View' }))
    expect(p.onView).toHaveBeenCalledWith(report3[2])
    fireEvent.click(within(v2).getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(files.downloads).toEqual(['r2']))
  })

  it('the latest version downloads by its own id', async () => {
    setup(report3)
    fireEvent.click(within(screen.getByTestId('artifact-row')).getAllByRole('button', { name: 'Download' })[0])
    await waitFor(() => expect(files.downloads).toEqual(['r3']))
  })

  it('deleting an earlier version deletes that single version, after a confirmation', async () => {
    const p = setup(report3)
    fireEvent.click(screen.getByRole('button', { name: 'Earlier versions (2)' }))
    const v1 = screen.getAllByTestId('artifact-version-row')[1]
    fireEvent.click(within(v1).getByRole('button', { name: 'Delete' }))
    expect(within(v1).getByText('Delete v1?')).toBeInTheDocument()
    expect(files.removed).toHaveLength(0)
    fireEvent.click(within(v1).getByRole('button', { name: 'Yes, delete' }))
    await waitFor(() => expect(p.onChanged).toHaveBeenCalled())
    expect(files.removed).toEqual(['r1'])
  })

  it('deleting the latest version deletes only it, and the previous one is then shown as the latest', async () => {
    const p = { onClose: vi.fn(), onView: vi.fn(), onChanged: vi.fn(), onAttach: vi.fn() }
    const { rerender } = render(<FilesPanel sessionId="s1" files={files} items={report3} status="ready" {...p} />)
    const row = screen.getByTestId('artifact-row')
    // The latest row's own Delete is the first one (the earlier versions are collapsed).
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
    expect(within(row).getByText('Delete v3?')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Yes, delete' }))
    await waitFor(() => expect(p.onChanged).toHaveBeenCalled())
    expect(files.removed).toEqual(['r3'])
    // The refreshed list no longer has v3: v2 is now the file's row.
    rerender(<FilesPanel sessionId="s1" files={files} items={[report3[1], report3[2]].map(a => ({ ...a, latest_version: 2 }))} status="ready" {...p} />)
    const after = screen.getByTestId('artifact-row')
    expect(within(after).getByTestId('artifact-version')).toHaveTextContent('v2')
    expect(after).toHaveTextContent('2.0 KB')
    expect(within(after).getByRole('button', { name: 'Earlier versions (1)' })).toBeInTheDocument()
  })

  it('a file with only one version left has no disclosure and no badge at v1', () => {
    setup([{ ...report3[2], latest_version: 1 }])
    const row = screen.getByTestId('artifact-row')
    expect(within(row).queryByRole('button', { name: /Earlier versions/ })).toBeNull()
    expect(within(row).queryByTestId('artifact-version')).toBeNull()
  })

  it('groups the Uploaded by you section the same way, apart from the agent file of the same name', () => {
    const mine = [
      v('u2', 'report.md', 2, { origin: 'user', latest_version: 2 }),
      v('u1', 'report.md', 1, { origin: 'user', latest_version: 2 }),
    ]
    setup([...mine, ...report3])
    const uploaded = screen.getByRole('list', { name: 'Uploaded by you' })
    const up = within(uploaded).getByTestId('artifact-row')
    expect(within(up).getByTestId('artifact-version')).toHaveTextContent('v2')
    expect(within(up).getByRole('button', { name: 'Earlier versions (1)' })).toBeInTheDocument()
    const agent = screen.getByRole('list', { name: 'Published by the agent' })
    const ag = within(agent).getByTestId('artifact-row')
    expect(within(ag).getByTestId('artifact-version')).toHaveTextContent('v3')
    expect(within(ag).getByRole('button', { name: 'Earlier versions (2)' })).toBeInTheDocument()
  })

  it('files without a version (an older server) are listed as they always were', () => {
    setup([{ id: 'p', name: 'a.txt', size: 1, content_type: 'text/plain', view: 'text' }, { id: 'q', name: 'a.txt', size: 2, content_type: 'text/plain', view: 'text' }])
    expect(screen.getAllByTestId('artifact-row')).toHaveLength(2)
    expect(screen.queryByTestId('artifact-version')).toBeNull()
  })
})
