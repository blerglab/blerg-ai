// History's feed of strokes (TopicList over a reply with strokes): one line each, newest first, in
// their topics; glyphs with their words in the tooltip and for a screen reader; the filters; the jump;
// hostile text drawn as text; the open topic's checklist progress.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { useTranscriptStore, type AgentEvent, type SessionTranscript } from '@blerglab/chat'
import { TopicList } from './HistoryTab'
import { ownerReply, strokesReply } from '../test/reelFixture'
import { feedEntries, hm, isTrigger, wallClockMs, type StrokeKind, type TopicsReply } from '../lib/topics'
import type { TopicsView } from '../hooks/useTopics'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(() => Promise.resolve({ ok: true, blob: () => Promise.resolve(new Blob(['x'])) })) }))

const NOW = new Date(2026, 9, 15, 12, 0)

function hold(firstSeq: number) {
  const t: SessionTranscript = {
    events: [{ seq: firstSeq } as AgentEvent], streaming: '', lastSeq: 400, replayDone: true, hasMore: false,
    hasOlder: true, firstSeq, clockOffset: 0,
  }
  useTranscriptStore.setState({ sessions: { 'sess-a': t } })
}

function show(reply: TopicsReply = strokesReply(NOW), over: { onJump?: (k: string, go: () => boolean) => void; progress?: { done: number; total: number } | null; missing?: string } = {}) {
  const onJump = over.onJump ?? vi.fn()
  const view: TopicsView = { status: 'ready', reply }
  render(<TopicList sessionId="sess-a" view={view} now={NOW} missing={over.missing ?? null} onJump={onJump} onOpenArtifact={vi.fn()} progress={over.progress ?? null} />)
  return { onJump }
}

const lines = () => screen.getAllByTestId('stroke')
const texts = () => lines().map(l => within(l).getByTestId('stroke-text').firstChild!.textContent)

beforeEach(() => {
  useTranscriptStore.setState({ sessions: {} })
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

describe('History feed', () => {
  it('draws one line per stroke, newest first, under its topic, and the strokes no topic holds between them', () => {
    show()
    const [open, done] = screen.getAllByTestId('reel-card')
    // The waiting question is pinned above the feed: in its place, a marker pointing up.
    expect(within(open).getAllByTestId('stroke').map(l => l.dataset.kind)).toEqual(['finding', 'decision'])
    expect(within(open).getByTestId('stroke-pinned-marker')).toHaveAttribute('title', 'Which drawer width should the phone use? (waiting: pinned above)')
    expect(within(done).getAllByTestId('stroke').map(l => l.dataset.kind)).toEqual(['milestone', 'attention', 'attention', 'decision'])
    const runs = screen.getAllByTestId('stroke-run')
    expect(runs).toHaveLength(2)
    // The person's decision outside every topic sits between the open topic and the finished one.
    expect(open.compareDocumentPosition(runs[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(runs[0].compareDocumentPosition(done) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // The back-dated release is the oldest: after the finished topic.
    expect(within(runs[1]).getByTestId('stroke-text')).toHaveTextContent('v1.2.0 released')
    expect(texts()).toHaveLength(8)
  })

  it('says what each glyph means in its tooltip and to a screen reader; the agent’s own decision is hollow', () => {
    show()
    const byText = (t: string) => lines().find(l => l.textContent!.includes(t))!
    const mine = byText('Ship the feed first')
    expect(mine.querySelector('.stroke-glyph')).toHaveTextContent('◆')
    expect(mine.querySelector('.stroke-glyph')).toHaveAttribute('title', 'Your decision')
    expect(mine.querySelector('.rail-sr')).toHaveTextContent('Your decision:')
    const agents = byText('Use the existing sheet')
    expect(agents.querySelector('.stroke-glyph')).toHaveTextContent('◇')
    expect(agents.querySelector('.stroke-glyph')).toHaveAttribute('title', 'Decision (agent): its own; you can overrule it')
    expect(agents).toHaveClass('by-agent')
    const waiting = screen.getAllByTestId('stroke-pin').find(l => l.textContent!.includes('Which drawer width'))!
    expect(waiting.querySelector('.stroke-glyph')).toHaveTextContent('?')
    expect(waiting).toHaveClass('is-waiting')
    expect(waiting.querySelector('.stroke-glyph')).toHaveAttribute('title', 'Asked of you · waiting')
    const approval = byText('Start a Codex session')
    expect(approval).toHaveTextContent('→ Allowed once')
    expect(byText('The drawer covers').querySelector('.stroke-glyph')).toHaveTextContent('⚠')
    expect(byText('Shipped on main').querySelector('.stroke-glyph')).toHaveTextContent('✓')
    expect(within(byText('Shipped on main')).getByTestId('reel-commit')).toHaveTextContent('9f8e7d6')
  })

  it('opens a card chip on its Board page in Blerg when the board is known, and shows only the number otherwise', () => {
    show()
    const chips = screen.getAllByTestId('stroke-card')
    const linked = chips.find(c => c.tagName === 'A')!
    expect(linked).toHaveTextContent('#42')
    expect(linked.getAttribute('href')).toMatch(/\/boards\/b1\/cards\/42$/)
    const bare = chips.find(c => c.tagName !== 'A')!
    expect(bare).toHaveTextContent('#115')
    expect(bare).toHaveAttribute('title', 'Card #115 · board unknown')
  })

  it('draws hostile text as text', () => {
    show()
    const line = lines().find(l => l.textContent!.includes('onerror'))!
    expect(within(line).getByTestId('stroke-text').firstChild!.textContent).toBe('<img src=x onerror="alert(1)"> <b>bold</b> javascript:alert(2)')
    expect(document.querySelector('img[src="x"]')).toBeNull()
    expect(line.querySelector('b')).toBeNull()
    expect(line.querySelector('a')).toBeNull()
  })

  it('filters by kind: the pressed chips show only theirs, a topic with none of them goes, and none pressed shows all', () => {
    show()
    const chip = (k: StrokeKind) => screen.getByTestId(`stroke-filter-${k}`)
    expect(chip('decision')).toHaveAttribute('aria-label', 'Decisions, 3')
    expect(chip('decision')).toHaveAttribute('title', 'Decisions')
    expect(chip('attention')).toHaveAttribute('aria-label', 'Attention, 3')
    expect(chip('milestone')).toHaveAttribute('aria-label', 'Milestones, 2')
    expect(chip('finding')).toHaveAttribute('aria-label', 'Findings, 1')
    expect(chip('decision')).toHaveTextContent('◆3')
    fireEvent.click(chip('finding'))
    expect(chip('finding')).toHaveAttribute('aria-pressed', 'true')
    expect(lines().map(l => l.dataset.kind)).toEqual(['finding'])
    expect(screen.getAllByTestId('reel-card')).toHaveLength(1)
    expect(screen.queryByTestId('reel-unmarked')).toBeNull()
    fireEvent.click(chip('milestone'))
    expect(lines().map(l => l.dataset.kind)).toEqual(['finding', 'milestone', 'milestone'])
    fireEvent.click(chip('finding'))
    fireEvent.click(chip('milestone'))
    expect(lines()).toHaveLength(8)
    expect(screen.getByTestId('reel-unmarked')).toBeInTheDocument()
  })

  it('jumps to the stroke’s moment: its event’s row, else its proposal’s card; a row older than what the chat holds shows only its time', () => {
    hold(150)
    const { onJump } = show()
    const target = document.createElement('div')
    target.dataset.eventId = 'ev-325'
    target.scrollIntoView = vi.fn()
    document.body.appendChild(target)
    const proposal = document.createElement('div')
    proposal.dataset.proposalId = 'p1'
    proposal.scrollIntoView = vi.fn()
    document.body.appendChild(proposal)
    try {
      const line = (t: string) => lines().find(l => l.textContent!.includes(t))!
      fireEvent.click(within(line('Use the existing sheet')).getByTestId('stroke-jump'))
      expect(onJump).toHaveBeenCalledWith('stroke:st-d2', expect.any(Function))
      const go = vi.mocked(onJump).mock.calls[0][1]
      expect(go()).toBe(true)
      expect(target.scrollIntoView).toHaveBeenCalled()
      // The proposal's own row is not drawn: its card is found by its id.
      fireEvent.click(within(line('File a board card')).getByTestId('stroke-jump'))
      expect(vi.mocked(onJump).mock.calls[1][1]()).toBe(true)
      expect(proposal.scrollIntoView).toHaveBeenCalled()
      // Held from seq 150: the decision at seq 120 and the back-dated release (anchored at seq 1) are not reachable.
      expect(within(line('Ship the feed first')).queryByTestId('stroke-jump')).toBeNull()
      expect(within(line('Ship the feed first')).getByTestId('stroke-time')).toHaveAttribute('title', 'Earlier than what is loaded')
      expect(within(line('v1.2.0 released')).queryByTestId('stroke-jump')).toBeNull()
      // A row with nothing drawn says so (the panel's onJump shows the note when go() is false).
      fireEvent.click(within(line('The drawer covers')).getByTestId('stroke-jump'))
      expect(vi.mocked(onJump).mock.calls[2][1]()).toBe(false)
    } finally {
      target.remove()
      proposal.remove()
    }
  })

  it('shows the open topic’s checklist progress instead of its duration; a finished topic keeps its span', () => {
    show(strokesReply(NOW), { progress: { done: 7, total: 17 } })
    const [open, done] = screen.getAllByTestId('reel-card')
    expect(within(open).getByTestId('reel-progress')).toHaveTextContent('7 of 17')
    expect(within(open).getByTestId('reel-progress')).toHaveAttribute('title', 'Checklist: 7 of 17 done')
    expect(within(open).queryByTestId('reel-span')).toBeNull()
    expect(within(done).getByTestId('reel-span')).toBeInTheDocument()
    expect(within(done).queryByTestId('reel-progress')).toBeNull()
  })

  it('without strokes (an older server) draws no filters and the topics as before', () => {
    const reply = strokesReply(NOW)
    delete reply.strokes
    show(reply)
    expect(screen.queryByTestId('stroke-filters')).toBeNull()
    expect(screen.getAllByTestId('reel-card')).toHaveLength(2)
  })
})

describe('feedEntries', () => {
  it('puts an open topic first and each run of loose strokes where it falls between the topics', () => {
    const r = strokesReply(NOW)
    const e = feedEntries(r.topics, r.strokes!, new Set())
    expect(e.map(x => (x.type === 'topic' ? x.topic.id : `loose:${x.strokes.map(s => s.id).join(',')}`))).toEqual([
      'tp_open', 'loose:st-x', 'tp_done', 'loose:st-old',
    ])
  })
})

describe('History feed: the agent\'s account, approvals, a long feed', () => {
  it('draws a ruling the agent recorded as yours as the agent\'s line, saying so', () => {
    const reply = strokesReply(NOW)
    reply.strokes!.push({ id: 'st-rec', kind: 'decision', source: 'mark', by: 'agent', recorded_as: 'person', text: 'Ship on Friday', at: new Date(NOW.getTime() - 3 * 60_000).toISOString(), seq: 336, jump: null })
    show(reply)
    const line = lines().find(l => l.textContent!.includes('Ship on Friday'))!
    expect(line.querySelector('.stroke-glyph')).toHaveTextContent('◇')
    expect(line.querySelector('.stroke-glyph')).toHaveAttribute('title', 'Decision (agent): your ruling, as the agent recorded it')
    expect(line).toHaveClass('by-agent')
  })

  it('draws a run of command approvals as one line', () => {
    const reply = strokesReply(NOW)
    const member = (i: number, state: 'open' | 'resolved') => ({ id: `ap${i}`, kind: 'attention' as const, source: 'ask' as const, text: 'Run make deploy?', state,
      at: new Date(NOW.getTime() - i * 1000).toISOString(), seq: 339 - i, topic_id: 'tp_open', jump: { event_id: `ev-ap${i}`, seq: 339 - i } })
    reply.strokes!.unshift({ id: 'st-ap', kind: 'attention', source: 'approvals', text: '3 command approvals', state: 'open', outcome: '1 waiting, 2 answered', count: 3,
      at: NOW.toISOString(), seq: 339, topic_id: 'tp_open', jump: { event_id: 'ev-ap0', seq: 339 }, items: [member(0, 'open'), member(1, 'resolved'), member(2, 'resolved')] })
    show(reply)
    const line = lines().find(l => l.textContent!.includes('command approvals'))!
    expect(line).toHaveTextContent('3 command approvals → 1 waiting, 2 answered')
    expect(line.querySelector('.stroke-glyph')).toHaveAttribute('title', 'Command approvals · waiting')
    // The waiting approval itself is pinned, not the fold.
    const pins = screen.getAllByTestId('stroke-pin')
    expect(pins.map(p => within(p).getByTestId('stroke-text').textContent)).toEqual(['Run make deploy?', 'Which drawer width should the phone use?'])
  })

  it('draws the newest lines first and more on "Show earlier"; the server\'s cut is said at the end', () => {
    const reply = strokesReply(NOW)
    const many = Array.from({ length: 250 }, (_, i) => ({
      id: `m${i}`, kind: 'finding' as const, source: 'mark' as const, by: 'agent' as const, text: `finding ${i}`,
      at: new Date(NOW.getTime() - (i + 1) * 1000).toISOString(), seq: 1000 - i, topic_id: 'tp_open', jump: null,
    }))
    reply.strokes = [...many, ...reply.strokes!]
    reply.strokes_truncated = 40
    show(reply)
    expect(lines()).toHaveLength(100)
    expect(screen.getByTestId('stroke-earlier')).toHaveTextContent('Show earlier (159)')
    expect(screen.queryByTestId('reel-unmarked')).toBeNull()
    // The finished topic is older than every line drawn: it waits with the earlier lines.
    expect(screen.getAllByTestId('reel-card')).toHaveLength(1)
    fireEvent.click(screen.getByTestId('stroke-earlier'))
    expect(lines()).toHaveLength(200)
    fireEvent.click(screen.getByTestId('stroke-earlier'))
    expect(lines()).toHaveLength(258)
    expect(screen.queryByTestId('stroke-earlier')).toBeNull()
    expect(screen.getByTestId('stroke-truncated')).toHaveTextContent('40 older lines are not loaded.')
    expect(screen.getAllByTestId('reel-card')).toHaveLength(2)
  })
})

describe('isTrigger', () => {
  it('reads the feed again on its events, an answer and a review round, never on an ordinary message', () => {
    expect(isTrigger({ kind: 'mark', payload: {} })).toBe(true)
    expect(isTrigger({ kind: 'user_message', payload: { text: 'yes', source: 'ask_answer' } })).toBe(true)
    expect(isTrigger({ kind: 'user_message', payload: { text: 'Review of plan.md (v2): 3 requests.\n\n1. x', source: 'chat' } })).toBe(true)
    expect(isTrigger({ kind: 'user_message', payload: { text: 'how is it going?', source: 'chat' } })).toBe(false)
    expect(isTrigger({ kind: 'assistant_text', payload: { text: 'x' } })).toBe(false)
  })
})

describe('History feed: folds and waiting items (#122, #121)', () => {
  it('draws 27 card proposals as one line: count, outcome, cards as chips, span; a tap opens the list', () => {
    show(ownerReply(NOW))
    const feed = screen.getByTestId('reel-list')
    const fold = within(feed).getAllByTestId('stroke').find(l => l.textContent!.includes('Filed 26 cards'))!
    expect(within(feed).getAllByTestId('stroke')).toHaveLength(2)
    expect(within(fold).getByTestId('stroke-text')).toHaveTextContent('Filed 26 cards → approved, 1 rejected')
    const chips = within(within(fold).getByTestId('stroke-cards')).getAllByTestId('stroke-card')
    expect(chips.map(c => c.textContent)).toEqual(Array.from({ length: 27 }, (_, i) => `#${44 + i}`).filter(c => c !== '#57'))
    const r = ownerReply(NOW).strokes![0]
    expect(fold).toHaveTextContent(`${hm(r.first_at!)}–${hm(r.at)}`)
    const toggle = within(fold).getByTestId('stroke-text')
    expect(toggle.tagName).toBe('BUTTON')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(within(fold).queryAllByTestId('stroke-item')).toHaveLength(0)
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const items = within(fold).getAllByTestId('stroke-item')
    expect(items).toHaveLength(27)
    expect(items[13]).toHaveTextContent('File a board card → Rejected')
    expect(document.getElementById(toggle.getAttribute('aria-controls')!)).toBe(within(fold).getByTestId('stroke-fold-items'))
    fireEvent.click(toggle)
    expect(within(fold).queryAllByTestId('stroke-item')).toHaveLength(0)
  })

  it('a waiting item keeps the fold waiting, its jump goes to it, and it is pinned above the feed', () => {
    hold(1)
    const reply = ownerReply(NOW, { waiting: true })
    const { onJump } = show(reply)
    const fold = within(screen.getByTestId('reel-list')).getAllByTestId('stroke').find(l => l.textContent!.includes('cards'))!
    expect(fold).toHaveClass('is-waiting')
    expect(within(fold).getByTestId('stroke-text')).toHaveTextContent('1 waiting, 25 approved, 1 rejected')
    const target = document.createElement('div')
    target.dataset.eventId = 'ev-p26'
    target.scrollIntoView = vi.fn()
    document.body.appendChild(target)
    try {
      fireEvent.click(within(fold).getByTestId('stroke-jump'))
      expect(vi.mocked(onJump).mock.calls[0][1]()).toBe(true)
      expect(target.scrollIntoView).toHaveBeenCalled()
    } finally {
      target.remove()
    }
    const pinned = screen.getByTestId('stroke-pinned')
    expect(pinned).toHaveAccessibleName('Waiting on you')
    expect(within(pinned).getAllByTestId('stroke-pin')).toHaveLength(1)
    expect(pinned.compareDocumentPosition(screen.getByTestId('reel-list')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('pins a waiting question that is past the lines drawn (#121)', () => {
    const reply = strokesReply(NOW)
    const many = Array.from({ length: 150 }, (_, i) => ({
      id: `m${i}`, kind: 'finding' as const, source: 'mark' as const, by: 'agent' as const, text: `finding ${i}`,
      at: new Date(NOW.getTime() - (i + 1) * 1000).toISOString(), seq: 1000 - i, topic_id: 'tp_open', jump: null,
    }))
    reply.strokes = [...many, ...reply.strokes!]
    show(reply)
    // The question (st-q) is older than the first 100 lines drawn, but pinned.
    expect(within(screen.getByTestId('reel-list')).queryAllByText(/Which drawer width/)).toHaveLength(0)
    expect(within(screen.getByTestId('stroke-pinned')).getByTestId('stroke-pin')).toHaveTextContent('Which drawer width should the phone use?')
    // Filtering to other kinds leaves it out; filtering to attention keeps it.
    fireEvent.click(screen.getByTestId('stroke-filter-finding'))
    expect(screen.queryByTestId('stroke-pinned')).toBeNull()
    fireEvent.click(screen.getByTestId('stroke-filter-finding'))
    fireEvent.click(screen.getByTestId('stroke-filter-attention'))
    expect(screen.getByTestId('stroke-pinned')).toBeInTheDocument()
  })

  it('opens the owner\'s topic onto compact lines: your 3 messages, 3 subagents, 2 files, a commit', () => {
    hold(1)
    show(ownerReply(NOW))
    fireEvent.click(screen.getByTestId('reel-head'))
    const summary = screen.getByTestId('reel-summary')
    expect(Array.from(summary.children).map(c => c.getAttribute('data-testid'))).toEqual(['reel-sum-messages', 'reel-sum-agents', 'reel-sum-files', 'reel-sum-commits'])
    expect(within(summary).getAllByTestId('reel-sum-message')).toHaveLength(3)
    expect(within(summary).getByTestId('reel-sum-agents')).toHaveTextContent('3 subagents')
    expect(within(summary).getAllByTestId('reel-sum-file').map(f => f.textContent)).toEqual(['follow-ups.md v2', 'a-very-long-file-name-for-the-duplicate-search-results.csv v1'])
    expect(summary.querySelector('.agent-card, [data-event-id]')).toBeNull()
  })
})

describe('History feed: fix round 1 of #122', () => {
  it('pins the waiting proposal itself, its own words, never the fold or its chips (I2)', () => {
    show(ownerReply(NOW, { waiting: true }))
    const pinned = screen.getByTestId('stroke-pinned')
    const pins = within(pinned).getAllByTestId('stroke-pin')
    expect(pins).toHaveLength(1)
    expect(within(pins[0]).getByTestId('stroke-text')).toHaveTextContent(/^File a board card$/)
    expect(within(pinned).queryAllByTestId('stroke-card')).toHaveLength(0)
    expect(within(pinned).queryByTestId('stroke-cards')).toBeNull()
    expect(pinned).not.toHaveTextContent('Filed')
  })

  it('shows a waiting item once: pinned above, a marker in its place in the feed and in its fold (I3)', () => {
    show(strokesReply(NOW))
    const copies = screen.getAllByTestId('stroke-text').filter(t => t.textContent!.includes('Which drawer width'))
    expect(copies).toHaveLength(1)
    expect(copies[0].closest('[data-testid="stroke-pinned"]')).not.toBeNull()
    expect(screen.getAllByTestId('stroke-pinned-marker')).toHaveLength(1)
    cleanup()
    show(ownerReply(NOW, { waiting: true }))
    const fold = within(screen.getByTestId('reel-list')).getAllByTestId('stroke').find(l => l.textContent!.includes('cards'))!
    fireEvent.click(within(fold).getByTestId('stroke-text'))
    expect(within(fold).getAllByTestId('stroke-item')).toHaveLength(26)
    expect(within(fold).getAllByTestId('stroke-pinned-marker')).toHaveLength(1)
  })

  it('gives each copy of a row its own key: a fold item\'s "not found" note shows in that item only', () => {
    show(ownerReply(NOW), { missing: 'stroke:fold:ev-p0/stroke-item:ev-p5' })
    const fold = within(screen.getByTestId('reel-list')).getAllByTestId('stroke').find(l => l.textContent!.includes('cards'))!
    fireEvent.click(within(fold).getByTestId('stroke-text'))
    const notes = screen.getAllByRole('status')
    expect(notes).toHaveLength(1)
    expect(notes[0].closest('[data-testid="stroke-item"]')).toHaveTextContent('File a board card')
  })

  it('says what a bounded fold and topic left out (I4)', () => {
    const reply = ownerReply(NOW)
    const fold = reply.strokes![0]
    fold.items_truncated = 120
    fold.cards_truncated = 95
    reply.topics[0].messages_truncated = 12
    show(reply)
    const line = within(screen.getByTestId('reel-list')).getAllByTestId('stroke').find(l => l.textContent!.includes('cards'))!
    expect(within(line).getByTestId('stroke-cards-earlier')).toHaveTextContent('+95')
    fireEvent.click(within(line).getByTestId('stroke-text'))
    expect(within(line).getByTestId('stroke-items-earlier')).toHaveTextContent('and 120 earlier')
    fireEvent.click(screen.getByTestId('reel-head'))
    const msgs = screen.getByTestId('reel-sum-messages')
    expect(msgs).toHaveTextContent('15 messages')
    expect(within(msgs).getByTestId('reel-sum-messages-earlier')).toHaveTextContent('+12')
  })

  it('an unanswered item (no-wait, or the session ended) is not waiting and not pinned', () => {
    const reply = strokesReply(NOW)
    const q = reply.strokes!.find(s => s.id === 'st-q')!
    q.state = 'unanswered'
    show(reply)
    expect(screen.queryByTestId('stroke-pinned')).toBeNull()
    const line = lines().find(l => l.textContent!.includes('Which drawer width'))!
    expect(line).not.toHaveClass('is-waiting')
    expect(line).toHaveClass('is-closed')
    expect(line.querySelector('.stroke-glyph')).toHaveAttribute('title', 'Asked of you · unanswered')
  })

  it('dates the message chips when the topic\'s messages span days', () => {
    hold(1)
    const reply = ownerReply(NOW)
    reply.topics[0].messages![0].ts = new Date(NOW.getTime() - 30 * 3600_000).toISOString()
    show(reply)
    fireEvent.click(screen.getByTestId('reel-head'))
    const chips = within(screen.getByTestId('reel-sum-messages')).getAllByTestId('reel-sum-message')
    for (const c of chips) expect(c.textContent).toMatch(/^[A-Z][a-z]{2} \d{1,2}, \d{2}:\d{2}$/)
  })
})

describe('wallClockMs', () => {
  it('counts parallel runs once', () => {
    const at = (min: number) => new Date(Date.UTC(2026, 0, 1, 10, min)).toISOString()
    // Four 30-minute runs in parallel, then one 10-minute run after a gap: 40 minutes, not 130.
    const runs = [0, 0, 0, 0].map(() => ({ ts: at(0), duration_ms: 30 * 60_000 })).concat([{ ts: at(45), duration_ms: 10 * 60_000 }])
    expect(wallClockMs(runs)).toBe(40 * 60_000)
  })
})

describe('History feed: fix round 2 of #122', () => {
  it('a fold whose oldest member waits keeps its line and chips in the feed; the member is pinned (N1)', () => {
    // The agent files 26 cards; the person approves all but the first.
    show(ownerReply(NOW, { oldestWaiting: true }))
    const feed = screen.getByTestId('reel-list')
    expect(within(feed).queryAllByTestId('stroke-pinned-marker')).toHaveLength(0)
    const fold = within(feed).getAllByTestId('stroke').find(l => l.textContent!.includes('cards'))!
    expect(fold).toHaveClass('is-fold')
    expect(fold).toHaveClass('is-waiting')
    expect(within(fold).getAllByTestId('stroke-card')).toHaveLength(25)
    const pins = within(screen.getByTestId('stroke-pinned')).getAllByTestId('stroke-pin')
    expect(pins).toHaveLength(1)
    expect(within(pins[0]).getByTestId('stroke-text')).toHaveTextContent(/^File a board card$/)
    // Opened: the waiting member comes first, as a marker in a group of its own; then the rest.
    fireEvent.click(within(fold).getByTestId('stroke-text'))
    const list = within(fold).getByTestId('stroke-fold-items')
    const rows = Array.from(list.children)
    expect(rows[0]).toHaveAttribute('data-testid', 'stroke-pinned-marker')
    expect(rows[0]).toHaveAttribute('data-group', 'waiting')
    expect(rows.slice(1).every(r => r.getAttribute('data-group') === 'rest')).toBe(true)
    expect(rows).toHaveLength(27)
  })

  it('lists an older waiting member above the newest ones, never among the "earlier" (N4)', () => {
    const reply = ownerReply(NOW, { oldestWaiting: true })
    const fold = reply.strokes![0]
    fold.items_truncated = 10
    show(reply)
    const line = within(screen.getByTestId('reel-list')).getAllByTestId('stroke').find(l => l.textContent!.includes('cards'))!
    fireEvent.click(within(line).getByTestId('stroke-text'))
    const rows = Array.from(within(line).getByTestId('stroke-fold-items').children)
    expect(rows[0]).toHaveAttribute('data-group', 'waiting')
    expect(rows[rows.length - 1]).toHaveTextContent('and 10 earlier')
    // Every row between them is newest first.
    const times = rows.slice(1, -1).map(r => r.querySelector('time, .stroke-time')!.getAttribute('datetime') ?? '')
    expect([...times].sort().reverse()).toEqual(times)
  })
})
