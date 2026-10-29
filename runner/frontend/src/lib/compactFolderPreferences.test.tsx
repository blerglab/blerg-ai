import { beforeEach, expect, it, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
const fixture = vi.hoisted(() => ({ account: 'person-a', read: vi.fn() }))
vi.mock('./liveBoardCard', () => ({ signedInAccount: () => fixture.account }))
vi.mock('../apiFetch', () => ({ apiFetch: fixture.read }))
beforeEach(() => { vi.resetModules(); localStorage.clear(); fixture.account = 'person-a'; fixture.read.mockReset() })
it('defaults on, saves immediately through account preferences and isolates browser caches by account', async () => {
  const pref = await import('./compactFolders')
  fixture.read.mockResolvedValue(new Response('{}'))
  expect(pref.getCompactFolders()).toBe(true)
  const saving = pref.saveCompactFolders(false)
  expect(pref.getCompactFolders()).toBe(false)
  expect(await saving).toBe('')
  expect(fixture.read.mock.calls[0]).toEqual(['/api/me/preferences', expect.objectContaining({ method: 'PUT', body: '{"compact_folders":false}' })])
  fixture.account = 'person-b'
  expect(pref.getCompactFolders()).toBe(true)
  fixture.account = 'person-a'
  expect(pref.getCompactFolders()).toBe(false)
  expect(localStorage.getItem('blerg-runner.compact-folders:person-a')).toBe('false')
  expect(localStorage.getItem('blerg-runner.compact-folders:person-b')).toBeNull()
})
it('restores the previous choice when saving fails', async () => {
  const pref = await import('./compactFolders')
  fixture.read.mockResolvedValue(new Response('{}', { status: 500 }))
  expect(await pref.saveCompactFolders(false)).toContain('Could not save')
  expect(pref.getCompactFolders()).toBe(true)
})
it('applies the server preference and resets to on for an account with no stored choice', async () => {
  const pref = await import('./compactFolders')
  fixture.read.mockResolvedValueOnce(new Response('{"compact_folders":false}')).mockResolvedValueOnce(new Response('{}'))
  const { result } = renderHook(() => { pref.useCompactFoldersSync(); return pref.useCompactFolders() })
  await waitFor(() => expect(result.current).toBe(false))
  act(() => { fixture.account = 'person-b'; window.dispatchEvent(new Event('blerg-identity-changed')) })
  await waitFor(() => expect(result.current).toBe(true))
})
it('does not let an earlier fetch overwrite a newer choice', async () => {
  const pref = await import('./compactFolders')
  let resolve!: (r: Response) => void
  fixture.read.mockImplementationOnce(() => new Promise<Response>(done => { resolve = done })).mockResolvedValueOnce(new Response('{}'))
  renderHook(pref.useCompactFoldersSync)
  await act(async () => { await pref.saveCompactFolders(false) })
  await act(async () => { resolve(new Response('{}')) })
  expect(pref.getCompactFolders()).toBe(false)
})
it('ignores another account’s late response', async () => {
  const pref = await import('./compactFolders')
  let resolve!: (r: Response) => void
  fixture.read.mockImplementationOnce(() => new Promise<Response>(done => { resolve = done })).mockResolvedValueOnce(new Response('{}'))
  renderHook(pref.useCompactFoldersSync)
  await act(async () => { fixture.account = 'person-b'; window.dispatchEvent(new Event('blerg-identity-changed')) })
  await act(async () => { resolve(new Response('{"compact_folders":false}')) })
  expect(pref.getCompactFolders()).toBe(true)
})
