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
beforeEach(() => { window.dispatchEvent(new Event('blerg-identity-changed')); invalidateChatTransport(); wsMock.send.mockReturnValue(true); vi.useRealTimers(); useTranscriptStore.setState({sessions:{}}) })
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

for (const first of ['queued redaction', 'reconnect', 'sidebar removal'] as const) {
  for (const cache of ['transcript', 'transport', 'both'] as const) {
    it(`${first} clears ${cache} desktop scope independently of the current sidebar row`, async () => {
      const daemon = makeDaemon({ id: 'desktop-a' })
      const session = makeSession({ id: 'cached', daemon_id: daemon.id })
      // Exactly one accessible row: no second session can accidentally cause the reset.
      wsMock.emit({ type: 'initial_state', daemons: [daemon], sessions: [session] })
      const store = useTranscriptStore.getState()
      if (cache !== 'transport') {
        store.ingest({ type: 'agent_event', session_id: session.id, client_event_id: 'prose', seq: 1, ts: '2026-01-01T00:00:00Z', kind: 'user_message', payload: { text: 'Shared prose' } })
        store.ingest({ type: 'agent_event', session_id: session.id, client_event_id: 'caps', seq: 2, ts: '2026-01-01T00:00:01Z', kind: 'capabilities', payload: report })
        store.ingestReplayDone(session.id, { lastSeq: 2, hasMore: true })
      }
      const old = blergTransport()
      let older: Promise<unknown> | undefined
      const cancelled = vi.fn()
      if (cache !== 'transcript') {
        old.subscribe(session.id, { afterSeq: 2 }, {
          onEvent: store.ingest,
          onEvents: events => store.ingestMany(session.id, events),
          onReplayDone: vi.fn(), onStreaming: vi.fn(), onStatus: vi.fn(), onConnection: vi.fn(),
        })
        wsMock.flushOpen()
        // Neither this forward replay nor this older read has reached the transcript yet.
        wsMock.emit({ type: 'agent_event', session_id: session.id, client_event_id: 'buffered', seq: 3, ts: '2026-01-01T00:00:02Z', kind: 'capabilities', payload: report })
        older = old.loadOlder!(session.id, 2, 200).catch(() => { cancelled(); return null })
      }
      const reset = vi.fn()
      window.addEventListener('blerg-desktop-scope-changed', reset)
      const redacted = { ...session, daemon_id: '', project_path: '', repo: '' }
      try {
        if (first === 'queued redaction') {
          // Delivery-time redaction after reassignment, then a disconnect before
          // the old socket's scope watcher can send desktop_inventory_invalidated.
          act(() => wsMock.emit({ type: 'session_started', session: redacted }))
        } else if (first === 'sidebar removal') {
          // A cached transcript or replay can outlive its sidebar row entirely.
          useSessionStore.setState({ sessions: [] })
        }
        if (first !== 'queued redaction') {
          act(() => wsMock.emit({ type: 'initial_state', daemons: [], sessions: first === 'sidebar removal' ? [] : [redacted] }))
        }
        // Loss is handled by whichever authoritative message arrives first.
        expect.soft(reset).toHaveBeenCalledOnce()
        if (cache !== 'transport') {
          const transcript = useTranscriptStore.getState().sessions[session.id]
          expect.soft(transcript.events.map(event => event.kind)).toEqual(['user_message'])
          expect.soft(transcript).toMatchObject({ lastSeq: 0, firstSeq: 0, replayDone: false, hasMore: false, hasOlder: false })
          expect.soft(transcript.replayCursor).toBeUndefined()
        }
        expect.soft(blergTransport()).not.toBe(old)
        if (cache !== 'transcript') {
          await Promise.resolve()
          expect.soft(cancelled).toHaveBeenCalledOnce()
        }
        wsMock.setSocketOpen(false)
        wsMock.setSocketOpen(true)
        act(() => wsMock.emit({ type: 'initial_state', daemons: [], sessions: [redacted] }))
        act(() => wsMock.emit({ type: 'session_started', session: redacted }))
        wsMock.flushOpen()
        wsMock.emit({ type: 'agent_events_replay_done', session_id: session.id, last_seq: 3, has_more: false })
        wsMock.emit({ type: 'agent_events_replay_done', session_id: session.id, last_seq: 2, has_more: false, older: true, first_seq: 1, has_older: false })
        expect.soft(reset).toHaveBeenCalledOnce()
        expect.soft(useTranscriptStore.getState().sessions[session.id]?.events.some(event => event.kind === 'capabilities') ?? false).toBe(false)
      } finally {
        window.removeEventListener('blerg-desktop-scope-changed', reset)
        invalidateChatTransport()
        await older
      }
    })
  }
}

it('keeps cached scope, buffered replay and pending older reads through same-viewer enrollment', async () => {
  const daemon = makeDaemon({ id: 'desktop-a', compatibility: true })
  const session = makeSession({ id: 'cached', daemon_id: daemon.id, desktop_compatibility: true })
  wsMock.emit({ type: 'initial_state', daemons: [daemon], sessions: [session] })
  const store = useTranscriptStore.getState()
  store.ingest({ type: 'agent_event', session_id: session.id, client_event_id: 'prose', seq: 2, ts: '2026-01-01T00:00:00Z', kind: 'user_message', payload: { text: 'Shared prose' } })
  store.ingestReplayDone(session.id, { lastSeq: 2, hasMore: false })
  const transcript = useTranscriptStore.getState().sessions[session.id]
  const transport = blergTransport()
  transport.subscribe(session.id, { afterSeq: 2 }, {
    onEvent: store.ingest, onEvents: events => store.ingestMany(session.id, events),
    onReplayDone: vi.fn(), onStreaming: vi.fn(), onStatus: vi.fn(), onConnection: vi.fn(),
  })
  wsMock.flushOpen()
  wsMock.emit({ type: 'agent_event', session_id: session.id, client_event_id: 'buffered', seq: 3, ts: '2026-01-01T00:00:01Z', kind: 'capabilities', payload: report })
  const older = transport.loadOlder!(session.id, 2, 200)
  const reset = vi.fn()
  window.addEventListener('blerg-desktop-scope-changed', reset)
  try {
    wsMock.emit({ type: 'desktop_sources_changed', desktop_id: daemon.id })
    wsMock.emit({ type: 'session_started', session: { ...session, desktop_compatibility: false } })
    wsMock.emit({ type: 'initial_state', daemons: [{ ...daemon, compatibility: false }], sessions: [{ ...session, desktop_compatibility: false }] })
    expect(reset).not.toHaveBeenCalled()
    expect(blergTransport()).toBe(transport)
    expect(useTranscriptStore.getState().sessions[session.id]).toBe(transcript)
    wsMock.emit({ type: 'agent_events_replay_done', session_id: session.id, last_seq: 1, has_more: false, older: true, first_seq: 1, has_older: false })
    await expect(older).resolves.toMatchObject({ events: [] })
    wsMock.emit({ type: 'agent_events_replay_done', session_id: session.id, last_seq: 3, has_more: false })
    expect(useTranscriptStore.getState().sessions[session.id].events.map(event => event.kind)).toEqual(['user_message', 'capabilities'])
  } finally {
    window.removeEventListener('blerg-desktop-scope-changed', reset)
  }
})

it('keeps cluster startup and its later snapshot outside desktop ownership scope', () => {
  const session = makeSession({ id: 'cached', runtime: 'cluster', daemon_id: 'cluster', status: 'starting' })
  wsMock.emit({ type: 'initial_state', daemons: [], sessions: [session] })
  const store = useTranscriptStore.getState()
  store.ingest({ type: 'agent_event', session_id: session.id, client_event_id: 'caps', seq: 1, ts: '2026-01-01T00:00:00Z', kind: 'capabilities', payload: report })
  store.ingestReplayDone(session.id, { lastSeq: 1, hasMore: false })
  const transcript = useTranscriptStore.getState().sessions[session.id]
  const transport = blergTransport()
  transport.subscribe(session.id, { afterSeq: 1 }, {
    onEvent: store.ingest, onEvents: events => store.ingestMany(session.id, events),
    onReplayDone: vi.fn(), onStreaming: vi.fn(), onStatus: vi.fn(), onConnection: vi.fn(),
  })
  const reset = vi.fn()
  window.addEventListener('blerg-desktop-scope-changed', reset)
  try {
    const running = { ...session, daemon_id: 'pod-daemon', status: 'running' as const }
    wsMock.emit({ type: 'session_started', session: running })
    expect.soft(reset).not.toHaveBeenCalled()
    expect.soft(useTranscriptStore.getState().sessions[session.id]).toBe(transcript)
    expect.soft(blergTransport()).toBe(transport)
    wsMock.emit({ type: 'initial_state', daemons: [], sessions: [running] })
    expect.soft(reset).not.toHaveBeenCalled()
    expect.soft(useTranscriptStore.getState().sessions[session.id]).toBe(transcript)
    expect.soft(blergTransport()).toBe(transport)
  } finally {
    window.removeEventListener('blerg-desktop-scope-changed', reset)
  }
})
