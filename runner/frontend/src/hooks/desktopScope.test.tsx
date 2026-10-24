import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { useTranscriptStore } from '@blerglab/chat'
import { useCapabilities } from './useCapabilities'
import { blergTransport, invalidateChatTransport } from '../lib/chatTransport'
import { useSessionStore } from './useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
vi.mock('../ws', () => import('../test/wsMock'))
const noEvents: never[] = []
const report = {cwd:'/private-old-owner',engine:'codex',groups:[]}
const useCachedCapabilities = () => useCapabilities('cached', useTranscriptStore(s => s.sessions.cached?.events) ?? noEvents)
beforeEach(() => { invalidateChatTransport(); wsMock.send.mockReturnValue(true); vi.useRealTimers(); useTranscriptStore.setState({sessions:{}}) })
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })
for (const type of ['desktop_ownership_changed', 'desktop_inventory_invalidated'] as const) {
  it(`${type} clears real transcript source caches and replay before remount, retaining prose`, async () => {
    useSessionStore.setState({daemons:[makeDaemon({id:'desktop-a'})],sessions:[makeSession({id:'cached',daemon_id:'desktop-a'})]})
    const store=useTranscriptStore.getState()
    for (const [index, kind] of (['user_message','assistant_text','capabilities','delegation_request','start_stage'] as const).entries()) {
      store.ingest({type:'agent_event',session_id:'cached',client_event_id:kind,seq:index+1,ts:'2026-01-01T00:00:00Z',kind,payload:kind==='capabilities'?report:{text:'Shared prose',repo:'/private-old-owner',detail:'/private-old-owner'}})
    }
    store.ingestReplayDone('cached',{lastSeq:5,hasMore:true})
    const fetcher=vi.fn(async()=>new Response(JSON.stringify({capabilities:report,seq:6})))
    vi.stubGlobal('fetch',fetcher)
    const mounted=renderHook(useCachedCapabilities)
    await waitFor(()=>expect(mounted.result.current?.seq).toBe(6))
    fetcher.mockImplementation(async()=>new Response(JSON.stringify({capabilities:null,seq:0})))
    let atRemount: unknown
    const observe=()=>{atRemount=useTranscriptStore.getState().sessions.cached}
    window.addEventListener('blerg-desktop-scope-changed',observe,{once:true})
    act(()=>wsMock.emit(type==='desktop_ownership_changed'?{type,desktop_id:'desktop-a',ownership_revision:2}:{type}))
    expect(atRemount).toMatchObject({lastSeq:0,firstSeq:0,replayDone:false,hasMore:false,hasOlder:false})
    const transcript=useTranscriptStore.getState().sessions.cached
    expect(transcript.replayCursor).toBeUndefined()
    expect(transcript.events.map(e=>e.kind)).toEqual(['user_message','assistant_text'])
    expect(transcript.events[0].payload).toMatchObject({text:'Shared prose'})
    await waitFor(()=>expect(mounted.result.current).toBeNull())
    mounted.unmount()
    const remounted=renderHook(useCachedCapabilities)
    await waitFor(()=>expect(fetcher).toHaveBeenCalledTimes(3))
    expect(remounted.result.current).toBeNull()
  })
}

for (const type of ['desktop_ownership_changed', 'desktop_inventory_invalidated'] as const) {
  it(`${type} discards a private capability HTTP response arriving after invalidation`, async () => {
    let finish!: (response: Response) => void
    const fetcher=vi.fn().mockImplementationOnce(()=>new Promise<Response>(resolve=>{finish=resolve})).mockImplementation(async()=>new Response(JSON.stringify({capabilities:null})))
    vi.stubGlobal('fetch',fetcher)
    const reader=renderHook(useCachedCapabilities)
    act(()=>wsMock.emit(type==='desktop_ownership_changed'?{type,desktop_id:'desktop-a',ownership_revision:2}:{type}))
    await waitFor(()=>expect(fetcher).toHaveBeenCalledTimes(2))
    await act(async()=>{finish(new Response(JSON.stringify({capabilities:report,seq:99})))})
    expect(reader.result.current).toBeNull()
  })
}

for (const type of ['desktop_ownership_changed', 'desktop_inventory_invalidated'] as const) {
  it(`${type} drops real transport replay buffers and pending older reads before resubscribe`, async () => {
    const old= blergTransport()
    const store=useTranscriptStore.getState()
    const handlers={onEvent:store.ingest,onEvents:(events: Parameters<typeof store.ingestMany>[1])=>store.ingestMany('cached',events),onReplayDone:vi.fn(),onStreaming:vi.fn(),onStatus:vi.fn(),onConnection:vi.fn()}
    old.subscribe('cached',{afterSeq:5},handlers)
    wsMock.flushOpen()
    // Buffered in an unfinished forward replay, not yet in the store.
    wsMock.emit({type:'agent_event',session_id:'cached',client_event_id:'buffered',seq:6,ts:'2026-01-01T00:00:00Z',kind:'capabilities',payload:report})
    let cancelled=false
    const older=old.loadOlder!('cached',5,200).catch(()=>{cancelled=true;return null})
    wsMock.emit({type:'agent_event',session_id:'cached',client_event_id:'older',seq:2,ts:'2026-01-01T00:00:00Z',kind:'capabilities',payload:report})
    act(()=>wsMock.emit(type==='desktop_ownership_changed'?{type,desktop_id:'desktop-a',ownership_revision:2}:{type}))
    await act(async()=>{await Promise.resolve()})
    expect(cancelled).toBe(true)
    expect(await older).toBeNull()
    const fresh=blergTransport()
    expect(fresh).not.toBe(old)
    fresh.subscribe('cached',{afterSeq:0,tail:true},handlers)
    wsMock.flushOpen()
    wsMock.emit({type:'agent_events_replay_done',session_id:'cached',last_seq:6,has_more:false,older:true,first_seq:1,has_older:false})
    expect(useTranscriptStore.getState().sessions.cached?.events ?? []).toEqual([])
  })
}
