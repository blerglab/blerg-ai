import {afterEach, expect, it, vi} from 'vitest'
import {blergTransport, invalidateChatTransport} from './chatTransport'
import * as ws from '../test/wsMock'
import {useTranscriptStore} from '@blerglab/chat'
const auth = vi.hoisted(() => ({token: 'stale-token', refresh: vi.fn(async () => {auth.token = 'fresh-token'; return true})}))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', () => ({getAccessToken: () => auth.token, ensureFreshToken: () => auth.refresh()}))
afterEach(() => {useTranscriptStore.setState({sessions:{}});invalidateChatTransport(); vi.unstubAllGlobals(); vi.clearAllMocks(); auth.token = 'stale-token'})
function liveAsk(bound = false) {useTranscriptStore.getState().ingest({type:'agent_event',session_id:'session',client_event_id:'live-ask',seq:1,ts:'2026-11-05T00:00:00Z',kind:'ask',payload:{message_id:'ask',live_binding:!bound,bound}})}
for (const kind of ['live binding', 'plan', 'cut', 'draft', 'board link']) it(`refreshes and retries a ${kind} answer with the current human token instead of the socket token`, async () => {
 liveAsk(kind !== 'live binding')
 const fetcher = vi.fn().mockResolvedValueOnce(new Response('{}', {status:401})).mockResolvedValueOnce(new Response('{}', {status:200}))
 vi.stubGlobal('fetch', fetcher)
 const result = await blergTransport().send('session','yes',{answerTo:'ask',clientId:'input'})
 expect(result.queued).toBe(true)
 expect(auth.refresh).toHaveBeenCalledTimes(1)
 expect(fetcher).toHaveBeenCalledTimes(2)
 expect(fetcher.mock.calls[0][0]).toBe('/api/messages/ask/reply')
 expect(fetcher.mock.calls[0][1].headers.Authorization).toBe('Bearer stale-token')
 expect(fetcher.mock.calls[1][1].headers.Authorization).toBe('Bearer fresh-token')
 expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({answer:'yes',client_id:'input'})
 expect(ws.send).not.toHaveBeenCalled()
})
it('keeps an unavailable live decision open and sends ordinary chat through the socket', async () => {
 liveAsk()
 vi.stubGlobal('fetch',vi.fn(async()=>new Response('{}',{status:409})))
 expect((await blergTransport().send('session','yes',{answerTo:'ask'})).queued).toBe(false)
 ws.send.mockReturnValue(true)
 expect((await blergTransport().send('session','hello')).queued).toBe(true)
 expect(ws.send).toHaveBeenCalledWith({type:'agent_user_message',session_id:'session',text:'hello'})
})

it('cannot send an answer through an invalidated transport', async () => {
 const fetcher=vi.fn(async()=>new Response('{}',{status:200}));vi.stubGlobal('fetch',fetcher)
 const transport=blergTransport();invalidateChatTransport()
 expect((await transport.send('session','yes',{answerTo:'ask'})).queued).toBe(false)
 expect(fetcher).not.toHaveBeenCalled()
})

for (const kind of ['ordinary','closed','unknown']) it(`keeps a ${kind} ask answer on the durable socket path`, async () => {
 const fetcher=vi.fn(async()=>new Response('{}',{status:409}));vi.stubGlobal('fetch',fetcher)
 if(kind!=='unknown') useTranscriptStore.getState().ingest({type:'agent_event',session_id:'session',client_event_id:'ordinary-ask',seq:1,ts:'2026-11-05T00:00:00Z',kind:'ask',payload:{message_id:'ask',body:'Approve live binding · untrusted prose',bound:false,live_binding:false}})
 ws.send.mockReturnValue(true)
 expect((await blergTransport().send('session','yes',{answerTo:'ask',clientId:'durable-input'})).queued).toBe(true)
 expect(ws.send).toHaveBeenCalledWith({type:'agent_user_message',session_id:'session',text:'yes',answer_message_id:'ask',client_id:'durable-input'})
 expect(fetcher).not.toHaveBeenCalled();expect(auth.refresh).not.toHaveBeenCalled()
})
