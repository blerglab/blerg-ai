import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import FilesList from './FilesList'
import { createFakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'

const file = (i: number, over: Partial<ArtifactInfo> = {}): ArtifactInfo => ({
  id: `f${i}`, name: `file-${String(i).padStart(2, '0')}.md`, size: 1024, content_type: 'text/markdown', view: 'markdown',
  created_at: `2026-09-30T${String(23 - i).padStart(2, '0')}:00:00Z`, ...over,
})
const many = (n: number) => Array.from({ length: n }, (_, i) => file(i))

function setup(items: ArtifactInfo[]) {
  const p = { onView: vi.fn(), onChanged: vi.fn() }
  render(<FilesList sessionId="s1" files={createFakeFiles()} items={items} status="ready" {...p} />)
  return p
}
const names = () => screen.getAllByTestId('artifact-name').map(n => n.textContent)

beforeEach(() => localStorage.clear())

describe('FilesList rows', () => {
  it('detects the language from the name even when the viewer is plain text', () => {
    setup([file(0, { name: 'main.go', view: 'text' })])
    expect(screen.getByTestId('artifact-row').querySelector('svg')).toHaveAttribute('data-file-icon', 'go')
  })

  it('draws a row with a glyph, the name, and size · version · time · you', () => {
    setup([file(0, { name: 'brief.pdf', view: 'pdf', size: 2048, version: 3, latest_version: 3, origin: 'user' })])
    const row = screen.getByTestId('artifact-row')
    expect(row.querySelector('svg[data-file-icon=pdf] title')).toHaveTextContent('PDF')
    expect(within(row).getByRole('button', { name: 'View brief.pdf' })).toHaveAttribute('title', 'brief.pdf')
    const meta = row.querySelector('.artifact-row-meta')!
    expect(meta).toHaveTextContent(/^2\.0 KB · v3 · .+ · you$/)
    expect(meta.querySelector('time')).toHaveAttribute('title')
  })

  it('has Download and Delete as labeled buttons a keyboard reaches, and no text button for View', () => {
    setup([file(0)])
    const row = screen.getByTestId('artifact-row')
    for (const name of ['Download', 'Delete']) {
      const b = within(row).getByRole('button', { name })
      expect(b.tabIndex).toBe(0)
    }
  })

  it('confirms a delete in the row', () => {
    setup([file(0)])
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(screen.getByText('Delete this file?')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Yes, delete' })).toBeInTheDocument()
  })
})

describe('FilesList search, sort and origin', () => {
  it('shows no tools for six files or fewer', () => {
    setup(many(6))
    expect(screen.queryByRole('searchbox')).toBeNull()
  })

  it('filters by name (case-insensitive substring), counts "4 of 21", says when nothing matches, and clears on Esc', () => {
    const items = many(21).map((f, i) => (i < 4 ? { ...f, name: `Report-${i}.md` } : f))
    setup(items)
    const box = screen.getByRole('searchbox', { name: 'Filter files' })
    expect(screen.queryByTestId('artifacts-shown')).toBeNull()
    fireEvent.change(box, { target: { value: 'rePORT' } })
    expect(names()).toEqual(['Report-0.md', 'Report-1.md', 'Report-2.md', 'Report-3.md'])
    expect(screen.getByTestId('artifacts-shown')).toHaveTextContent('4 of 21')
    fireEvent.change(box, { target: { value: 'zzz' } })
    expect(screen.getByTestId('artifacts-no-match')).toHaveTextContent('No files match')
    fireEvent.keyDown(box, { key: 'Escape' })
    expect(box).toHaveValue('')
    expect(screen.getAllByTestId('artifact-row')).toHaveLength(21)
  })

  it('sorts by name, and remembers the choice', () => {
    const items = many(8).map((f, i) => ({ ...f, name: `${'hgfedcba'[i]}.md` }))
    setup(items)
    expect(names()[0]).toBe('h.md') // newest first, as given
    fireEvent.change(screen.getByRole('combobox', { name: 'Sort files' }), { target: { value: 'name' } })
    expect(names()).toEqual(['a.md', 'b.md', 'c.md', 'd.md', 'e.md', 'f.md', 'g.md', 'h.md'])
    expect(localStorage.getItem('blerg.files.sort')).toBe('name')
  })

  it('starts from the remembered sort and origin', () => {
    localStorage.setItem('blerg.files.origin', 'user')
    setup(many(8).map((f, i) => (i < 2 ? { ...f, origin: 'user' as const } : f)))
    expect(screen.getAllByTestId('artifact-row')).toHaveLength(2)
    expect(screen.getByTestId('artifacts-shown')).toHaveTextContent('2 of 8')
  })

  it('filters by origin', () => {
    setup(many(8).map((f, i) => (i < 3 ? { ...f, origin: 'user' as const } : f)))
    const sel = screen.getByRole('combobox', { name: 'Show files from' })
    fireEvent.change(sel, { target: { value: 'agent' } })
    expect(screen.getAllByTestId('artifact-row')).toHaveLength(5)
    fireEvent.change(sel, { target: { value: 'user' } })
    expect(screen.getAllByTestId('artifact-row')).toHaveLength(3)
    fireEvent.change(sel, { target: { value: 'all' } })
    expect(screen.getAllByTestId('artifact-row')).toHaveLength(8)
  })
})
