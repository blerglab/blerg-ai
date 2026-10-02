import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import {
  formatArguments, collapsedArguments, prettyJson, resultText, isAwaitingYou, approveFailureMessage,
  usePendingProposals, fetchPendingCount, useProposalCountPoll, beginCountRequest, applyPendingCount, PROPOSALS_POLL_MS,
} from './proposals'
import type { ProposalInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const base: ProposalInfo = {
  id: 'p1', state: 'pending', connection_id: 'c1', connection_name: 'calendar', tool: 'send', arguments: {},
  agent_summary: '', session_id: null, cron_id: null, created_at: '', expires_at: '', decided_at: null, decided_by: null, result: null,
}

describe('formatArguments', () => {
  it('pretty-prints the stored text, keeping markup as text', () => {
    expect(formatArguments({ arguments_raw: '{"to":"a","body":"<b>hi</b>"}' })).toBe('{\n  "to": "a",\n  "body": "<b>hi</b>"\n}')
  })
  it('shows an empty object as {}', () => {
    expect(formatArguments({ arguments_raw: '{}' })).toBe('{}')
    expect(formatArguments({ arguments: {} })).toBe('{}')
  })
  it('keeps number lexemes and key order of the stored text', () => {
    const raw = '{"b": 12345678901234567890, "a": 1.10, "c": [1e5, -0.0, {"z": 1, "y": null}]}'
    expect(formatArguments({ arguments_raw: raw })).toBe(
      '{\n  "b": 12345678901234567890,\n  "a": 1.10,\n  "c": [\n    1e5,\n    -0.0,\n    {\n      "z": 1,\n      "y": null\n    }\n  ]\n}',
    )
  })
  it('falls back to the raw text when it cannot be formatted', () => {
    expect(formatArguments({ arguments_raw: '{"a": 01}' })).toBe('{"a": 01}')
    expect(formatArguments({ arguments_raw: '{"a": 1} trailing' })).toBe('{"a": 1} trailing')
  })
  it('falls back to the object from an older server', () => {
    expect(formatArguments({ arguments: { to: 'a' } })).toBe('{\n  "to": "a"\n}')
  })
})

describe('prettyJson', () => {
  it('rejects invalid JSON', () => {
    for (const bad of ['', '{', '{"a"}', '[1,]', '{"a":1,}', 'nul', '"abc', '{a:1}']) expect(prettyJson(bad)).toBeNull()
  })
  it('accepts scalars and nested empties', () => {
    expect(prettyJson('"x"')?.text).toBe('"x"')
    expect(prettyJson('{"a": [], "b": {}}')?.text).toBe('{\n  "a": [],\n  "b": {}\n}')
  })
})

describe('collapsedArguments', () => {
  it('cuts long strings and says so; the full text is formatArguments', () => {
    const long = 'x'.repeat(2000)
    const raw = `{"text": "${long}", "n": 12345678901234567890}`
    const c = collapsedArguments({ arguments_raw: raw })
    expect(c.truncated).toBe(true)
    expect(c.text.length).toBeLessThan(700)
    expect(c.text).toContain('12345678901234567890')
    expect(formatArguments({ arguments_raw: raw })).toContain(long)
  })
  it('is not truncated for short values', () => {
    expect(collapsedArguments({ arguments_raw: '{"a":"b"}' }).truncated).toBe(false)
  })
})

describe('resultText', () => {
  it('joins text parts of a result and reports tool errors', () => {
    expect(resultText({ content: [{ type: 'text', text: 'one' }, { type: 'text', text: 'two' }] })).toEqual({ text: 'one\ntwo', isError: false })
    expect(resultText({ content: [{ type: 'text', text: 'bad' }], isError: true })).toEqual({ text: 'bad', isError: true })
  })
  it('reads an error result', () => {
    expect(resultText({ error: 'boom' })).toEqual({ text: 'boom', isError: true })
  })
  it('returns null when there is nothing to show', () => {
    expect(resultText(null)).toBeNull()
    expect(resultText({ content: [] })).toBeNull()
  })
})

describe('isAwaitingYou', () => {
  it('is true for pending, executing and unknown only', () => {
    expect((['pending', 'executing', 'unknown'] as const).every(s => isAwaitingYou({ ...base, state: s }))).toBe(true)
    expect((['done', 'failed', 'rejected', 'expired'] as const).some(s => isAwaitingYou({ ...base, state: s }))).toBe(false)
  })
})

describe('approveFailureMessage', () => {
  it('explains an expired proposal', () => {
    expect(approveFailureMessage(409, 'this proposal has expired (it is older than 7 days) and can no longer be approved: let the agent propose again'))
      .toMatch(/not run.*expired.*propose it again/i)
  })
  it('explains a changed tool or address and what to do', () => {
    const t = approveFailureMessage(409, "the tool's definition changed (or the server no longer offers it) since the agent proposed this, so it was not run")
    expect(t).toMatch(/changed/i)
    expect(t).toMatch(/reject this proposal and ask the agent to propose again/i)
    expect(approveFailureMessage(409, "the connection's address changed since the agent proposed this, so it was not run: reject")).toMatch(/address or the tool's definition changed/i)
  })
  it('explains an already decided or running proposal', () => {
    expect(approveFailureMessage(409, 'this proposal is no longer pending (done)')).toMatch(/already decided or is running/i)
    expect(approveFailureMessage(409, 'this proposal was already decided by another request, or has expired')).toMatch(/already decided/i)
    expect(approveFailureMessage(409, 'this proposal is no longer pending (expired)')).not.toMatch(/ask the agent to propose it again/i)
  })
  it('explains a connection that is gone', () => {
    expect(approveFailureMessage(409, 'the connection no longer exists, so this proposal can no longer be run')).toMatch(/no longer exists.*never run/i)
  })
  it('keeps the server words for other 409s', () => {
    expect(approveFailureMessage(409, 'the connection needs attention (auth)')).toMatch(/still pending.*needs attention/i)
  })
  it('says nothing was sent on 502 and that it is back in pending', () => {
    expect(approveFailureMessage(502, 'the server is unreachable: nothing was sent')).toMatch(/nothing was sent.*back in your pending list.*try again/i)
  })
  it('handles sign-in, gateway, and other statuses', () => {
    expect(approveFailureMessage(401, 'x')).toMatch(/sign in again/i)
    expect(approveFailureMessage(503, 'x')).toMatch(/not configured/i)
    expect(approveFailureMessage(500, 'oops')).toContain('oops')
  })
})

describe('pending count', () => {
  beforeEach(() => {
    apiFetch.mockReset()
    usePendingProposals.setState({ count: 0 })
  })
  afterEach(() => { vi.useRealTimers() })

  it('reads the cheap count route, not the list', async () => {
    apiFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve({ pending_count: 4 }) })
    await fetchPendingCount()
    expect(apiFetch).toHaveBeenCalledWith('/api/proposals/count')
    expect(usePendingProposals.getState().count).toBe(4)
  })

  it('a slow older response never overwrites a newer one', async () => {
    let slow: (v: unknown) => void = () => {}
    apiFetch.mockImplementationOnce(() => new Promise(r => { slow = r }))
    apiFetch.mockImplementationOnce(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ pending_count: 7 }) }))
    const older = fetchPendingCount()
    await fetchPendingCount()
    expect(usePendingProposals.getState().count).toBe(7)
    slow({ ok: true, json: () => Promise.resolve({ pending_count: 2 }) })
    await older
    expect(usePendingProposals.getState().count).toBe(7)
  })

  it('applyPendingCount refuses an older ticket', () => {
    const a = beginCountRequest()
    const b = beginCountRequest()
    expect(applyPendingCount(b, 5)).toBe(true)
    expect(applyPendingCount(a, 1)).toBe(false)
    expect(usePendingProposals.getState().count).toBe(5)
  })

  it('does not poll while the tab is hidden, and refreshes when it becomes visible', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    apiFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve({ pending_count: 1 }) })
    let hidden = true
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
    try {
      const { unmount } = renderHook(() => useProposalCountPoll())
      await vi.advanceTimersByTimeAsync(PROPOSALS_POLL_MS * 3)
      expect(apiFetch).not.toHaveBeenCalled()
      hidden = false
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(0)
      expect(apiFetch).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(PROPOSALS_POLL_MS)
      expect(apiFetch).toHaveBeenCalledTimes(2)
      expect(apiFetch.mock.calls.every(c => c[0] === '/api/proposals/count')).toBe(true)
      unmount()
    } finally {
      Reflect.deleteProperty(document, 'hidden')
    }
  })
})
