import { describe, expect, it } from 'vitest'
import type { AgentEvent, SessionTranscript } from '@blerglab/chat'
import { EMPTY_HISTORY, countsLabel, hm, isHeld, shownVersion, spanLabel, strokeWords, unmarkedLabel, usageTotal, type TopicItem } from './topics'

const at = (h: number, m: number, day = 15) => new Date(2026, 9, day, h, m)

describe('spanLabel', () => {
  it('reads "10:42–11:30 · 48 min" for a topic of today', () => {
    const now = at(12, 0)
    expect(spanLabel({ started_at: at(10, 42).toISOString(), ended_at: at(11, 30).toISOString() }, now)).toBe('10:42–11:30 · 48 min')
  })
  it('runs to "now" while the topic is open', () => {
    expect(spanLabel({ started_at: at(11, 42).toISOString(), ended_at: null }, at(12, 0))).toBe('11:42–now · 18 min')
  })
  it('names the day when it was not today', () => {
    const start = at(9, 5, 13)
    const label = spanLabel({ started_at: start.toISOString(), ended_at: at(9, 50, 13).toISOString() }, at(12, 0))
    expect(label).toBe(`${start.toLocaleDateString([], { month: 'short', day: 'numeric' })}, 09:05–09:50 · 45 min`)
  })
  it('is empty for a time it cannot read', () => {
    expect(spanLabel({ started_at: 'nope', ended_at: null }, at(12, 0))).toBe('')
  })
})

describe('countsLabel', () => {
  it('never shows a zero', () => {
    expect(countsLabel({ documents: 2, agents: 3 })).toBe('2 docs · 3 agents')
    expect(countsLabel({ documents: 1 })).toBe('1 doc')
    expect(countsLabel({ agents: 1, documents: 0 })).toBe('1 agent')
    expect(countsLabel({})).toBe('')
  })
  it('adds the commits after the agents, and hides a zero', () => {
    expect(countsLabel({ documents: 2, agents: 3, commits: 3 })).toBe('2 docs · 3 agents · 3 commits')
    expect(countsLabel({ commits: 1 })).toBe('1 commit')
    expect(countsLabel({ agents: 1, commits: 0 })).toBe('1 agent')
  })
})

describe('unmarkedLabel', () => {
  const u = (agents: number, files: number) => ({ agents, documents: Array(files).fill(null), subagents: [], first_seq: 1, last_seq: 2, started_at: '', ended_at: '' })
  it('reads "Unmarked work · 42 agents · 8 files", singular for one, a zero left out', () => {
    expect(unmarkedLabel(u(42, 8))).toBe('Unmarked work · 42 agents · 8 files')
    expect(unmarkedLabel(u(1, 1))).toBe('Unmarked work · 1 agent · 1 file')
    expect(unmarkedLabel(u(0, 3))).toBe('Unmarked work · 3 files')
    expect(unmarkedLabel(u(2, 0))).toBe('Unmarked work · 2 agents')
  })
  it('the empty state names the marker command', () => {
    expect(EMPTY_HISTORY).toBe('No topics yet. The agent marks a topic when it starts a piece of work: blerg-runner topic "<title>", and a decision, milestone or finding with blerg-runner mark decision "<text>".')
  })
})

describe('isHeld', () => {
  const ev = (seq: number) => ({ seq } as AgentEvent)
  const t = (over: Partial<SessionTranscript>): SessionTranscript => ({
    events: [ev(150), ev(151)], streaming: '', lastSeq: 151, replayDone: true, hasMore: false, hasOlder: true, firstSeq: 150, clockOffset: 0, ...over,
  })
  it('holds a row at or after the oldest event loaded', () => {
    expect(isHeld(t({}), 150)).toBe(true)
    expect(isHeld(t({}), 149)).toBe(false)
  })
  it('holds everything once nothing older is left', () => {
    expect(isHeld(t({ hasOlder: false }), 3)).toBe(true)
  })
  it('holds nothing before the transcript is loaded, or after it was reset', () => {
    expect(isHeld(undefined, 200)).toBe(false)
    expect(isHeld(t({ events: [] }), 200)).toBe(false)
  })
})

describe('shownVersion and usageTotal', () => {
  const doc = (versions: Array<[number, boolean, boolean]>): Extract<TopicItem, { type: 'document' }> => ({
    type: 'document', seq: 1, event_id: 'e', ts: '', name: 'plan.md', view: 'markdown', mock: false, content_type: 'text/markdown', size: 1,
    latest_version: versions.length, versions_total: versions.length,
    versions: versions.map(([v, inTopic, deleted]) => ({ version: v, artifact_id: `a${v}`, created_at: '', in_topic: inTopic, deleted })),
  })
  it('shows the newest version the topic published that still exists', () => {
    expect(shownVersion(doc([[1, true, false], [2, true, false], [3, false, false]]))?.artifact_id).toBe('a2')
    expect(shownVersion(doc([[1, true, false], [2, true, true]]))?.artifact_id).toBe('a1')
    expect(shownVersion(doc([[1, true, true]]))?.artifact_id).toBe('a1')
  })
  it('adds every kind of token', () => {
    expect(usageTotal({ input_tokens: 1, output_tokens: 2, cache_read_tokens: 3, cache_write_tokens: 4 })).toBe(10)
    expect(usageTotal(null)).toBe(0)
  })
  it('hm is hours and minutes, padded', () => {
    expect(hm(at(9, 5))).toBe('09:05')
  })
})

describe('dismissed strokes (card #155)', () => {
  it('say the person dismissed it', () => {
    expect(strokeWords({ kind: 'attention', by: 'person', state: 'expired', source: 'review' })).toBe('Asked of you · dismissed by you')
    expect(strokeWords({ kind: 'attention', state: 'expired', source: 'ask' })).toBe('Asked of you · closed')
  })
})
