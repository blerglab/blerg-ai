import { Profiler } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { formatDuration, useTranscriptStore, type AgentEvent, type SessionTranscript } from '@blerglab/chat'
import HistoryTab, { TopicList } from './HistoryTab'
import { reelReply } from '../test/reelFixture'
import { EMPTY_HISTORY, spanLabel, type TopicsReply } from '../lib/topics'
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
const expand = (i: number) => fireEvent.click(within(cards()[i]).getByTestId('reel-head'))

beforeEach(() => {
  useTranscriptStore.setState({ sessions: {} })
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

describe('History tab', () => {
  it('lists the marked topics newest first: a now dot on the open one, outcome, span and counts', () => {
    const reply = reelReply(NOW)
    show(ready(reply))
    const all = cards()
    expect(all.map(c => within(c).getByTestId('reel-head').textContent)).toEqual(['Fix the drawer on phones', 'Build the History tab'])
    expect(within(all[0]).getByTestId('reel-now')).toBeInTheDocument()
    expect(within(all[1]).queryByTestId('reel-now')).toBeNull()
    expect(within(all[1]).getByTestId('reel-outcome')).toHaveTextContent('Shipped on main')
    expect(within(all[1]).getByTestId('reel-span')).toHaveTextContent(spanLabel(reply.topics[1], NOW))
    expect(within(all[1]).getByTestId('reel-counts')).toHaveTextContent('2 docs · 3 agents · 3 commits')
    expect(within(all[0]).getByTestId('reel-counts')).toHaveTextContent('1 agent')
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

  it('opens a card onto its timeline, in seq order', () => {
    show(ready())
    // A closed card controls nothing; an open one names its timeline.
    expect(within(cards()[1]).getByTestId('reel-head')).not.toHaveAttribute('aria-controls')
    expand(1)
    const head = within(cards()[1]).getByTestId('reel-head')
    expect(document.getElementById(head.getAttribute('aria-controls')!)).toBe(within(cards()[1]).getByRole('list'))
    const rows = within(cards()[1]).getAllByRole('listitem')
    expect(rows.map(r => r.getAttribute('data-testid'))).toEqual([
      'reel-request', 'reel-document', 'reel-document', 'reel-agent', 'reel-agent', 'reel-agent', 'reel-done',
    ])
    expect(rows[0]).toHaveTextContent('You: Build the History tab per the spec')
    expect(rows[6]).toHaveTextContent('Done: Shipped on main')
    expand(0)
    expect(within(cards()[0]).getByTestId('reel-request')).toHaveTextContent('Started: Fix the drawer on phones')
  })

  it('draws every version as a chip: filled in the topic, outlined outside it, struck when deleted', () => {
    const { onOpenArtifact } = show(ready())
    expand(1)
    const plan = within(cards()[1]).getAllByTestId('reel-document')[0]
    const [v1, v2, v3] = within(plan).getAllByTestId('reel-chip')
    expect(v1.tagName).toBe('SPAN')
    expect(v1).toHaveAttribute('aria-disabled', 'true')
    expect(v1.querySelector('s')).toHaveTextContent('v1')
    expect(v2).toHaveAttribute('href', '?artifact=a2')
    expect(v2).toHaveClass('in-topic')
    expect(v3).toHaveAttribute('href', '?artifact=a3')
    expect(v3).not.toHaveClass('in-topic')
    // Named for a screen reader by file and version; a deleted one says so.
    expect(within(plan).getByRole('link', { name: 'plan.md v2' })).toBe(v2)
    expect(within(plan).getByRole('link', { name: 'plan.md v3' })).toBe(v3)
    expect(v1).toHaveTextContent('plan.md v1 (deleted)')
    fireEvent.click(v2)
    expect(onOpenArtifact).toHaveBeenCalledWith('a2')
    fireEvent.click(v1)
    expect(onOpenArtifact).toHaveBeenCalledTimes(1)
  })

  it('draws a version whose bytes are gone as a disabled chip that says no longer available', () => {
    const reply = reelReply(NOW)
    const doc = reply.topics.flatMap(t => t.items).find(i => i.type === 'document' && i.versions.some(v => v.artifact_id === 'a2'))
    if (!doc || doc.type !== 'document') throw new Error('fixture has no a2 document')
    doc.versions = doc.versions.map(v => (v.artifact_id === 'a2' ? { ...v, missing: true } : v))
    const { onOpenArtifact } = show(ready(reply))
    expand(1)
    const plan = within(cards()[1]).getAllByTestId('reel-document')[0]
    const v2 = within(plan).getAllByTestId('reel-chip')[1]
    expect(v2.tagName).toBe('SPAN')
    expect(v2).toHaveClass('missing')
    expect(v2).toHaveAttribute('aria-disabled', 'true')
    expect(v2).toHaveAttribute('title', 'v2 is no longer available')
    expect(v2).toHaveTextContent('plan.md v2 (no longer available)')
    expect(within(plan).queryByRole('link', { name: 'plan.md v2' })).toBeNull()
    fireEvent.click(v2)
    expect(onOpenArtifact).not.toHaveBeenCalled()
  })

  it('draws the commits on the Done row as short chips that copy the sha, never a link out of Blerg', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    show(ready())
    expand(1)
    const done = within(cards()[1]).getByTestId('reel-done')
    const chips = within(done).getAllByTestId('reel-commit')
    expect(chips.map(c => c.textContent)).toEqual(['1a2b3c4', '9f8e7d6', '0badc0f'])
    expect(done.querySelector('a')).toBeNull()
    for (const c of chips) expect(c.tagName).toBe('BUTTON')
    const [long, , bare] = chips
    expect(long).toHaveAttribute('title', '1a2b3c4d5e6f708192a3b4c5d6e7f80912a3b4c5 (click to copy)')
    expect(within(done).getByRole('button', { name: 'Copy commit 0badc0ffee0ddf00d' })).toBe(bare)
    await act(async () => { fireEvent.click(bare) })
    expect(writeText).toHaveBeenCalledWith('0badc0ffee0ddf00d')
    expect(within(done).getByRole('status')).toHaveTextContent('Copied')
    expect(done).toHaveTextContent('Done: Shipped on main')
  })

  it('draws no commit chips on a Done row without commits', () => {
    const reply = reelReply(NOW)
    const done = reply.topics[1].items[6]
    if (done.type !== 'done') throw new Error('fixture')
    delete done.commits
    reply.topics[1].counts = { documents: 2, agents: 3 }
    show(ready(reply))
    expand(1)
    expect(within(cards()[1]).queryAllByTestId('reel-commit')).toHaveLength(0)
    expect(within(cards()[1]).getByTestId('reel-counts')).toHaveTextContent('2 docs · 3 agents')
  })

  it('says how many older versions a capped document leaves out', () => {
    const reply = reelReply(NOW)
    const plan = reply.topics[1].items[1]
    if (plan.type !== 'document') throw new Error('fixture')
    plan.versions_total = 25
    show(ready(reply))
    expand(1)
    const [planRow, mockRow] = within(cards()[1]).getAllByTestId('reel-document')
    expect(within(planRow).getByTestId('reel-older')).toHaveTextContent('+22 older')
    // Before the oldest listed version, where the older ones would be.
    expect(planRow.querySelector('.reel-chips')!.firstElementChild).toBe(within(planRow).getByTestId('reel-older'))
    expect(within(mockRow).queryByTestId('reel-older')).toBeNull()
  })

  it('offers "Show in conversation" only for a row the chat holds, and follows the store when it resets', () => {
    hold(150)
    const onJump = vi.fn((_: string, go: () => boolean) => go())
    show(ready(), { onJump })
    expand(1)
    const card = cards()[1]
    const request = within(card).getByTestId('reel-request')
    expect(within(request).queryByTestId('rail-show')).toBeNull()
    expect(within(request).getByTestId('reel-earlier')).toHaveAttribute('title', 'Earlier than what is loaded')
    const agent = within(card).getAllByTestId('reel-agent')[0]
    const target = document.createElement('div')
    target.setAttribute('data-call-id', 'toolu_1')
    target.scrollIntoView = vi.fn()
    document.body.appendChild(target)
    fireEvent.click(within(agent).getByTestId('rail-show'))
    expect(onJump).toHaveBeenCalledWith('reel:ev-160', expect.any(Function))
    expect(target.scrollIntoView).toHaveBeenCalled()
    act(() => useTranscriptStore.getState().resetEvents('sess-a'))
    expect(within(card).queryAllByTestId('rail-show')).toHaveLength(0)
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

  it('names a row that was not found, as the Live rows do', () => {
    hold(1)
    show(ready(), { missing: 'reel:ev-160' })
    expand(1)
    const agent = within(cards()[1]).getAllByTestId('reel-agent')[0]
    expect(within(agent).getByRole('status')).toHaveTextContent('Not in the part of the conversation the chat has drawn. Scroll up in the chat to reach it.')
  })

  it('gives each subagent its status, type, model, tokens and duration; no row carries the chat\'s anchors', () => {
    show(ready())
    expand(1)
    const [done, failed, stopped] = within(cards()[1]).getAllByTestId('reel-agent')
    expect(done).toHaveTextContent('done')
    expect(done).toHaveTextContent('general-purpose')
    expect(done).toHaveTextContent('sonnet-5')
    expect(within(done).getByTestId('reel-agent-tokens')).toHaveTextContent('1.5k tokens')
    expect(done).toHaveTextContent(formatDuration(412000))
    expect(within(failed).getByTestId('reel-agent-status')).toHaveTextContent('error')
    expect(within(stopped).getByTestId('reel-agent-status')).toHaveTextContent('stopped')
    expect(cards()[1].querySelector('[data-event-id], [data-call-id], [data-call-ids]')).toBeNull()
  })

  it('a mock image gets a thumbnail, a mock page an icon', async () => {
    const reply = reelReply(NOW)
    const doc = reply.topics[1].items[2]
    if (doc.type !== 'document') throw new Error('fixture')
    Object.assign(doc, { name: 'shot.png', view: 'image', content_type: 'image/png' })
    show(ready(reply))
    expand(1)
    expect(await screen.findByTestId('reel-thumb')).toHaveAttribute('src', 'blob:x')
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
  it('shows a chip in its topic that opens the card on its Board page, in Blerg', () => {
    const reply = reelReply(NOW)
    reply.topics[0].items.push({ type: 'card', seq: 399, event_id: 'ev-card', ts: NOW.toISOString(), card })
    reply.topics[0].counts.cards = 1
    show(ready(reply))
    expect(within(cards()[0]).getByTestId('reel-counts')).toHaveTextContent('1 card')
    expand(0)
    const chip = within(cards()[0]).getByTestId('reel-card-chip')
    expect(chip).toHaveTextContent('Product #42 Export button is stale')
    expect(chip.getAttribute('href')).toMatch(/\/boards\/b1\/cards\/42$/)
    expect(chip.getAttribute('href')).not.toMatch(/^https?:\/\/(?!localhost|board\.)/)
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
