import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import type { AgentEvent } from '../types'

// Thinking blocks are one assistant-style row, left out when the host asks for hidden; a subagent's events are drawn inside its card, not as the
// main agent's.

let seq = 0
// The test events are stamped 2026-07-26T00:00:00Z; the clock is just after, so none is stale.
const NOW = Date.parse('2026-07-26T00:05:00Z')
function ev(kind: string, payload: unknown, ts?: string): AgentEvent {
  seq++
  return mk({ client_event_id: `e${seq}`, seq, kind, payload, ...(ts ? { ts } : {}) })
}

const LONG = 'I should look at the transcript store first, because the streaming buffer is where the reply is folded'

const agentCall = (id: string) => ev('tool_call', { tool: 'Agent', call_id: id, input: { description: 'Survey the repo', prompt: 'List the directories', subagent_type: 'Explore' } })
const started = (id: string) => ev('subagent_started', { child_id: id, agent_type: 'Explore', model: '', summary: 'Survey the repo' })
const done = (id: string, summary = 'docs and runner') => ev('subagent_done', { child_id: id, agent_type: 'Explore', model: '', summary })
const agentResult = (id: string, is_error = false) => ev('tool_result', { call_id: id, output: 'Two directories: docs and runner.', is_error, duration_ms: 0 })
const childCall = (id: string, parent: string) => ev('tool_call', { tool: 'Bash', call_id: id, input: { command: 'ls -la' }, parent_call_id: parent })
const childResult = (id: string, parent: string) => ev('tool_result', { call_id: id, output: 'docs\nrunner', is_error: false, duration_ms: 5, parent_call_id: parent })
const childText = (text: string, parent: string) => ev('assistant_text', { text, done: true, parent_call_id: parent })
const childThinking = (text: string, parent: string) => ev('assistant_thinking', { text, parent_call_id: parent })

function mount(status: 'idle' | 'running' = 'idle', t: FakeTransport = createFakeTransport(), thinking?: 'shown' | 'hidden' | 'open' | 'collapsed') {
  const r = render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status })} thinking={thinking} />)
  return { t, ...r }
}

function subagentRun(id = 'A') {
  return [
    ev('user_message', { text: 'survey it', source: 'chat' }),
    agentCall(id), started(id),
    childThinking('Start with ls.', id),
    childText('Listing the directories.', id),
    childCall('s1', id), childResult('s1', id),
    done(id), agentResult(id),
    ev('assistant_text', { text: 'The repo has two directories.', done: true }),
  ]
}

describe('thinking', () => {
  beforeEach(() => {
    resetChatState()
    seq = 0
  })

  it('is an ordinary assistant card with rendered Markdown and a "thinking" caption', () => {
    const text = `${LONG}\nSecond line.\n\n**important**`
    seed(ev('user_message', { text: 'why?', source: 'chat' }), ev('assistant_thinking', { text }), ev('assistant_text', { text: 'Because.', done: true }))
    mount()
    const row = screen.getByTestId('thinking')
    expect(row.tagName).toBe('DIV')
    expect(row).toHaveClass('agent-card', 'assistant')
    expect(row).toHaveAttribute('data-kind', 'thinking')
    expect(row.querySelector('details, summary')).toBeNull()
    // Thinking retains its content, with the same Markdown formatting as answers.
    expect(row.querySelector('.thinking-text')!.textContent).toContain(`${LONG}\nSecond line.`)
    expect(row.querySelector('strong')!.textContent).toBe('important')
    // Where the answer shows its time, the card says "thinking · <time>".
    expect(row.querySelector('.card-foot')!.textContent).toMatch(/^thinking · /)
    // The answer is the assistant card under it.
    expect(screen.getByText('Because.').closest('.agent-card.assistant')).not.toBeNull()
  })

  it('draws no row for thinking when hidden, and brings it back when shown', () => {
    seed(ev('assistant_thinking', { text: 'first thought' }), ev('assistant_text', { text: 'Because.', done: true }))
    const t = createFakeTransport()
    const view = (thinking: 'shown' | 'hidden' | 'open' | 'collapsed') => (
      <ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} thinking={thinking} />
    )
    const { rerender } = render(view('hidden'))
    expect(screen.queryByTestId('thinking')).not.toBeInTheDocument()
    expect(screen.queryByText('first thought')).not.toBeInTheDocument()
    expect(screen.getByText('Because.')).toBeInTheDocument()
    rerender(view('shown'))
    expect(screen.getByTestId('thinking')).toHaveTextContent('first thought')
    // The old names still work: 'collapsed' hides, 'open' shows.
    rerender(view('collapsed'))
    expect(screen.queryByTestId('thinking')).not.toBeInTheDocument()
    rerender(view('open'))
    expect(screen.getByTestId('thinking')).toHaveTextContent('first thought')
  })

  it('draws no row for a subagent\'s thinking when hidden', () => {
    seed(...subagentRun())
    mount('idle', createFakeTransport(), 'hidden')
    fireEvent.click(within(card()).getByTestId('subagent-row'))
    const body = within(card()).getByTestId('subagent-body')
    expect(within(body).getByText('Listing the directories.')).toBeInTheDocument()
    expect(within(body).queryByTestId('thinking')).not.toBeInTheDocument()
    expect(within(body).queryByText('Start with ls.')).not.toBeInTheDocument()
  })

  it('does not close the streaming reply', () => {
    const s = useTranscriptStore.getState()
    act(() => {
      s.ingest(mk({ client_event_id: 'd1', kind: 'assistant_text', payload: { text: 'typing', done: false }, transient: true }))
      s.ingest(mk({ client_event_id: 't1', seq: 1, kind: 'assistant_thinking', payload: { text: 'hmm' } }))
    })
    expect(useTranscriptStore.getState().sessions[SESSION].streaming).toBe('typing')
  })
})

const update = (id: string, payload: Record<string, unknown>) => ev('subagent_update', { child_id: id, agent_type: 'Explore', summary: 'Survey the repo', ...payload })
const card = () => screen.getByTestId('subagent-card')
const row = () => within(card()).getByTestId('subagent-row')
// The one state glyph: its words are its accessible name.
const statusOf = () => within(card()).getByTestId('subagent-status').getAttribute('aria-label')
const expand = () => { fireEvent.click(row()) }

describe('subagent card', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })
  afterEach(() => { vi.useRealTimers() })

  it('shows a subagent with no end recorded for 12 hours as stopped, with its summary', () => {
    const old = new Date(NOW - 13 * 3600_000).toISOString()
    seed(
      ev('user_message', { text: 'survey it', source: 'chat' }, old),
      ev('subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey the repo' }, old),
    )
    mount('running')
    expect(card()).toHaveClass('status-stopped')
    expect(card().dataset.status).toBe('stopped')
    expect(statusOf()).toBe('no result')
    expect(within(card()).getByTestId('subagent-status')).toHaveTextContent(/^…$/)
    expect(row()).toHaveAttribute('title', 'Survey the repo · Explore · configured effort: unknown · no result · no end recorded')
    // The row says no more; opened, the card says why.
    expect(within(card()).queryByTestId('subagent-line')).toBeNull()
    expect(row()).toHaveAttribute('aria-expanded', 'false')
    expand()
    expect(within(card()).getByTestId('subagent-line')).toHaveTextContent(/^no end recorded$/)
  })

  it('uses sparse replay lifecycle context to finish an old child while another stays running', () => {
    const t = createFakeTransport()
    mount('running', t)
    const event = (n: number, kind: string, payload: unknown) => mk({ client_event_id: `sparse-${n}`, seq: n, kind, payload })
    act(() => {
      t.emit(event(801, 'assistant_text', { text: 'Later report from the completed review', done: true, parent_call_id: 'old-child' }))
      t.emit(event(802, 'subagent_started', { child_id: 'active-child', agent_type: 'Codex', summary: 'Current review' }))
      t.replayDone({ older: true, firstSeq: 801, lastSeq: 802, hasMore: false, hasOlder: true })
    })
    const completed = () => document.querySelector('[data-call-id="old-child"]')!
    const active = () => document.querySelector('[data-call-id="active-child"]')!
    expect(completed()).toHaveAttribute('data-status', 'running')
    expect(active().querySelector('.tcg-spin')).not.toBeNull()
    act(() => {
      t.emit(event(20, 'subagent_started', { child_id: 'old-child', agent_type: 'Codex', summary: 'Earlier review' }))
      t.emit(event(40, 'subagent_done', { child_id: 'old-child', agent_type: 'Codex', summary: 'Review finished' }))
    })
    expect(completed()).toHaveAttribute('data-status', 'done')
    expect(completed().querySelector('.tcg-spin')).toBeNull()
    expect(within(completed() as HTMLElement).getByTestId('subagent-status')).toHaveAttribute('aria-label', 'done')
    expect(active()).toHaveAttribute('data-status', 'running')
    expect(active().querySelector('.tcg-spin')).not.toBeNull()
    expect(useTranscriptStore.getState().sessions[SESSION].firstSeq).toBe(801)
  })

  it('keeps a subagent started an hour ago running', () => {
    const recent = new Date(NOW - 3600_000).toISOString()
    seed(
      ev('user_message', { text: 'survey it', source: 'chat' }, recent),
      ev('subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey the repo' }, recent),
    )
    mount('running')
    expect(card().dataset.status).toBe('running')
    expect(screen.queryByText('no end recorded')).toBeNull()
  })

  it('a subagent with no effort of its own shows the session\'s, never "unknown"', () => {
    seed(...subagentRun())
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle', effort: 'high' })} />)
    expect(row().getAttribute('title')).toContain("configured effort: high (the session's)")
    fireEvent.click(row())
    expect(within(card()).getByTestId('subagent-effort')).toHaveTextContent("Configured effort: high (the session's)")
  })

  it('is one row, the title and one state glyph, until it is opened', () => {
    seed(...subagentRun())
    mount()
    expect(within(card()).getByTestId('subagent-title')).toHaveTextContent('↳ Survey the repo')
    expect(statusOf()).toBe('done')
    expect(within(card()).getByTestId('subagent-status')).toHaveTextContent(/^✓$/)
    // The row is the title, the glyph and the tool group's chevron: no chips, no doing or end line,
    // no report.
    expect(row()).toHaveTextContent(/^↳ Survey the repo✓▸$/)
    expect(card()).toHaveTextContent(/^↳ Survey the repo✓▸$/)
    expect(row().querySelector('.tcg-chev')).toHaveAttribute('aria-hidden', 'true')
    expect(within(card()).queryByTestId('subagent-line')).toBeNull()
    expect(within(card()).queryByTestId('subagent-report')).toBeNull()
    // What the row leaves out is its tooltip and its accessible name.
    expect(row()).toHaveAttribute('title', 'Survey the repo · Explore · configured effort: unknown · done')
    expect(screen.getByRole('button', { name: 'Survey the repo · Explore · configured effort: unknown · done' })).toBe(row())
    expect(row()).toHaveAttribute('aria-expanded', 'false')
    // Nothing the subagent did is drawn, and the Agent call is not a tool row of its own.
    expect(screen.queryByText('Listing the directories.')).not.toBeInTheDocument()
    expect(screen.queryByTestId('thinking')).not.toBeInTheDocument()
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
    expect(screen.queryByText(/Agent/)).not.toBeInTheDocument()
    // The main agent's own answer is at the top level.
    expect(screen.getByText('The repo has two directories.')).toBeInTheDocument()

    expand()
    expect(row()).toHaveAttribute('aria-expanded', 'true')
    const body = within(card()).getByTestId('subagent-body')
    expect(within(body).getByTestId('subagent-line')).toHaveTextContent(/^done · 1 tool call · 1 message/)
    // Under the end line, the report's first line.
    expect(within(body).getByTestId('subagent-gist')).toHaveTextContent(/^↲ Two directories: docs and runner\.$/)
    expect(body.firstElementChild).toBe(within(body).getByTestId('subagent-line'))
    expect(within(body).getByTestId('subagent-line').nextElementSibling).toBe(within(body).getByTestId('subagent-gist'))
    expect(row().querySelector('.tcg-chev')).toHaveTextContent('▾')
    expect(within(body).getByText('Prompt')).toBeInTheDocument()
    expect(body.querySelector('.subagent-prompt')).toHaveTextContent('List the directories')
    expect(within(body).getByText('Listing the directories.')).toBeInTheDocument()
    expect(within(body).getByTestId('thinking')).toHaveTextContent('Start with ls.')
    expect(within(body).getByTestId('tool-row')).toHaveTextContent('$ ls -la')
    // The report it returned, in the open: last in the card.
    const report = within(body).getByTestId('subagent-report')
    expect(report).toHaveTextContent('Report')
    expect(report).toHaveTextContent('Two directories: docs and runner.')
    expect(body.lastElementChild).toBe(report)
    // Still one tool row in the whole transcript: the child's.
    expect(screen.getAllByTestId('tool-row')).toHaveLength(1)

    expand()
    expect(within(card()).queryByTestId('subagent-body')).toBeNull()
  })

  it('is running until subagent_done, then done', () => {
    const run = subagentRun()
    seed(...run.slice(0, 7))
    const { t } = mount('running')
    expect(statusOf()).toBe('running')
    expect(within(card()).getByTestId('subagent-status').querySelector('.tcg-spin')).not.toBeNull()
    // A foreground subagent keeps the solid card.
    expect(card()).toHaveAttribute('data-status', 'running')
    expect(card()).not.toHaveClass('status-background')
    act(() => { t.emit(run[7]) })
    expect(statusOf()).toBe('done')
  })

  it('says what it is doing: its newest tool call, or its newest message', () => {
    seed(ev('user_message', { text: 'go', source: 'chat' }), agentCall('A'), started('A'), childCall('s1', 'A'))
    const { t } = mount('running')
    // Not in the row: opened, the card says it.
    expect(within(card()).queryByTestId('subagent-line')).toBeNull()
    expand()
    expect(within(card()).getByTestId('subagent-line')).toHaveTextContent('running Bash · ls -la')
    act(() => { t.emit(ev('tool_call', { tool: 'Read', call_id: 's2', input: { file_path: '/repo/src/model/toolGroups.ts' }, parent_call_id: 'A' })) })
    expect(within(card()).getByTestId('subagent-line')).toHaveTextContent('running Read · toolGroups.ts')
    act(() => { t.emit(childText('Found the fold.\nIt nests subagents.', 'A')) })
    expect(within(card()).getByTestId('subagent-line')).toHaveTextContent(/^Found the fold\.$/)
  })

  it('names the model and tokens of its latest update in the row\'s tooltip, and the final totals once done', () => {
    seed(ev('user_message', { text: 'go', source: 'chat' }), agentCall('A'), started('A'), childCall('s1', 'A'),
      update('A', { model: 'claude-haiku-5', effort: 'low', usage: { input_tokens: 1000, output_tokens: 200, cache_read_tokens: 0, cache_write_tokens: 0 } }))
    const { t } = mount('running')
    expect(row()).toHaveAttribute('title', 'Survey the repo · Explore · claude-haiku-5 · configured effort: low · 1.2k tokens · running')
    expect(row()).toHaveAccessibleName('Survey the repo · Explore · claude-haiku-5 · configured effort: low · 1.2k tokens · running')
    // No chips in the row.
    expect(card()).not.toHaveTextContent('claude-haiku-5')
    expect(card()).not.toHaveTextContent('tokens')
    expand()
    expect(within(card()).getByTestId('subagent-settings')).toHaveTextContent('Configured effort: low')
    expand()
    act(() => {
      t.emit(ev('subagent_done', { child_id: 'A', agent_type: 'Explore', model: 'claude-haiku-5', effort: '', summary: 'found it',
        usage: { input_tokens: 20000, output_tokens: 3000, cache_read_tokens: 0, cache_write_tokens: 0 }, tool_uses: 1, duration_ms: 64000 }))
    })
    expect(row()).toHaveAccessibleName('Survey the repo · Explore · claude-haiku-5 · configured effort: unknown · 23k tokens · done')
    expand()
    expect(within(card()).getByTestId('subagent-line')).toHaveTextContent('done · 1 tool call · 0 messages · 23k tokens · 1m 4s')
    expect(within(card()).getByTestId('subagent-settings')).toHaveTextContent('Model: haiku-5 Configured effort: unknown')
  })

  it('a background subagent runs in the background until its real end', () => {
    seed(
      ev('user_message', { text: 'go', source: 'chat' }), agentCall('A'), started('A'),
      ev('subagent_done', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'running in the background', background: true }),
      agentResult('A'),
    )
    const { t } = mount('running')
    expect(statusOf()).toBe('running in the background')
    // A muted spinner and the ⏱.
    const glyph = within(card()).getByTestId('subagent-status')
    expect(glyph.querySelector('.tcg-spin')).not.toBeNull()
    expect(glyph).toHaveTextContent(/^⏱$/)
    // Drawn dashed while it runs in the background (status-background), solid once it ends.
    expect(card()).toHaveClass('status-background')
    expect(card()).toHaveAttribute('data-status', 'background')
    act(() => { t.emit(childCall('s1', 'A')) })
    expect(statusOf()).toBe('running in the background')
    act(() => { t.emit(done('A', 'The build is green.')) })
    expect(statusOf()).toBe('done')
    expect(card()).not.toHaveClass('status-background')
    expect(card()).toHaveAttribute('data-status', 'done')
    // Its call returned its launch: opened, the report is the end's summary.
    expect(card()).not.toHaveTextContent('The build is green.')
    expand()
    expect(within(card()).getByTestId('subagent-gist')).toHaveTextContent(/^↲ The build is green\.$/)
    // A one-line summary is not drawn twice.
    expect(within(card()).queryByTestId('subagent-report')).toBeNull()
    // A failed background subagent: its call returned its launch, the end says it failed.
    act(() => { t.emit(done('A', 'error: could not build')) })
    expect(statusOf()).toBe('error')
    expect(within(card()).getByTestId('subagent-status')).toHaveTextContent(/^✗$/)
  })

  it('infers a background launch from child activity after its call acknowledgement without a final end', () => {
    seed(
      ev('user_message', { text: 'go', source: 'chat' }), agentCall('A'), started('A'),
      agentResult('A'),
      childCall('s1', 'A'), childResult('s1', 'A'),
    )
    mount()
    expect(statusOf()).toBe('running in the background')
  })

  it('shows an error when the subagent call failed, and why in the row\'s tooltip', () => {
    seed(ev('user_message', { text: 'go', source: 'chat' }), agentCall('A'), started('A'), done('A', 'error: crashed'),
      ev('tool_result', { call_id: 'A', output: 'The build crashed.\nstack…', is_error: true, duration_ms: 0 }))
    mount()
    expect(statusOf()).toBe('error')
    expect(row()).toHaveAttribute('title', 'Survey the repo · Explore · configured effort: unknown · error · The build crashed.')
    expect(row()).toHaveAccessibleName('Survey the repo · Explore · configured effort: unknown · error · The build crashed.')
    // No second visible line.
    expect(card()).toHaveTextContent(/^↳ Survey the repo✗▸$/)
  })

  it('names a native subagent\'s error from its summary in the tooltip', () => {
    seed(ev('user_message', { text: 'go', source: 'chat' }), started('n1'), done('n1', 'error: budget spent\nmore'))
    mount()
    expect(row()).toHaveAttribute('title', 'Survey the repo · Explore · configured effort: unknown · error · budget spent')
  })

  it('a report that begins "error:" is not a failure when the call succeeded', () => {
    seed(ev('user_message', { text: 'go', source: 'chat' }), agentCall('A'), started('A'), done('A', 'error: handling looks fine'), agentResult('A'))
    mount()
    expect(statusOf()).toBe('done')
  })

  it('a native subagent (no call) still reads its failure from the summary', () => {
    seed(ev('user_message', { text: 'go', source: 'chat' }), started('n1'), done('n1', 'error: budget'))
    mount()
    expect(statusOf()).toBe('error')
  })

  it('nests events whose start is not held in a placeholder card that fills in when it arrives', () => {
    const t = createFakeTransport()
    seed(childText('a report from far back', 'A'), childCall('o1', 'A'), childResult('o1', 'A'))
    mount('idle', t)
    expect(within(card()).getByTestId('subagent-title')).toHaveTextContent('Subagent · started earlier')
    expect(screen.queryByText('a report from far back')).not.toBeInTheDocument()
    expect(screen.queryAllByTestId('tool-row')).toHaveLength(0)
    // The older page with its start arrives.
    act(() => {
      useTranscriptStore.getState().ingestMany(SESSION, [
        mk({ client_event_id: 'old1', seq: -2, kind: 'tool_call', payload: { tool: 'Agent', call_id: 'A', input: { description: 'Survey the repo', prompt: 'x' } } }),
        mk({ client_event_id: 'old2', seq: -1, kind: 'subagent_started', payload: { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey the repo' } }),
      ])
    })
    expect(screen.getAllByTestId('subagent-card')).toHaveLength(1)
    expect(within(card()).getByTestId('subagent-title')).toHaveTextContent('↳ Survey the repo')
    expect(row()).toHaveAccessibleName(/^Survey the repo · Explore · /)
  })

  it('a placeholder card opened keeps its state as it fills in', () => {
    for (const compact of ['1', '0']) {
      resetChatState()
      localStorage.setItem(COMPACT, compact)
      seed(childText('a report from far back', 'A'), childCall('o1', 'A'), childResult('o1', 'A'))
      const { unmount } = mount()
      expand()
      const before = card()
      act(() => {
        useTranscriptStore.getState().ingestMany(SESSION, [
          mk({ client_event_id: 'old1', seq: -2, kind: 'tool_call', payload: { tool: 'Agent', call_id: 'A', input: { description: 'Survey the repo', prompt: 'x' } } }),
          mk({ client_event_id: 'old2', seq: -1, kind: 'subagent_started', payload: { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey the repo' } }),
        ])
      })
      // In the one-card-per-call view the card sits at another event now: a new card, still open.
      if (compact === '0') expect(card()).not.toBe(before)
      expect(within(card()).getByTestId('subagent-title')).toHaveTextContent('↳ Survey the repo')
      expect(row()).toHaveAttribute('aria-expanded', 'true')
      expect(within(card()).getByText('a report from far back')).toBeInTheDocument()
      unmount()
    }
  })

  it('a placeholder card nests in the one-card-per-call view too', () => {
    localStorage.setItem(COMPACT, '0')
    seed(childText('a report from far back', 'A'), childCall('o1', 'A'), childResult('o1', 'A'))
    mount()
    expect(within(card()).getByTestId('subagent-title')).toHaveTextContent('Subagent · started earlier')
    expect(screen.queryByText('a report from far back')).not.toBeInTheDocument()
    expect(screen.queryAllByTestId('tool-card')).toHaveLength(0)
  })

  it('nests in the one-card-per-call view too', () => {
    localStorage.setItem(COMPACT, '0')
    seed(...subagentRun())
    mount()
    expect(screen.queryByText('Listing the directories.')).not.toBeInTheDocument()
    // No tool card for the Agent call, nor for the child's call, outside the subagent card.
    expect(screen.queryAllByTestId('tool-card')).toHaveLength(0)
    expand()
    expect(within(card()).getByTestId('tool-card')).toHaveTextContent('ls -la')
    expect(within(card()).getByText('Listing the directories.')).toBeInTheDocument()
  })
})

describe('subagent card: stale guard', () => {
  const H = 3600_000
  const at = (ms: number) => new Date(NOW - ms).toISOString()
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    vi.setSystemTime(NOW)
    resetChatState()
    seq = 0
  })
  afterEach(() => { vi.useRealTimers() })

  it('keeps a subagent started over 12 hours ago running while its updates keep coming', () => {
    seed(
      ev('user_message', { text: 'survey it', source: 'chat' }, at(14 * H)),
      ev('subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey the repo' }, at(13 * H)),
      ev('subagent_update', { child_id: 'A', agent_type: 'Explore', summary: 'Survey the repo', model: 'claude-haiku-5' }, at(5 * 60_000)),
    )
    mount('running')
    expect(card().dataset.status).toBe('running')
    expect(screen.queryByText('no end recorded')).toBeNull()
  })

  it('shows a background subagent whose launch is its only end, over 12 hours old, as stopped', () => {
    seed(
      ev('user_message', { text: 'watch it', source: 'chat' }, at(14 * H)),
      ev('subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey the repo' }, at(13 * H)),
      ev('subagent_done', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'running in the background', background: true }, at(13 * H)),
    )
    mount('idle')
    expect(card().dataset.status).toBe('stopped')
    expand()
    expect(within(card()).getByTestId('subagent-line')).toHaveTextContent(/^no end recorded$/)
  })

  it('flips an open card to stopped at the 12 hour mark, and stops its clock on unmount', () => {
    seed(
      ev('user_message', { text: 'survey it', source: 'chat' }, at(12 * H - 90_000)),
      ev('subagent_started', { child_id: 'A', agent_type: 'Explore', model: '', summary: 'Survey the repo' }, at(12 * H - 90_000)),
    )
    const before = vi.getTimerCount()
    const { unmount } = mount('running')
    expand()
    expect(card().dataset.status).toBe('running')
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(card().dataset.status).toBe('running')
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(card().dataset.status).toBe('stopped')
    expect(within(card()).getByTestId('subagent-line')).toHaveTextContent(/^no end recorded$/)
    unmount()
    expect(vi.getTimerCount()).toBe(before)
  })
})

describe('main reply while a Codex subagent finishes', () => {
  beforeEach(() => { resetChatState(); seq = 0 })

  it('keeps the root typing passage visible when a child message is persisted', () => {
    seed(agentCall('child-call'), started('child-call'))
    const { t } = mount('running')
    act(() => { t.emit({ ...childText('Child working note', 'child-call'), transient: true, seq: undefined, payload: { text: 'Child working note', done: false, parent_call_id: 'child-call' } }) })
    expect(screen.queryByTestId('streaming')).not.toBeInTheDocument()
    act(() => { t.emit({ ...ev('assistant_text', { text: 'Root passage still being written', done: false }), transient: true, seq: undefined }) })
    expect(screen.getByTestId('streaming')).toHaveTextContent('Root passage still being written')
    act(() => { t.emit(childText('Child completed its review', 'child-call')) })
    expect(screen.getByTestId('streaming')).toHaveTextContent('Root passage still being written')
    act(() => { t.emit(ev('assistant_text', { text: 'Root passage still being written', done: true })) })
    expect(screen.queryByTestId('streaming')).not.toBeInTheDocument()
    expect(screen.getAllByText('Root passage still being written')).toHaveLength(1)
  })
})
