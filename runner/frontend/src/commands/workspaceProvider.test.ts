import { beforeEach, describe, expect, it, vi } from 'vitest'
import { workspaceProvider } from './workspaceProvider'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: vi.fn(() => 'owner') }))
const reply = () => ({ result: { ok: true, launch_generation: 'launch', generation: 2, truncated: true, unavailable_members: ['missing'], entries: [
  { path: 'app', name: 'app', directory: true },
  { path: 'app/src/main.ts', name: 'main.ts', directory: false, member_id: 'app' },
  { path: 'lib/main.go', name: 'main.go', directory: false, member_id: 'lib' },
] }, members: [{ id: 'app', name: 'App' }, { id: 'lib', name: 'Library' }] })
beforeEach(() => { vi.mocked(apiFetch).mockReset(); vi.mocked(signedInAccount).mockReturnValue('owner') })
describe('P4 quick open provider', () => {
  it('uses bounded root searches across repositories, skips folders, preserves paths and partial state', async () => {
    vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify(reply())))
    const open = vi.fn(), signal = new AbortController().signal
    const result = await workspaceProvider('s/1', open).search('mn', signal)
    expect(apiFetch).toHaveBeenCalledTimes(2)
    expect(apiFetch).toHaveBeenNthCalledWith(1, '/api/sessions/s%2F1/workspace?op=search&path=&filter=mn', { signal })
    expect(apiFetch).toHaveBeenNthCalledWith(2, '/api/sessions/s%2F1/workspace?op=search&path=&filter=m', { signal })
    expect(result.items.map(i => i.title)).toEqual(['main.ts', 'main.go'])
    expect(result.items.map(i => i.detail)).toEqual(['App · app/src/main.ts', 'Library · lib/main.go'])
    expect(result.note).toMatch(/Partial results/)
    expect(result.note).toMatch(/repositories are unavailable/)
    result.items[1].run()
    expect(open).toHaveBeenCalledWith('lib/main.go')
  })
  it.each([403, 404, 409, 500])('fails closed on HTTP %i without another endpoint or traversal fallback', async status => {
    vi.mocked(apiFetch).mockResolvedValue(new Response('{}', { status }))
    await expect(workspaceProvider('s', vi.fn()).search('m', new AbortController().signal)).rejects.toThrow(/unavailable|permission/)
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })
  it('discards a reply after cancellation or a changed sign-in', async () => {
    const controller = new AbortController()
    vi.mocked(apiFetch).mockImplementationOnce(async () => { controller.abort(); return new Response(JSON.stringify(reply())) })
    await expect(workspaceProvider('s', vi.fn()).search('m', controller.signal)).rejects.toThrow()
    vi.mocked(apiFetch).mockImplementationOnce(async () => { vi.mocked(signedInAccount).mockReturnValue('another'); return new Response(JSON.stringify(reply())) })
    await expect(workspaceProvider('s', vi.fn()).search('m', new AbortController().signal)).rejects.toThrow(/sign-in changed/)
  })
  it('does not treat an unsuccessful or malformed result as an empty workspace', async () => {
    for (const value of [{ result: { ok: false } }, {}]) {
      vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify(value)))
      await expect(workspaceProvider('s', vi.fn()).search('m', new AbortController().signal)).rejects.toThrow(/safely searched/)
    }
  })
})

it('does not issue an invalid empty P4 search and keeps selected files only for the same account/runtime', async () => {
  const open = vi.fn(), provider = workspaceProvider('s', open), signal = new AbortController().signal
  expect((await provider.search('', signal)).items).toEqual([])
  expect(apiFetch).not.toHaveBeenCalled()
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify(reply())))
  const results = await provider.search('src/mn', signal)
  expect(String(vi.mocked(apiFetch).mock.calls[1][0])).toContain('filter=m')
  results.items[0].run()
  expect((await provider.search('', signal)).items.map(i => i.title)).toEqual(['main.ts'])
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ ...reply(), result: { ...reply().result, generation: 3 } })))
  await provider.search('m', signal)
  expect((await provider.search('', signal)).items).toEqual([])
  vi.mocked(signedInAccount).mockReturnValue('another')
  expect((await provider.search('', signal)).items).toEqual([])
  results.items[0].run()
  expect(open).toHaveBeenCalledTimes(1)
})

it('keeps exact filename hits ahead of a truncated fuzzy candidate set and refuses mixed generations', async () => {
  const signal = new AbortController().signal
  const exact = { ...reply(), result: { ...reply().result, entries: [{ path: 'last/Needle.ts', name: 'Needle.ts', directory: false }] } }
  vi.mocked(apiFetch).mockImplementation(async path => new Response(JSON.stringify(String(path).includes('filter=Needle.ts') ? exact : reply())))
  const provider = workspaceProvider('s', vi.fn())
  const hits = await provider.search('Needle.ts', signal)
  expect(hits.items[0].title).toBe('Needle.ts')
  expect(apiFetch).toHaveBeenCalledTimes(2)
  vi.mocked(apiFetch).mockReset().mockImplementation(async path => new Response(JSON.stringify(String(path).includes('filter=Needle.ts') ? exact : { ...reply(), result: { ...reply().result, generation: 3 } })))
  await expect(provider.search('Needle.ts', signal)).rejects.toThrow('The workspace changed')
})

it('caps the combined exact and fuzzy candidates at 200 and marks that truncation', async () => {
  const makePage = (prefix: string) => ({ ...reply(), result: { ...reply().result, truncated: false, unavailable_members: [], entries: Array.from({ length: 200 }, (_, i) => ({ name: `${prefix}-${i}`, path: `${prefix}/${i}.ts`, directory: false })) } })
  vi.mocked(apiFetch).mockImplementation(async path => new Response(JSON.stringify(makePage(String(path).includes('filter=main') ? 'exact' : 'fuzzy'))))
  const result = await workspaceProvider('s', vi.fn()).search('main', new AbortController().signal)
  expect(result.items).toHaveLength(200)
  expect(result.items[0].title).toBe('0.ts')
  expect(result.note).toContain('Partial results')
})
