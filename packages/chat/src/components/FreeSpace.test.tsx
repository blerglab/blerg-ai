import {fireEvent, render, screen, waitFor, within} from '@testing-library/react'
import {expect, it, vi} from 'vitest'
import {FreeSpaceButton} from './FreeSpace'
import type {ArtifactInfo, TransportFiles} from '../transport/types'
import {FileRequestError} from '../transport/files'

it('retains the dialog and reports a partial deletion failure, then retries remaining files', async () => {
  let artifacts: ArtifactInfo[] = ['a', 'b'].map((id, i) => ({id, name: `${id}.txt`, size: 3, origin: 'user', content_type: 'text/plain', view: 'text', created_at: `2026-10-0${i+1}`}))
  const remove = vi.fn(async (_sid: string, id: string) => {
    if (id === 'b' && artifacts.some(a => a.id === 'a')) throw new Error('unexpected order')
    artifacts = artifacts.filter(a => a.id !== id)
  })
  remove.mockImplementationOnce(async (_sid, id) => {artifacts = artifacts.filter(a => a.id !== id)})
  remove.mockRejectedValueOnce(new FileRequestError('Deletion temporarily unavailable.', 503))
  const files: TransportFiles = {list: async () => artifacts, space: async () => ({artifacts, limits: {max_session_upload_bytes: 6}, can_delete: true}), remove, upload: async () => {throw new Error('unused')}, raw: async () => new Blob(), download: async () => {}}
  const changed = vi.fn()
  render(<FreeSpaceButton sessionId="s" files={files} waitingBytes={4} onChanged={changed} />)
  fireEvent.click(screen.getByRole('button', {name: 'Free up space…'}))
  const dialog = screen.getByRole('dialog', {name: 'Free up space'})
  await waitFor(() => expect(within(dialog).getAllByRole('checkbox')).toHaveLength(2))
  fireEvent.click(within(dialog).getByRole('button', {name: 'Delete selected files'}))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent('Deletion temporarily unavailable.')
  await waitFor(() => expect(within(dialog).getAllByRole('checkbox')).toHaveLength(1))
  expect(changed).toHaveBeenCalledTimes(1)
  fireEvent.click(within(dialog).getByRole('button', {name: 'Delete selected files'}))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(artifacts).toEqual([])
})

it('lets a person who may delete only their own attachments select only those, and says why (#231 review, finding 2)', async () => {
  const artifacts: ArtifactInfo[] = [
    {id: 'theirs', name: 'theirs.txt', size: 3, origin: 'user', content_type: 'text/plain', view: 'text', created_at: '2026-10-01', can_delete: false},
    {id: 'mine', name: 'mine.txt', size: 3, origin: 'user', content_type: 'text/plain', view: 'text', created_at: '2026-10-02', can_delete: true},
  ]
  const remove = vi.fn(async () => {})
  const files: TransportFiles = {list: async () => artifacts, space: async () => ({artifacts, limits: {max_session_upload_bytes: 6}, can_delete: false}), remove, upload: async () => {throw new Error('unused')}, raw: async () => new Blob(), download: async () => {}}
  render(<FreeSpaceButton sessionId="s" files={files} waitingBytes={3} />)
  fireEvent.click(screen.getByRole('button', {name: 'Free up space…'}))
  const dialog = screen.getByRole('dialog', {name: 'Free up space'})
  await waitFor(() => expect(within(dialog).getAllByRole('checkbox')).toHaveLength(2))
  const [theirs, mine] = within(dialog).getAllByRole('checkbox') as HTMLInputElement[]
  expect(theirs).toBeDisabled(); expect(theirs.checked).toBe(false)
  expect(mine).not.toBeDisabled(); expect(mine.checked).toBe(true)
  expect(dialog).toHaveTextContent('You can delete the files you attached.')
  fireEvent.click(within(dialog).getByRole('button', {name: 'Delete selected files'}))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(remove).toHaveBeenCalledTimes(1)
  expect(remove).toHaveBeenCalledWith('s', 'mine')
})

it('shows a refused delete in words, not an HTTP code (#231 review, finding 10)', async () => {
  const artifacts: ArtifactInfo[] = [{id: 'a', name: 'a.txt', size: 3, origin: 'user', content_type: 'text/plain', view: 'text', created_at: '2026-10-01', can_delete: true}]
  const files: TransportFiles = {list: async () => artifacts, space: async () => ({artifacts, limits: {max_session_upload_bytes: 3}, can_delete: true}), remove: async () => {throw new TypeError('Failed to fetch')}, upload: async () => {throw new Error('unused')}, raw: async () => new Blob(), download: async () => {}}
  render(<FreeSpaceButton sessionId="s" files={files} waitingBytes={3} />)
  fireEvent.click(screen.getByRole('button', {name: 'Free up space…'}))
  const dialog = screen.getByRole('dialog', {name: 'Free up space'})
  await waitFor(() => expect(within(dialog).getAllByRole('checkbox')).toHaveLength(1))
  fireEvent.click(within(dialog).getByRole('button', {name: 'Delete selected files'}))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent('Couldn’t reach the server')
})
