import { beforeEach, describe, expect, it } from 'vitest'
import { makeSession } from '../test/fixtures'
import { offerSession, recentSessionIds, recentSessionOrder, rememberSessionVisit } from './recentSessions'

beforeEach(() => localStorage.clear())

describe('recent sessions', () => {
  it('keeps visits per account, most recent first, without duplicates', () => {
    rememberSessionVisit('alice', 'a')
    rememberSessionVisit('alice', 'b')
    rememberSessionVisit('alice', 'a')
    rememberSessionVisit('bob', 'c')
    expect(recentSessionIds('alice')).toEqual(['a', 'b'])
    expect(recentSessionIds('bob')).toEqual(['c'])
    expect(recentSessionIds('')).toEqual([])
  })
  it('caps the list and survives unreadable storage', () => {
    for (let i = 0; i < 60; i++) rememberSessionVisit('alice', `s${i}`)
    expect(recentSessionIds('alice')).toHaveLength(50)
    expect(recentSessionIds('alice')[0]).toBe('s59')
    localStorage.setItem('blerg-runner.recent-sessions.alice', '{not json')
    expect(recentSessionIds('alice')).toEqual([])
  })
  it('never offers crons, ended, stopped, failed, archived or the current session', () => {
    expect(offerSession(makeSession({ id: 'x', status: 'idle' }), 'cur')).toBe(true)
    expect(offerSession(makeSession({ id: 'x', status: 'disconnected' }), 'cur')).toBe(true)
    for (const over of [{ cron_id: 'c1' }, { started_by: { kind: 'cron' } }, { status: 'ended' as const }, { status: 'stopped' as const }, { status: 'error' as const }, { archived_at: '2026-10-01T00:00:00Z' }, { id: 'cur' }]) {
      expect(offerSession(makeSession({ id: 'x', status: 'running', ...over }), 'cur')).toBe(false)
    }
  })
  it('orders opened sessions by visit, open before unreachable, and leaves out never-opened ones', () => {
    const sessions = [
      makeSession({ id: 'fresh', status: 'running', started_at: '2026-10-26T12:00:00Z' }),
      makeSession({ id: 'first', status: 'idle' }),
      makeSession({ id: 'second', status: 'running' }),
      makeSession({ id: 'gone', status: 'disconnected' }),
      makeSession({ id: 'cron', status: 'running', cron_id: 'c1' }),
      makeSession({ id: 'done', status: 'ended' }),
    ]
    expect(recentSessionOrder(sessions, ['gone', 'cron', 'done', 'second', 'first'])).toEqual(['second', 'first', 'gone'])
    expect(recentSessionOrder(sessions, ['gone', 'second', 'first'], 'second')).toEqual(['first', 'gone'])
  })
  it('falls back to open sessions by latest activity until one was opened', () => {
    const sessions = [
      makeSession({ id: 'old', status: 'running', started_at: '2026-10-01T00:00:00Z' }),
      makeSession({ id: 'new', status: 'idle', started_at: '2026-10-20T00:00:00Z' }),
      makeSession({ id: 'cron', status: 'running', cron_id: 'c1', started_at: '2026-10-26T00:00:00Z' }),
      makeSession({ id: 'done', status: 'ended', started_at: '2026-10-26T00:00:00Z' }),
      makeSession({ id: 'tool', status: 'running', started_by: { kind: 'agent', name: 'ci' }, started_at: '2026-10-26T00:00:00Z' }),
    ]
    expect(recentSessionOrder(sessions, [])).toEqual(['new', 'old'])
    // A session a tool started is offered once the person opened it.
    expect(recentSessionOrder(sessions, ['tool'])).toEqual(['tool'])
    // Only the current or no-longer-offered sessions were opened: still the fallback.
    expect(recentSessionOrder(sessions, ['done', 'cur'], 'cur')).toEqual(['new', 'old'])
  })
})
