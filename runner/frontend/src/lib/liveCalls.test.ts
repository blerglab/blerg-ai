import { describe, expect, it } from 'vitest'
import { liveCalls, longRunningTitle, oldestLive, runningLabel, runningMs } from './liveCalls'
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
