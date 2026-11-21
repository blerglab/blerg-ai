import { expect, it, vi } from 'vitest'
const fetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: fetch }))
vi.mock('./liveBoardCard', () => ({ signedInAccount: () => 'person' }))
import { boardViewRequest } from './boardViewRequest'
it('shares overlapping board reads while giving each tab a consumable body', async () => {
 let resolve!: (response: Response) => void
 fetch.mockReturnValueOnce(new Promise<Response>(done => { resolve = done }))
 const first = boardViewRequest('/board'), second = boardViewRequest('/board')
 expect(fetch).toHaveBeenCalledTimes(1)
 resolve(new Response('{"cards":[]}'))
 expect(await (await first).json()).toEqual({ cards: [] })
 expect(await (await second).json()).toEqual({ cards: [] })
 fetch.mockResolvedValueOnce(new Response('{}'))
 await boardViewRequest('/board')
 expect(fetch).toHaveBeenCalledTimes(2)
})
