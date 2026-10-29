import {act, renderHook, waitFor} from '@testing-library/react'
import {afterEach, expect, it, vi} from 'vitest'
import {resetKeptAttachments, useAttachments, type AttachmentItem} from './useAttachments'
import {attachmentQueue} from '../model/attachmentQueue'
import type {TransportFiles} from '../transport/types'
vi.mock('../model/attachmentPreview', () => ({createAttachmentPreview: async () => undefined}))
afterEach(() => {vi.restoreAllMocks(); return resetKeptAttachments()})
const files: TransportFiles = {queueScope: () => 'fixture', list: async () => [], raw: async () => new Blob(), remove: async () => {}, download: async () => {}, upload: async (_sid, file) => ({id: file.name, name: file.name, size: file.size})}
const saved: AttachmentItem = {key: 'saved', name: 'saved.txt', size: 3, status: 'waiting', file: new File(['old'], 'saved.txt')}
it('merges a slow durable restore with a file chosen before storage answers', async () => {
  let finish!: (items: AttachmentItem[]) => void
  vi.spyOn(attachmentQueue, 'read').mockImplementation(() => new Promise(resolve => {finish = resolve}))
  const {result} = renderHook(() => useAttachments('restore', files))
  await act(async () => result.current.addFiles([new File(['new'], 'new.txt')]))
  await act(async () => finish([saved]))
  await waitFor(() => expect(result.current.items.map(a => a.name)).toEqual(['saved.txt', 'new.txt']))
})
it('does not resurrect a cleared draft when a slow durable restore finishes', async () => {
  let finish!: (items: AttachmentItem[]) => void
  vi.spyOn(attachmentQueue, 'read').mockImplementation(() => new Promise(resolve => {finish = resolve}))
  const {result} = renderHook(() => useAttachments('clear-restore', files))
  act(() => result.current.clear())
  await act(async () => finish([saved]))
  expect(result.current.items).toEqual([])
})
it('isolates pending chips across accounts viewing the same session', async () => {
  let viewer = 'first-account'
  const scoped = {...files, queueScope: () => viewer}
  const {result, rerender} = renderHook(() => useAttachments('shared-session', scoped))
  await act(async () => result.current.addFiles([new File(['private'], 'private.txt')]))
  expect(result.current.done).toHaveLength(1)
  viewer = 'second-account'
  rerender()
  expect(result.current.items).toEqual([])
  viewer = 'first-account'
  rerender()
  expect(result.current.items[0]?.name).toBe('private.txt')
})
