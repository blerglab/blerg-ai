import { printContent } from '../model/contentExport'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ViewerToolbar, { ViewerAction } from './ViewerToolbar'

vi.mock('../model/contentExport', () => ({ printContent: vi.fn(async () => {}) }))
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks() })
it('copies the full path from its truncated title or icon and labels icon actions', async () => {
  const writeText = vi.fn(async () => {})
  vi.stubGlobal('navigator', { clipboard: { writeText } })
  const download = vi.fn()
  render(<ViewerToolbar titleId="path" title="src/deep/main.go" meta="From the session’s folder · as of now · 27 KB">
    <ViewerAction icon="download" label="Download" onClick={download} />
  </ViewerToolbar>)
  fireEvent.click(screen.getByRole('button', { name: 'Copy src/deep/main.go path' }))
  expect(writeText).toHaveBeenLastCalledWith('src/deep/main.go')
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Copied'))
  fireEvent.click(screen.getByRole('button', { name: 'Copy path' }))
  expect(writeText).toHaveBeenCalledTimes(2)
  fireEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(download).toHaveBeenCalledOnce()
})

it('keeps a tab’s repeated filename accessible without drawing another heading', () => {
  render(<ViewerToolbar titleId="file" title="notes.md" meta="Markdown" showPath={false} />)
  expect(screen.queryByRole('heading', { name: 'notes.md' })).toBeNull()
  expect(document.getElementById('file')).toHaveTextContent('notes.md')
  expect(screen.getByRole('button', { name: 'Copy path' })).toBeVisible()
})

it('makes print available on every shared viewer toolbar', () => {
  render(<ViewerToolbar titleId="print" title="Report" meta="Text" />)
  expect(screen.getByRole('button', { name: 'Print' })).toHaveAttribute('title', 'Print')
})

it('prints the visible comparison rather than the hidden document behind it', () => {
  render(<div className="artifact-viewer">
    <ViewerToolbar titleId="compare" title="Report" meta="Comparison" />
    <section className="artifact-comparison is-active">Current comparison</section>
    <div className="artifact-body" hidden>Original document</div>
  </div>)
  fireEvent.click(screen.getByRole('button', { name: 'Print' }))
  expect(printContent).toHaveBeenCalledWith(document.querySelector('.artifact-comparison.is-active'))
})
