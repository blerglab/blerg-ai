// #281: the page keeps the transcripts (and Files lists, file contents, shell terminals) of the last
// KEPT_SESSIONS sessions viewed; an older one, another account's, or an ended, archived or discarded
// session is forgotten, except the transcript of a session still on screen.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
vi.mock('../ws', () => import('../test/wsMock'))
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import { KEPT_SESSIONS, resetSessionRetentionForTest, retainedSessions, sessionShown } from './sessionRetention'
import { contentKeeper, listKeeper, peekContent, peekList } from '../../../../packages/chat/src/model/viewCache'

const ids = (n: number) => Array.from({ length: n }, (_, i) => `s${i}`)
const seed = (id: string) => useTranscriptStore.getState().ingest({ session_id: id, client_event_id: `${id}-1`, seq: 1, ts: '2026-10-27T00:00:00Z', kind: 'user_message', payload: { text: id } } as AgentEvent)
const held = (id: string) => !!useTranscriptStore.getState().sessions[id]

beforeEach(() => {
  resetSessionRetentionForTest()
  useTranscriptStore.setState({ sessions: {} })
  useSessionStore.setState({ sessions: ids(KEPT_SESSIONS + 2).map(id => makeSession({ id, status: 'idle' })) })
})
afterEach(() => resetSessionRetentionForTest())

it(`keeps the last ${KEPT_SESSIONS} sessions viewed and forgets the eleventh's oldest`, () => {
  const files = {}
  for (const id of ids(KEPT_SESSIONS + 1)) {
    seed(id)
    contentKeeper(files, id, 'f')(id, 10)
    sessionShown('person-a', id)()
  }
  expect(retainedSessions()).toEqual(ids(KEPT_SESSIONS + 1).slice(1))
  expect(held('s0')).toBe(false)
  expect(peekContent(files, 's0', 'f')).toBeUndefined()
  expect(held('s1')).toBe(true)
  expect(peekContent(files, 's1', 'f')).toBe('s1')
})

it('never evicts a session on screen; one that ends on screen is dropped once it leaves (review 6)', async () => {
  seed('s0')
  const leave = sessionShown('person-a', 's0')
  for (const id of ids(KEPT_SESSIONS + 1).slice(1)) sessionShown('person-a', id)()
  expect(held('s0')).toBe(true)
  useSessionStore.setState(s => ({ sessions: s.sessions.map(x => x.id === 's0' ? { ...x, status: 'ended' } : x) }))
  expect(held('s0')).toBe(true)
  expect(retainedSessions()).not.toContain('s0')
  leave()
  await Promise.resolve()
  expect(held('s0')).toBe(false)
})

it('an already ended session is not kept past its visit (review 6)', async () => {
  useSessionStore.setState(s => ({ sessions: s.sessions.map(x => x.id === 's0' ? { ...x, status: 'stopped' } : x) }))
  seed('s0')
  sessionShown('person-a', 's0')()
  await Promise.resolve()
  expect(held('s0')).toBe(false)
  expect(retainedSessions()).toEqual([])
})

it.each(['blerg-login-changed', 'blerg-signed-out', 'blerg-desktop-scope-changed'])('%s forgets kept transcripts, Files lists and contents, and fences reads still out (review 2)', event => {
  const files = {}
  seed('s0'); contentKeeper(files, 's0', 'f')('private file', 12); listKeeper(files, 's0')(['private list'])
  sessionShown('person-a', 's0')()
  const late = contentKeeper(files, 's0', 'g')
  window.dispatchEvent(new Event(event))
  late('late', 4)
  expect(held('s0')).toBe(false)
  expect(peekContent(files, 's0', 'f')).toBeUndefined()
  expect(peekContent(files, 's0', 'g')).toBeUndefined()
  expect(peekList(files, 's0')).toBeUndefined()
})

it('forgets an ended, archived or discarded session that is not on screen', () => {
  for (const id of ['s0', 's1', 's2']) { seed(id); sessionShown('person-a', id)() }
  useSessionStore.setState(s => ({ sessions: s.sessions.filter(x => x.id !== 's2').map(x => x.id === 's0' ? { ...x, status: 'ended' } : x.id === 's1' ? { ...x, archived_at: '2026-10-27T00:00:00Z' } : x) }))
  expect(retainedSessions()).toEqual([])
  expect(['s0', 's1', 's2'].map(held)).toEqual([false, false, false])
})

it('another account forgets everything the first one kept', () => {
  const files = {}
  seed('s0'); contentKeeper(files, 's0', 'f')('secret', 10)
  sessionShown('person-a', 's0')()
  sessionShown('person-b', 's1')()
  expect(held('s0')).toBe(false)
  expect(peekContent(files, 's0', 'f')).toBeUndefined()
  expect(retainedSessions()).toEqual(['s1'])
})
