import { beforeEach, expect, it, vi } from 'vitest'
import { blackboardReads, invalidateBlackboardReads, readBlackboards, resetBlackboardReadsForTest } from './blackboardReads'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
beforeEach(() => { resetBlackboardReadsForTest(); apiFetch.mockReset() })

it('does not let an older read replace a list refreshed after a browser change', async () => {
 let finish!: (response: Response) => void
 apiFetch.mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
 const old = readBlackboards('main')
 invalidateBlackboardReads()
 const current = [{ id: 'created', name: 'Plan', milestones: [] }]
 apiFetch.mockResolvedValueOnce(new Response(JSON.stringify({ blackboards: current })))
 await readBlackboards('main')
 finish(new Response(JSON.stringify({ blackboards: [] })))
 await old
 expect(blackboardReads.get('main')?.list).toEqual(current)
 expect(await readBlackboards('main')).toEqual(current)
 expect(apiFetch).toHaveBeenCalledTimes(2)
})

it('invalidates only the changed workspace', async () => {
 apiFetch.mockImplementation(async () => new Response(JSON.stringify({ blackboards: [] })))
 await readBlackboards('main')
 await readBlackboards('other')
 invalidateBlackboardReads(['main'])
 await readBlackboards('main')
 await readBlackboards('other')
 expect(apiFetch).toHaveBeenCalledTimes(3)
})
it('logs one safe diagnostic for a refused read', async () => {
 const log = vi.spyOn(console, 'error').mockImplementation(() => {})
 try {
  apiFetch.mockResolvedValue(new Response('private-canary', { status: 503 }))
  await readBlackboards('main')
  expect(log).toHaveBeenCalledTimes(1)
  expect(log.mock.calls[0]).toEqual(['Could not read blackboards.', { cause: 'response', status: 503 }])
 } finally { log.mockRestore() }
})
