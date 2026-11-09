import { beforeEach, expect, it, vi } from 'vitest'
import { useMessageStore } from '../hooks/useMessageStore'
import { questionThreads } from './questionThreads'
vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
beforeEach(() => {
 apiFetch.mockReset()
 useMessageStore.setState({messages:[{id:'q1',session_id:'child',kind:'ask',body:'Which branch?',status:'open',created_at:'2026-11-05T12:00:00Z',question_revision:7}]})
})
it.each(['answer','dismiss'] as const)('settles %s locally only after a confirmed revision-bound response',async action=>{
 let resolve!: (response: Response) => void
 apiFetch.mockReturnValue(new Promise<Response>(done=>{resolve=done}))
 const pending=action==='answer'?questionThreads.answer('q1','Use staging',7):questionThreads.dismiss('q1',7)
 expect(useMessageStore.getState().messages[0].status).toBe('open')
 expect(apiFetch).toHaveBeenCalledWith(`/api/messages/q1/${action==='answer'?'reply':'dismiss'}`,expect.objectContaining({method:'POST',body:JSON.stringify(action==='answer'?{answer:'Use staging',revision_seq:7}:{revision_seq:7})}))
 resolve(new Response('{}',{status:200}))
 expect(await pending).toBe(true)
 expect(useMessageStore.getState().messages[0]).toMatchObject({id:'q1',session_id:'child',body:'Which branch?',status:'answered',question_revision:7,...action==='answer'?{answer:'Use staging'}:{dismissed:true}})
})
it.each(['answer','dismiss'] as const)('keeps a revised question open when %s is stale',async action=>{
 apiFetch.mockResolvedValue(new Response(JSON.stringify({error:'Main revised this question'}),{status:409}))
 await expect(action==='answer'?questionThreads.answer('q1','Use staging',7):questionThreads.dismiss('q1',7)).rejects.toThrow('Main revised this question')
 expect(useMessageStore.getState().messages[0].status).toBe('open')
})
