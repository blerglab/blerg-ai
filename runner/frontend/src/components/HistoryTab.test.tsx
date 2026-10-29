import { Profiler } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { formatDuration, useTranscriptStore, type AgentEvent, type SessionTranscript } from '@blerglab/chat'
import HistoryTab, { TopicList } from './HistoryTab'
import { reelReply } from '../test/reelFixture'
import { EMPTY_HISTORY, hm, spanLabel, type TopicsReply } from '../lib/topics'
import type { TopicsView } from '../hooks/useTopics'

// HistoryTab's own reads are useTopics' (tested there): here it answers the fixture.
vi.mock('../hooks/useTopics', () => ({ useTopics: () => ({ status: 'ready', reply: reelReply(new Date(2026, 9, 15, 12, 0)) }) }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(() => Promise.resolve({ ok: true, blob: () => Promise.resolve(new Blob(['x'])) })) }))

const NOW = new Date(2026, 9, 15, 12, 0)

function hold(firstSeq: number) {
  const t: SessionTranscript = {
    events: [{ seq: firstSeq } as AgentEvent], streaming: '', lastSeq: 400, replayDone: true, hasMore: false,
    hasOlder: true, firstSeq, clockOffset: 0,
  }
  useTranscriptStore.setState({ sessions: { 'sess-a': t } })
}

function show(view: TopicsView, over: { onJump?: (k: string, go: () => boolean) => void; onOpenArtifact?: (id: string) => void; missing?: string | null } = {}) {
  const onJump = over.onJump ?? vi.fn()
  const onOpenArtifact = over.onOpenArtifact ?? vi.fn()
  render(<TopicList sessionId="sess-a" view={view} now={NOW} missing={over.missing ?? null} onJump={onJump} onOpenArtifact={onOpenArtifact} />)
  return { onJump, onOpenArtifact }
}

const ready = (reply: TopicsReply = reelReply(NOW)): TopicsView => ({ status: 'ready', reply })
const cards = () => screen.getAllByTestId('reel-card')
const expand = (i: number) => {
  const head = within(cards()[i]).getByTestId('reel-head')
  if (head.getAttribute('aria-expanded') !== 'true') fireEvent.click(head)
}

beforeEach(() => {
  useTranscriptStore.setState({ sessions: {} })
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

describe('History tab', () => {
  it('lists the marked topics newest first: a now dot on the open one, outcome, span and counts', () => {
    const reply = reelReply(NOW)
    show(ready(reply))
    const all = cards()
    expect(all.map(c => within(c).getByTestId('reel-head').querySelector('.rail-item-title')!.textContent)).toEqual(['Fix the drawer on phones', 'Build the History tab'])
    expect(within(all[0]).getByTestId('reel-now')).toBeInTheDocument()
    expect(within(all[1]).queryByTestId('reel-now')).toBeNull()
    expect(within(all[1]).getByTestId('reel-outcome')).toHaveTextContent('Shipped on main')
    expect(within(all[1]).getByTestId('reel-span')).toHaveTextContent(spanLabel(reply.topics[1], NOW))
    // The counts are glyphs, their words in the name and the tooltips.
    expect(within(all[1]).getByTestId('reel-counts')).toHaveTextContent('📄2↳3⎇3')
    expect(within(all[1]).getByTestId('reel-counts')).toHaveAttribute('aria-label', '2 files, 3 subagents, 3 commits')
    expect(within(all[0]).getByTestId('reel-counts')).toHaveAttribute('aria-label', '1 subagent')
    expect(screen.queryByTestId('reel-auto')).toBeNull()
    expect(screen.queryByText('auto')).toBeNull()
  })

  it('folds the unmarked work into one collapsed line after the cards', () => {
    show(ready())
    const line = screen.getByTestId('reel-unmarked')
    const head = within(line).getByTestId('reel-unmarked-head')
    expect(head).toHaveTextContent('Unmarked work · 5 agents · 2 files')
    expect(head).toHaveAttribute('aria-expanded', 'false')
    expect(head).not.toHaveAttribute('aria-controls')
    expect(cards()).not.toContain(line)
    expect(cards()[1].compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(line).queryByRole('list')).toBeNull()
  })

  it('opens the unmarked line onto its files (version chips) and one line per subagent; no request rows', () => {
    const { onOpenArtifact } = show(ready())
    const line = screen.getByTestId('reel-unmarked')
    fireEvent.click(within(line).getByTestId('reel-unmarked-head'))
    const head = within(line).getByTestId('reel-unmarked-head')
    expect(head).toHaveAttribute('aria-expanded', 'true')
    expect(document.getElementById(head.getAttribute('aria-controls')!)).toBe(within(line).getByRole('list'))
    const docs = within(line).getAllByTestId('reel-document')
    expect(docs.map(d => d.querySelector('.rail-item-title')!.textContent)).toEqual(['notes.md', 'drawer.png'])
    expect(docs[0].querySelector('.reel-icon')).toHaveAttribute('data-file-icon', 'markdown')
    const chips = within(docs[0]).getAllByTestId('reel-chip')
    expect(chips.map(c => c.textContent)).toEqual(['v1', 'v2'])
    expect(chips[1]).toHaveAttribute('title', 'v2, published outside a topic')
    fireEvent.click(chips[1])
    expect(onOpenArtifact).toHaveBeenCalledWith('n2')
    const agents = within(line).getAllByTestId('reel-unmarked-agent')
    expect(agents.map(a => a.querySelector('.reel-line-title')!.textContent)).toEqual([
      'Survey the repo', 'Review the sign-in guard fix', 'Reproduce the stuck refresh', 'Watch CI', 'Check the badge on phones',
    ])
    expect(agents[1]).toHaveTextContent('sonnet-5 · 18k tokens')
    expect(agents[2]).toHaveTextContent('✗')
    expect(agents[3]).toHaveTextContent('■')
    expect(within(line).queryByTestId('reel-request')).toBeNull()
    expect(within(line).queryByTestId('reel-agent')).toBeNull()
    expect(line.querySelector('[data-event-id], [data-call-id], [data-call-ids]')).toBeNull()
  })

  it('counts one agent and one file in the singular, and leaves out a zero', () => {
    const reply = reelReply(NOW)
    reply.unmarked = { ...reply.unmarked!, agents: 1, subagents: reply.unmarked!.subagents.slice(0, 1), documents: [] }
    show(ready(reply))
    expect(screen.getByTestId('reel-unmarked-head')).toHaveTextContent(/^Unmarked work · 1 agent$/)
  })

  it('draws no unmarked line when everything is marked', () => {
    show(ready({ ...reelReply(NOW), unmarked: null }))
    expect(cards()).toHaveLength(2)
    expect(screen.queryByTestId('reel-unmarked')).toBeNull()
  })

  it('says how topics are made when there are none, still with the unmarked line, and when the server is too old', () => {
    show(ready({ ...reelReply(NOW), topics: [], unmarked: null }))
    const empty = screen.getByTestId('reel-empty')
    expect(empty.textContent).toBe(EMPTY_HISTORY)
    expect(empty.querySelector('code')).toHaveTextContent('blerg-runner topic "<title>"')
    cleanup()
    show(ready({ ...reelReply(NOW), topics: [] }))
    expect(screen.getByTestId('reel-empty').textContent).toBe(EMPTY_HISTORY)
    expect(screen.getByTestId('reel-unmarked-head')).toHaveTextContent('Unmarked work · 5 agents · 2 files')
    expect(screen.queryAllByTestId('reel-card')).toHaveLength(0)
    cleanup()
    show({ status: 'no-route' })
    expect(screen.getByTestId('reel-no-route')).toHaveTextContent('History needs a newer runner server')
  })

  it('opens a card onto one compact line per kind, never the chat replayed', () => {
    show(ready())
    // A closed card controls nothing; an open one names its summary.
    expect(within(cards()[1]).getByTestId('reel-head')).not.toHaveAttribute('aria-controls')
    expand(1)
    const head = within(cards()[1]).getByTestId('reel-head')
    const summary = within(cards()[1]).getByTestId('reel-summary')
    expect(document.getElementById(head.getAttribute('aria-controls')!)).toBe(summary)
    expect(Array.from(summary.children).map(r => r.getAttribute('data-testid'))).toEqual([
      'reel-sum-messages', 'reel-sum-agents', 'reel-sum-files', 'reel-sum-commits',
    ])
    for (const raw of ['reel-request', 'reel-document', 'reel-agent', 'reel-done', 'reel-filed-card']) {
      expect(within(cards()[1]).queryAllByTestId(raw)).toHaveLength(0)
    }
    // The open topic began at the marker: no message of yours, so no messages line.
    expand(0)
    expect(within(cards()[0]).queryByTestId('reel-sum-messages')).toBeNull()
    expect(within(cards()[0]).getByTestId('reel-sum-agents')).toHaveTextContent('1 subagent')
  })

  it('counts your messages, each a time that jumps to it, its text in the tooltip', () => {
    const reply = reelReply(NOW)
    reply.topics[1].messages = [
      { seq: 100, event_id: 'ev-100', ts: reply.topics[1].started_at, text: 'Build the History tab per the spec' },
      { seq: 200, event_id: 'ev-200', ts: new Date(NOW.getTime() - 100 * 60_000).toISOString(), text: 'Also the phone layout' },
    ]
    hold(1)
    const onJump = vi.fn((_: string, go: () => boolean) => go())
    show(ready(reply), { onJump })
    expand(1)
    const line = within(cards()[1]).getByTestId('reel-sum-messages')
    expect(line).toHaveTextContent('2 messages')
    const chips = within(line).getAllByTestId('reel-sum-message')
    expect(chips.map(c => c.textContent)).toEqual([hm(reply.topics[1].started_at), hm(reply.topics[1].messages[1].ts)])
    expect(chips[1]).toHaveAttribute('title', 'Also the phone layout')
    const target = document.createElement('div')
    target.dataset.eventId = 'ev-200'
    target.scrollIntoView = vi.fn()
    document.body.appendChild(target)
    try {
      fireEvent.click(chips[1])
      expect(onJump).toHaveBeenCalledWith('msg:ev-200', expect.any(Function))
      expect(target.scrollIntoView).toHaveBeenCalled()
    } finally {
      target.remove()
    }
  })

  it('draws the subagents as one line: their count, a chip each with its status, and their tokens and time', () => {
    hold(1)
    show(ready())
    expand(1)
    const line = within(cards()[1]).getByTestId('reel-sum-agents')
    expect(line).toHaveTextContent('3 subagents')
    const chips = within(line).getAllByTestId('reel-sum-agent')
    expect(chips.map(c => c.textContent)).toEqual(['✓ Implement task 3', '✗ Review the branch', '■ Watch CI'])
    expect(chips[0]).toHaveAttribute('title', `Implement task 3 · done · sonnet-5 · 1.5k tokens · ${formatDuration(412000)}`)
    // Time on the clock: the second subagent ran within the first's span, so it adds nothing.
    expect(within(line).getByTestId('reel-sum-agent-meta')).toHaveTextContent(`1.5k tokens · ${formatDuration(412000)}`)
    expect(cards()[1].querySelector('[data-event-id], [data-call-id], [data-call-ids]')).toBeNull()
  })

  it('draws the files as chips that open the version the topic published; a gone one is struck', () => {
    const reply = reelReply(NOW)
    const { onOpenArtifact } = show(ready(reply))
    expand(1)
    const files = within(cards()[1]).getAllByTestId('reel-sum-file')
    expect(files.map(f => f.textContent)).toEqual(['plan.md v2', 'mock.html v1'])
    expect(files[0]).toHaveAttribute('href', '?artifact=a2')
    fireEvent.click(files[0])
    expect(onOpenArtifact).toHaveBeenCalledWith('a2')
    cleanup()
    const doc = reply.topics[1].items[1]
    if (doc.type !== 'document') throw new Error('fixture')
    doc.versions = doc.versions.map(v => (v.artifact_id === 'a2' ? { ...v, missing: true } : v))
    const again = show(ready(reply))
    expand(1)
    const gone = within(cards()[1]).getAllByTestId('reel-sum-file')[0]
    expect(gone.tagName).toBe('SPAN')
    expect(gone).toHaveAttribute('aria-disabled', 'true')
    expect(gone).toHaveAttribute('title', 'plan.md v2 is no longer available')
    fireEvent.click(gone)
    expect(again.onOpenArtifact).not.toHaveBeenCalled()
  })

  it('draws the commits as short chips that copy the sha, never a link out of Blerg', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    show(ready())
    expand(1)
    const line = within(cards()[1]).getByTestId('reel-sum-commits')
    const chips = within(line).getAllByTestId('reel-commit')
    expect(chips.map(c => c.textContent)).toEqual(['1a2b3c4', '9f8e7d6', '0badc0f'])
    expect(line.querySelector('a')).toBeNull()
    await act(async () => { fireEvent.click(chips[2]) })
    expect(writeText).toHaveBeenCalledWith('0badc0ffee0ddf00d')
  })

  it('draws no commits line when the topic named none', () => {
    const reply = reelReply(NOW)
    const done = reply.topics[1].items[6]
    if (done.type !== 'done') throw new Error('fixture')
    delete done.commits
    reply.topics[1].counts = { documents: 2, agents: 3 }
    show(ready(reply))
    expand(1)
    expect(within(cards()[1]).queryByTestId('reel-sum-commits')).toBeNull()
    expect(within(cards()[1]).getByTestId('reel-counts')).toHaveAttribute('aria-label', '2 files, 3 subagents')
  })

  it('jumps from a line\'s time to its first item only when the chat holds it, and follows the store when it resets', () => {
    hold(150)
    const onJump = vi.fn((_: string, go: () => boolean) => go())
    show(ready(), { onJump })
    expand(1)
    const card = cards()[1]
    // Your message (seq 100) is older than what the chat holds: a time, not a jump.
    expect(within(within(card).getByTestId('reel-sum-messages')).queryByTestId('reel-sum-jump')).toBeNull()
    const target = document.createElement('div')
    target.setAttribute('data-call-id', 'toolu_1')
    target.scrollIntoView = vi.fn()
    document.body.appendChild(target)
    try {
      fireEvent.click(within(within(card).getByTestId('reel-sum-agents')).getByTestId('reel-sum-jump'))
      expect(onJump).toHaveBeenCalledWith('sum:tp_done:agents', expect.any(Function))
      expect(target.scrollIntoView).toHaveBeenCalled()
    } finally {
      target.remove()
    }
    act(() => useTranscriptStore.getState().resetEvents('sess-a'))
    expect(within(card).queryAllByTestId('reel-sum-jump')).toHaveLength(0)
  })

  it('does not draw again while the conversation streams in below what it holds', () => {
    hold(150)
    const onRender = vi.fn()
    render(
      <Profiler id="reel" onRender={onRender}>
        <TopicList sessionId="sess-a" view={ready()} now={NOW} missing={null} onJump={vi.fn()} onOpenArtifact={vi.fn()} />
      </Profiler>,
    )
    const drawn = onRender.mock.calls.length
    act(() => {
      useTranscriptStore.getState().ingest({
        type: 'agent_event', session_id: 'sess-a', client_event_id: 'x1', seq: 401, ts: NOW.toISOString(), kind: 'assistant_text', payload: { text: 'more' },
      } as AgentEvent)
    })
    expect(useTranscriptStore.getState().sessions['sess-a'].events).toHaveLength(2)
    expect(onRender.mock.calls.length).toBe(drawn)
  })

  it('names a line whose item was not found, as the Live rows do', () => {
    hold(1)
    show(ready(), { missing: 'sum:tp_done:agents' })
    expand(1)
    const line = within(cards()[1]).getByTestId('reel-sum-agents')
    expect(within(line).getByRole('status')).toHaveTextContent('Not in the part of the conversation the chat has drawn. Scroll up in the chat to reach it.')
  })

  it('reads the spans against the time the tab came on screen', () => {
    vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'setInterval'] })
    try {
      vi.setSystemTime(NOW)
      const props = { sessionId: 'sess-a', filesKey: '', missing: null, onJump: vi.fn(), onOpenArtifact: vi.fn() }
      const { rerender } = render(<HistoryTab {...props} active={false} />)
      const span = () => within(cards()[0]).getByTestId('reel-span')
      expect(span()).toHaveTextContent('· 20 min')
      vi.setSystemTime(new Date(NOW.getTime() + 10 * 60_000))
      rerender(<HistoryTab {...props} active />)
      act(() => { vi.advanceTimersByTime(1) })
      expect(span()).toHaveTextContent('· 30 min')
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('History tab: cards an approved proposal created', () => {
  const card = { proposal_id: 'p1', id: 'c1', board_id: 'b1', board_name: 'Product', number: 42, title: 'Export button is stale', column: 'Backlog', auto: true }
  it('shows a chip in its topic that opens the in-app card modal', () => {
    const reply = reelReply(NOW)
    reply.topics[0].items.push({ type: 'card', seq: 399, event_id: 'ev-card', ts: NOW.toISOString(), card })
    reply.topics[0].counts.cards = 1
    show(ready(reply))
    expect(within(cards()[0]).getByTestId('reel-counts')).toHaveAttribute('aria-label', '1 subagent, 1 card')
    expand(0)
    const chip = within(cards()[0]).getByTestId('reel-card-chip')
    expect(chip).toHaveTextContent('Product #42 Export button is stale')
    expect(chip.tagName).toBe('BUTTON')
    expect(chip).toHaveAccessibleName(/Open card.*#42/)
    expect(chip.getAttribute('href')).toBeNull()
    expect(chip.getAttribute('title')).toContain('approved by a session permission')
  })
  it('lists a card filed outside every topic under Unmarked work', () => {
    const reply = reelReply(NOW)
    reply.unmarked = { ...reply.unmarked!, cards: [{ type: 'card', seq: 5, event_id: 'ev-c', ts: NOW.toISOString(), card: { ...card, auto: false } }] }
    show(ready(reply))
    const line = screen.getByTestId('reel-unmarked')
    expect(within(line).getByTestId('reel-unmarked-head')).toHaveTextContent('1 card')
    fireEvent.click(within(line).getByTestId('reel-unmarked-head'))
    expect(within(line).getByTestId('reel-filed-card')).toHaveTextContent('#42')
  })
})

describe('History tab: renames', () => {
  it('shows who renamed the session, from what to what, in its topic', () => {
    const reply = reelReply(NOW)
    reply.topics[0].items.push({ type: 'rename', seq: 399, event_id: 'ev-rn', ts: NOW.toISOString(), rename: { from: 'Worker', to: 'Fix the login flow', by: 'parent' } })
    show(ready(reply))
    expand(0)
    const row = within(cards()[0]).getByTestId('reel-sum-renames')
    expect(row).toHaveTextContent('Renamed by the parent: Worker → Fix the login flow')
  })
  it('lists a rename outside every topic under Unmarked work', () => {
    const reply = reelReply(NOW)
    reply.unmarked = { ...reply.unmarked!, renames: [{ type: 'rename', seq: 5, event_id: 'ev-r', ts: NOW.toISOString(), rename: { from: '', to: 'Review the billing PR', by: 'agent' } }] }
    show(ready(reply))
    const line = screen.getByTestId('reel-unmarked')
    expect(within(line).getByTestId('reel-unmarked-head')).toHaveTextContent('1 rename')
    fireEvent.click(within(line).getByTestId('reel-unmarked-head'))
    expect(within(line).getByTestId('reel-rename')).toHaveTextContent('Renamed by the agent: Untitled → Review the billing PR')
  })
})

describe('bounded topic history (#215)', () => {
  it('opens the current topic, collapses completed topics and shows their stroke counts', () => {
    const reply = reelReply(NOW)
    reply.strokes = reply.topics.flatMap(t => Array.from({ length: 1000 }, (_, i) => ({ id: `${t.id}-${i}`, kind: 'finding' as const, source: 'mark' as const, text: `Finding ${i}`, topic_id: t.id, seq: 1000-i, at: NOW.toISOString(), jump: null })))
    show(ready(reply))
    const [current, done] = cards()
    expect(within(current).getByTestId('reel-head')).toHaveAttribute('aria-expanded', 'true')
    expect(within(done).getByTestId('reel-head')).toHaveAttribute('aria-expanded', 'false')
    expect(within(done).queryAllByTestId('stroke')).toHaveLength(0)
    expect(within(done).getByText('1000 strokes')).toBeTruthy()
    expect(within(current).getAllByTestId('stroke').length).toBeLessThanOrEqual(100)
    for (let i = 0; i < 9; i++) fireEvent.click(within(current).getByRole('button', { name: /Show earlier strokes/ }))
    expect(within(current).getAllByTestId('stroke').length).toBeLessThanOrEqual(100)
    expect(within(current).getByText('Finding 999')).toBeTruthy()
    fireEvent.click(within(current).getByRole('button', { name: /Show newer strokes/ }))
    expect(within(current).getByText('Finding 800')).toBeTruthy()
  })
})


it('keeps a thousand completed topics in bounded, reversible windows', () => {
  const reply = reelReply(NOW)
  reply.topics = Array.from({ length: 1001 }, (_, i) => ({ ...reply.topics[1], id: `topic-${i}`, title: `Topic ${i}`, items: [] }))
  reply.strokes = []
  show(ready(reply))
  expect(cards()).toHaveLength(100)
  for (let i = 0; i < 10; i++) fireEvent.click(screen.getByRole('button', { name: /Show earlier entries/ }))
  expect(cards()).toHaveLength(1)
  expect(cards()[0]).toHaveTextContent('Topic 1000')
  fireEvent.click(screen.getByRole('button', { name: /Show newer entries/ }))
  expect(cards()).toHaveLength(100)
  expect(cards()[0]).toHaveTextContent('Topic 900')
})

it('bounds the strokes inside an expanded fold as well as inside a topic', () => {
  const reply = reelReply(NOW)
  const items = Array.from({ length: 2000 }, (_, i) => ({ id: `fold-${i}`, kind: 'attention' as const, source: 'ask' as const, text: `Approval ${i}`, state: 'resolved' as const, seq: i, at: NOW.toISOString(), jump: null }))
  reply.strokes = [{ ...items[0], id: 'fold', text: '2000 approvals', items, topic_id: 'tp_open' }]
  show(ready(reply))
  fireEvent.click(screen.getByTestId('stroke-text'))
  const fold = screen.getByTestId('stroke-fold-items')
  expect(within(fold).getAllByTestId('stroke-item')).toHaveLength(100)
  fireEvent.click(within(fold).getByRole('button', { name: /Show earlier strokes/ }))
  expect(within(fold).getAllByTestId('stroke-item')).toHaveLength(100)
  expect(within(fold).getByText('Approval 100')).toBeTruthy()
})
