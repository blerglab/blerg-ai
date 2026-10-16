import { describe, expect, it } from 'vitest'
import { WAITING_ON_INITIAL, callDetail, liveCalls, liveTurn, longRunningTitle, nextWaitingOn, oldestLive, runningLabel, runningMs } from './liveCalls'
import type { AgentEvent } from '../types'

const S = 1000
let n = 0
const ev = (kind: string, payload: unknown, ts: string = new Date(1_000_000).toISOString()): AgentEvent =>
  ({ type: 'agent_event', session_id: 's', client_event_id: `c${++n}`, seq: n, ts, kind, payload }) as AgentEvent
const call = (id: string, ts?: string) => ev('tool_call', { tool: 'Bash', call_id: id, input: {} }, ts)

describe('runningLabel', () => {
  it('formats m:ss and h:mm:ss, clamping negatives', () => {
    expect(runningLabel(0)).toBe('0:00')
    expect(runningLabel(59 * S)).toBe('0:59')
    expect(runningLabel(60 * S)).toBe('1:00')
    expect(runningLabel(3599 * S)).toBe('59:59')
    expect(runningLabel(3600 * S)).toBe('1:00:00')
    expect(runningLabel(-5 * S)).toBe('0:00')
  })
})

describe('runningMs', () => {
  it('never returns negative or NaN', () => {
    expect(runningMs(10_000, 5_000, 0)).toBe(0)
    expect(runningMs(null, 5_000, 0)).toBeNull()
    expect(runningMs(1000, NaN, 0)).toBeNull()
    expect(runningMs(1000, 9000, 2000)).toBe(6000)
  })
})

describe('longRunningTitle', () => {
  it('names the minutes', () => {
    expect(longRunningTitle(2 * 60_000)).toBe('This command has been running for 2 minutes. Claude Code reports output only when it finishes.')
  })
})

describe('liveCalls', () => {
  it('is empty for an ended session', () => {
    expect(liveCalls([call('a')], false)).toEqual([])
  })
  it('lists unresolved calls of the current turn only, oldest first', () => {
    const events = [
      call('old'), ev('turn_done', {}),
      ev('user_message', { text: 'x' }),
      call('a'), call('b'), ev('tool_result', { call_id: 'a', output: '' }),
      call('c', 'garbage'),
    ]
    const l = liveCalls(events, true)
    expect(l.map(c => c.callId)).toEqual(['b', 'c'])
    expect(l[1].startedAt).toBeNull()
    expect(oldestLive(l)?.callId).toBe('b')
  })
})

describe('liveTurn', () => {
  const status = (s: string) => ev('status_changed', { status: s })
  const result = (id: string) => ev('tool_result', { call_id: id, output: '' })
  const ids = (t: { calls: Array<{ callId: string }> }) => t.calls.map(c => c.callId)
  it('is not bounded when the events hold no start of the turn; the calls are all those unresolved', () => {
    // A tail page from the middle of a long turn: the orphan may be a call long over. The cards
    // still tick for it; the working line names none (it reads bounded).
    const t = liveTurn([call('orphan'), call('a'), result('a')], true)
    expect(t.bounded).toBe(false)
    expect(t.turnStart).toBeNull()
    expect(ids(t)).toEqual(['orphan'])
    expect(liveCalls([call('orphan')], true).map(c => c.callId)).toEqual(['orphan'])
  })
  it('never counts an orphan of an earlier turn', () => {
    const events = [ev('user_message', { text: 'x' }), call('orphan'), ev('turn_done', {}), status('idle'), status('running'), call('b')]
    const t = liveTurn(events, true)
    expect(ids(t)).toEqual(['b'])
    expect(t.bounded).toBe(true)
    expect(t.turnStart).toBe(events[4].client_event_id)
    // An error that ends a turn ends it too; an API retry notice does not.
    const errored = [ev('user_message', { text: 'x' }), call('orphan'), ev('error', { message: 'boom' }), call('c')]
    expect(ids(liveTurn(errored, true))).toEqual(['c'])
    const retried = [ev('user_message', { text: 'x' }), call('d'), ev('error', { message: 'slow', code: 'api_retry' }), call('e')]
    expect(ids(liveTurn(retried, true))).toEqual(['d', 'e'])
  })
  it('starts the turn at a running status change (a turn with no message from the person)', () => {
    const t = liveTurn([call('orphan'), status('running'), call('a')], true)
    expect([ids(t), t.bounded]).toEqual([['a'], true])
    // A system message is not a turn start.
    const sys = liveTurn([call('orphan'), ev('user_message', { text: 'x', source: 'system' }), call('a')], true)
    expect([ids(sys), sys.bounded]).toEqual([['orphan', 'a'], false])
  })
  it('takes an ask (waiting, the answer, then running again) as part of the turn', () => {
    // The order the native loop records: the ask's call, waiting, the answer, running, its result.
    const start = ev('user_message', { text: 'x' })
    const events = [
      start, status('running'), call('long'), call('ask'), status('waiting'),
      ev('user_message', { text: 'yes', source: 'ask_answer', message_id: 'm1' }), status('running'),
      result('ask'), call('next'),
    ]
    const t = liveTurn(events, true)
    expect(ids(t)).toEqual(['long', 'next'])
    expect(t.turnStart).toBe(events[1].client_event_id)
    expect(ids(liveTurn([start, call('ask'), status('waiting')], true))).toEqual(['ask'])
  })
  it('is nothing for a session that is not running or waiting', () => {
    expect(liveTurn([ev('user_message', { text: 'x' }), call('a')], false)).toEqual({ calls: [], bounded: false, turnStart: null })
  })
})

describe('callDetail', () => {
  it("is a Bash call's description, else the start of its command, cut to 48", () => {
    expect(callDetail('Bash', { command: 'npm test', description: 'Run the unit tests' })).toBe('Run the unit tests')
    expect(callDetail('Bash', { command: '\n  npm test -- --watch=false\nnpm run lint' })).toBe('npm test -- --watch=false')
    expect(callDetail('Bash', { command: 'npm test', description: '   ' })).toBe('npm test')
    const long = 'x'.repeat(60)
    expect(callDetail('Bash', { command: long })).toBe('x'.repeat(47) + '…')
    expect(callDetail('Bash', { description: long })).toBe('x'.repeat(47) + '…')
    expect(callDetail('Bash', { command: 'x'.repeat(48) })).toBe('x'.repeat(48))
  })
  it("is an Agent's or a Monitor's description, and nothing for other tools", () => {
    expect(callDetail('Agent', { description: 'Survey the repo', prompt: 'p' })).toBe('Survey the repo')
    expect(callDetail('Task', { description: 'Survey' })).toBe('Survey')
    expect(callDetail('Monitor', { command: 'tail -f x', description: 'Watch the deploy' })).toBe('Watch the deploy')
    expect(callDetail('Read', { file_path: '/a/b.ts', description: 'x' })).toBe('')
    expect(callDetail('Bash', null)).toBe('')
    expect(callDetail('Bash', { command: 3 })).toBe('')
  })
  it('is carried by a live call', () => {
    const l = liveCalls([ev('tool_call', { tool: 'Bash', call_id: 'a', input: { command: 'make', description: 'Build it' } })], true)
    expect(l[0].detail).toBe('Build it')
  })
})

describe('nextWaitingOn', () => {
  const call = (tool: string, startedAt: number) => ({ tool, startedAt })
  it('names a call only after 2 s, holds it 3 s, then follows or drops', () => {
    let s = WAITING_ON_INITIAL
    s = nextWaitingOn(s, call('Bash', 0), 1_900)
    expect(s.shown).toBeNull()
    s = nextWaitingOn(s, call('Bash', 0), 2_000)
    expect(s.shown).toEqual(call('Bash', 0))
    // The call ended and another began: held until 3 s have passed.
    expect(nextWaitingOn(s, call('Read', 2_500), 4_900)).toBe(s)
    expect(nextWaitingOn(s, call('Read', 2_500), 5_000).shown).toEqual(call('Read', 2_500))
    expect(nextWaitingOn(s, null, 5_000).shown).toBeNull()
    // Nothing changed: the same state back.
    expect(nextWaitingOn(s, call('Bash', 0), 9_000)).toBe(s)
  })
  it('carries the detail of the call it names, and holds it the same way', () => {
    let s = WAITING_ON_INITIAL
    s = nextWaitingOn(s, { tool: 'Bash', startedAt: 0, detail: 'Run the tests' }, 2_000)
    expect(s.shown).toEqual({ tool: 'Bash', startedAt: 0, detail: 'Run the tests' })
    expect(nextWaitingOn(s, { tool: 'Bash', startedAt: 2_500, detail: 'Lint' }, 4_900)).toBe(s)
    expect(nextWaitingOn(s, { tool: 'Bash', startedAt: 2_500, detail: 'Lint' }, 5_000).shown).toEqual({ tool: 'Bash', startedAt: 2_500, detail: 'Lint' })
  })
})
