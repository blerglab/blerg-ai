import {act, renderHook, waitFor} from '@testing-library/react'
import {afterEach, expect, it, vi} from 'vitest'
import {resetKeptAttachments, useAttachments} from './useAttachments'
import {FileRequestError} from '../transport/files'
import type {TransportFiles} from '../transport/types'

vi.mock('../model/attachmentPreview', () => ({createAttachmentPreview: async () => undefined}))
afterEach(() => resetKeptAttachments())
it('keeps a quota refusal waiting and resumes when space is available', async () => {
  const upload = vi.fn().mockRejectedValueOnce(new FileRequestError('total exceeded', 409, undefined, 'file_space_required')).mockResolvedValue({id: 'new', name: 'new.txt', size: 3})
  const files: TransportFiles = {list: async () => [], raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload}
  const {result} = renderHook(() => useAttachments('space', files))
  act(() => result.current.addFiles([new File(['abc'], 'new.txt')]))
  await waitFor(() => expect(result.current.items[0]?.status).toBe('waiting'))
  // A waiting file is not an upload in flight: Send is not held for it (#231 review, finding 6).
  expect(result.current.uploading).toBe(false)
  expect(result.current.waitingCount).toBe(1)
  await act(() => result.current.retryWaiting())
  await waitFor(() => expect(result.current.done).toEqual([{id: 'new', name: 'new.txt', size: 3}]))
})
it('keeps version and per-file refusals as immediate errors', async () => {
  const upload = vi.fn().mockRejectedValue(new FileRequestError('20 versions already', 409))
  const files: TransportFiles = {list: async () => [], raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload}
  const {result} = renderHook(() => useAttachments('versions', files, 4))
  act(() => result.current.addFiles([new File(['abc'], 'version.txt'), new File(['12345'], 'large.txt')]))
  await waitFor(() => expect(result.current.items.every(a => a.status === 'error')).toBe(true))
  expect(upload).toHaveBeenCalledTimes(1)
  expect(result.current.items[1]?.error).toContain('larger than')
})
it('retains queued bytes when a space check temporarily fails, then retries on the polling clock', async () => {
  vi.useFakeTimers({toFake: ['setInterval', 'clearInterval']})
  try {
    let available = false
    let offline = false
    const upload = vi.fn(async (_sid: string, file: File) => ({id: 'up', name: file.name, size: file.size}))
    const space = vi.fn(async () => {
      if (offline) throw new Error('temporarily offline')
      return {artifacts: available ? [] : [{id: 'old', name: 'old.txt', origin: 'user' as const, size: 4, content_type: 'text/plain', view: 'text'}], limits: {max_session_upload_bytes: 4}, can_delete: true}
    })
    const files: TransportFiles = {list: async () => [], space, raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload}
    const {result} = renderHook(() => useAttachments('poll', files))
    await act(async () => result.current.addFiles([new File(['abc'], 'poll.txt')]))
    expect(result.current.items[0]?.status).toBe('waiting')
    offline = true
    await act(async () => vi.advanceTimersByTimeAsync(5000))
    expect(result.current.items[0]?.status).toBe('waiting')
    expect(result.current.items[0]?.file?.size).toBe(3)
    offline = false; available = true
    await act(async () => vi.advanceTimersByTimeAsync(5000))
    expect(result.current.done).toEqual([{id: 'up', name: 'poll.txt', size: 3}])
    expect(upload).toHaveBeenCalledTimes(1)
  } finally {vi.useRealTimers()}
})
it('retains a queued file after a rate-limited space check and resumes on retry', async () => {
  const space = vi.fn().mockRejectedValueOnce(new FileRequestError('Try again shortly.', 429)).mockResolvedValue({artifacts: [], limits: {max_session_upload_bytes: 8}, can_delete: true})
  const upload = vi.fn(async (_sid: string, file: File) => ({id: 'up', name: file.name, size: file.size}))
  const files: TransportFiles = {list: async () => [], space, raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload}
  const {result} = renderHook(() => useAttachments('limited', files))
  act(() => result.current.addFiles([new File(['abc'], 'waiting.txt')]))
  await waitFor(() => expect(result.current.items[0]?.status).toBe('waiting'))
  await act(() => result.current.retryWaiting())
  expect(result.current.done).toHaveLength(1)
})

// #231 review, finding 4: files chosen together that only fit one at a time upload one by one;
// the one that no longer fits waits instead of failing.
it('uploads one at a time, so a second file that no longer fits waits instead of failing', async () => {
  const stored: {id: string; name: string; origin: 'user'; size: number; content_type: string; view: string}[] = []
  let concurrent = 0, most = 0
  const upload = vi.fn(async (_sid: string, file: File) => {
    concurrent++; most = Math.max(most, concurrent)
    await new Promise(r => setTimeout(r, 10))
    concurrent--
    if (stored.reduce((n, a) => n + a.size, 0) + file.size > 5) throw new TypeError('The upload was cut off')
    stored.push({id: file.name, name: file.name, origin: 'user', size: file.size, content_type: 'text/plain', view: 'text'})
    return {id: file.name, name: file.name, size: file.size}
  })
  const space = vi.fn(async () => ({artifacts: [...stored], limits: {max_session_upload_bytes: 5}, can_delete: true}))
  const files: TransportFiles = {list: async () => [], space, raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload}
  const {result} = renderHook(() => useAttachments('together', files))
  act(() => result.current.addFiles([new File(['abc'], 'one.txt'), new File(['def'], 'two.txt')]))
  await waitFor(() => expect(result.current.items.map(i => i.status)).toEqual(['done', 'waiting']))
  expect(most).toBe(1)
  expect(upload).toHaveBeenCalledTimes(1)
  expect(result.current.items[1]?.file?.size).toBe(3)
})

// A cut-off upload (the server refused the body before reading it) waits when the session turns
// out to be full, rather than failing and dropping the bytes.
it('turns a cut-off upload into a waiting one when the session is full', async () => {
  let full = false
  const upload = vi.fn(async () => {full = true; throw new TypeError('The upload was cut off before the server answered')})
  const space = vi.fn(async () => ({artifacts: full ? [{id: 'x', name: 'x', origin: 'user' as const, size: 8, content_type: 'text/plain', view: 'text'}] : [], limits: {max_session_upload_bytes: 8}, can_delete: true}))
  const files: TransportFiles = {list: async () => [], space, raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload}
  const {result} = renderHook(() => useAttachments('cutoff', files))
  act(() => result.current.addFiles([new File(['abc'], 'cut.txt')]))
  await waitFor(() => expect(result.current.items[0]?.status).toBe('waiting'))
  expect(result.current.items[0]?.wait).toBe('space')
  expect(result.current.items[0]?.file?.size).toBe(3)
})

it('says the server cannot be reached when the space check fails, not that there is no space', async () => {
  const files: TransportFiles = {list: async () => [], space: async () => {throw new TypeError('Failed to fetch')}, raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload: vi.fn()}
  const {result} = renderHook(() => useAttachments('outage', files))
  act(() => result.current.addFiles([new File(['abc'], 'o.txt')]))
  await waitFor(() => expect(result.current.items[0]?.status).toBe('waiting'))
  expect(result.current.items[0]?.wait).toBe('offline')
  expect(result.current.waitingForSpace).toBe(false)
})

// Finding 7/10: an ended session stops polling and drops the waiting bytes.
it('fails waiting files and stops polling once the session ends', async () => {
  vi.useFakeTimers({toFake: ['setInterval', 'clearInterval']})
  try {
    const space = vi.fn(async () => ({artifacts: [{id: 'x', name: 'x', origin: 'user' as const, size: 8, content_type: 'text/plain', view: 'text'}], limits: {max_session_upload_bytes: 8}, can_delete: true}))
    const files: TransportFiles = {list: async () => [], space, raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload: vi.fn()}
    const {result, rerender} = renderHook(({ended}) => useAttachments('ending', files, undefined, {ended}), {initialProps: {ended: false}})
    await act(async () => result.current.addFiles([new File(['abc'], 'late.txt')]))
    expect(result.current.items[0]?.status).toBe('waiting')
    rerender({ended: true})
    expect(result.current.items[0]?.status).toBe('error')
    expect(result.current.items[0]?.file).toBeUndefined()
    const calls = space.mock.calls.length
    await act(async () => vi.advanceTimersByTimeAsync(20000))
    expect(space.mock.calls.length).toBe(calls)
  } finally {vi.useRealTimers()}
})

it('keeps waiting files when a message is sent, and clears the sent ones', async () => {
  const space = vi.fn(async () => ({artifacts: [], limits: {max_session_upload_bytes: 4}, can_delete: true}))
  const files: TransportFiles = {list: async () => [], space, raw: async () => new Blob(), download: async () => {}, remove: async () => {}, upload: async (_s: string, f: File) => ({id: f.name, name: f.name, size: f.size})}
  const {result} = renderHook(() => useAttachments('send', files))
  act(() => result.current.addFiles([new File(['abc'], 'fits.txt'), new File(['abcdefgh'], 'big.txt')]))
  await waitFor(() => expect(result.current.items.map(i => i.status)).toEqual(['done', 'waiting']))
  act(() => result.current.clear())
  expect(result.current.items.map(i => [i.name, i.status])).toEqual([['big.txt', 'waiting']])
})
