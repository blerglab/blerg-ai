import { beforeEach, describe, expect, it } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import { compactionText } from './cards/EventCards'
import { isFromParentSession, userMessageShape } from '../model/userMessages'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

const idle: SessionMeta = meta({ status: 'idle', started_at: '2026-10-14T00:00:00Z' })

function ev(seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  return mk({ seq, kind, ts: '2026-10-14T00:00:00Z', payload })
}

function mount(m: SessionMeta = idle, t = createFakeTransport()) {
  render(<ChatView session={SESSION} transport={t.transport} meta={m} />)
  return t
}

describe('text nobody typed', () => {
  beforeEach(() => resetChatState())

  it('the restart note is a system note, not the person, from a current daemon and an older one', () => {
    seed(
      ev(1, 'user_message', { text: 'start', source: 'chat' }),
      ev(2, 'user_message', { text: '[system] Session resumed after a restart. The workspace is a fresh clone.', source: 'system' }),
      ev(3, 'user_message', { text: '[system] Session resumed after a restart. Older daemon.', source: 'chat' }),
    )
    mount()
    expect(screen.getAllByText('You')).toHaveLength(1)
    const notes = screen.getAllByTestId('system-note')
    expect(notes).toHaveLength(2)
    expect(notes[0].textContent).toContain('Session resumed after a restart. The workspace is a fresh clone.')
    expect(notes[0].textContent).not.toContain('[system]')
    expect(notes[1].textContent).toContain('Older daemon.')
  })

  it('a decision Blerg relays to the agent (review, proposal) is a Blerg note, never the person', () => {
    seed(
      ev(1, 'user_message', { text: 'start', source: 'chat' }),
      ev(2, 'user_message', { text: "Proposal p1 approved: Blerg #43 'Live check' created.", source: 'proposal' }),
      ev(3, 'user_message', { text: 'Review of SECURITY.md v1 (request r1): APPROVED', source: 'review' }),
    )
    mount()
    expect(screen.getAllByText('You')).toHaveLength(1)
    const notes = screen.getAllByTestId('system-note')
    expect(notes).toHaveLength(2)
    expect(notes[0].textContent).toContain('Blerg')
    expect(notes[0].textContent).toContain('Blerg #43')
    expect(notes[1].textContent).toContain('APPROVED')
  })

  it("a child's turn note in its parent is a Blerg note, never the person", () => {
    seed(
      ev(1, 'user_message', { text: 'start', source: 'chat' }),
      ev(2, 'user_message', { text: 'Child "Port" finished its turn: "Done.". blerg-runner session result c1', source: 'child_turn' }),
    )
    mount()
    expect(screen.getAllByText('You')).toHaveLength(1)
    const notes = screen.getAllByTestId('system-note')
    expect(notes).toHaveLength(1)
    expect(notes[0].textContent).toContain('Blerg')
    expect(notes[0].textContent).toContain('finished its turn')
  })

  it('a prompt a board, a parent session or a cron wrote is drawn under that author, never "You"', () => {
    seed(
      ev(1, 'user_message', { text: 'You are working card #43 on the blerg-board board', source: 'blerg-board' }),
      ev(2, 'user_message', { text: 'Port the tests', source: 'session' }),
      ev(3, 'user_message', { text: 'Summarize my inbox', source: 'cron' }),
      ev(4, 'user_message', { text: 'thanks', source: 'chat' }),
    )
    mount()
    expect(screen.getAllByText('You')).toHaveLength(1)
    for (const [author, text] of [['Board', 'card #43'], ['↑ Parent', 'Port the tests'], ['Cron', 'Summarize my inbox']]) {
      const card = screen.getByText(author).closest('.agent-card')
      expect(card?.className).toContain('other-author')
      expect(card?.textContent).toContain(text)
    }
  })

  it("a host's author wins over the default one", () => {
    seed(ev(1, 'user_message', { text: 'Port the tests', source: 'session' }))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={idle}
      describeUserMessage={(_e, p) => (p.source === 'session' ? { author: 'from Release prep' } : null)} />)
    expect(screen.getByText('from Release prep')).toBeTruthy()
    expect(screen.queryByText('↑ Parent')).toBeNull()
    expect(screen.queryByText('You')).toBeNull()
  })

  it("a slash command's output is a system card, kept as written", () => {
    const out = 'Total cost:            $0.0123\nTotal duration (API):  4.2s'
    seed(
      ev(1, 'user_message', { text: '/cost', source: 'chat' }),
      ev(2, 'user_message', { text: out, source: 'system' }),
    )
    mount()
    const card = screen.getByTestId('system-output')
    expect(card.querySelector('pre')!.textContent).toBe(out)
    expect(screen.getAllByText('You')).toHaveLength(1)
  })

  it('harness text and the native loop reminder stay hidden', () => {
    seed(
      ev(1, 'user_message', { text: '<system-reminder>\nx\n</system-reminder>', source: 'system' }),
      ev(2, 'user_message', { text: '[system reminder] You made changes this turn', source: 'system' }),
    )
    mount()
    expect(screen.queryByTestId('system-output')).toBeNull()
    expect(screen.queryByTestId('system-note')).toBeNull()
    expect(screen.queryByText(/system reminder/)).toBeNull()
  })

  it("the person's answer to an ask is one short row, never a bubble (#156)", () => {
    seed(ev(1, 'user_message', { text: 'yes, ship it', source: 'ask_answer', message_id: 'm1' }))
    mount()
    const row = screen.getByTestId('system-note')
    expect(row.textContent).toMatch(/^You\s*answered: yes, ship it/)
    expect(document.querySelector('.agent-card.user')).toBeNull()
  })

  it("the parent's answer to the child's question is the parent's, never \"You\"", () => {
    seed(
      ev(1, 'user_message', { text: 'use main', source: 'ask_answer', message_id: 'm1', answered_by: 'session' }),
      ev(2, 'user_message', { text: 'my own answer', source: 'ask_answer', message_id: 'm2' }),
    )
    mount()
    const parent = screen.getByText('use main').closest('.agent-card')!
    expect(parent.className).toContain('other-author')
    expect(parent.textContent).toMatch(/↑ Parent · Answer/)
    expect(screen.getByText('answered: my own answer').closest('[data-testid="system-note"]')!.textContent).toMatch(/You\s*answered: my own answer/)
    expect(screen.getAllByText('You')).toHaveLength(1)
  })

  it("a host names and links the parent: the author opens the parent session in the app", () => {
    seed(
      ev(1, 'user_message', { text: 'Port the tests', source: 'session' }),
      ev(2, 'user_message', { text: 'use main', source: 'ask_answer', message_id: 'm1', answered_by: 'session' }),
    )
    const opened: string[] = []
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={idle}
      describeUserMessage={(_e, p) => (isFromParentSession(p)
        ? { author: '↑ Parent · Release prep', authorHref: '/sessions/p1', onAuthorOpen: () => opened.push('p1') }
        : null)} />)
    const links = screen.getAllByRole('link', { name: '↑ Parent · Release prep' })
    expect(links).toHaveLength(2)
    expect(links[0].getAttribute('href')).toBe('/sessions/p1')
    fireEvent.click(links[1])
    expect(opened).toEqual(['p1'])
    expect(screen.getByText('use main').closest('.agent-card')!.textContent).toMatch(/↑ Parent · Release prep · Answer/)
    expect(screen.queryByText('You')).toBeNull()
  })

  it("the daemon's continue after recovering a session is a system note", () => {
    expect(userMessageShape({ text: 'Continue the task you were working on.', source: 'system' }))
      .toEqual({ as: 'notice', text: 'Session recovered: the agent was asked to continue the task it was working on.' })
    expect(userMessageShape({ text: 'Continue the task you were working on.', source: 'chat' })).toEqual({ as: 'person' })
  })

  it('userMessageShape: the person by default', () => {
    expect(userMessageShape({ text: 'hello', source: 'chat' })).toEqual({ as: 'person' })
    expect(userMessageShape({ text: '[system] note', source: 'human' })).toEqual({ as: 'person' })
    expect(userMessageShape(null)).toEqual({ as: 'person' })
    expect(userMessageShape({ text: '  ', source: 'system' })).toEqual({ as: 'hidden' })
  })
})

describe('API errors, retries and compactions', () => {
  beforeEach(() => resetChatState())

  it('an API error is an error row labelled as such, never the model speaking', () => {
    seed(
      ev(1, 'user_message', { text: 'go', source: 'chat' }),
      ev(2, 'error', { message: 'API Error: 529 overloaded', retryable: true, code: 'api_error' }),
    )
    mount()
    const row = screen.getByTestId('api-error')
    expect(row.textContent).toMatch(/API error.*529 overloaded/)
    expect(document.querySelector('.agent-card.assistant')).toBeNull()
  })

  it('a retry notice is a muted line and the turn keeps working, its typing text kept', () => {
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }))
    mount(meta({ status: 'running', started_at: '2026-10-14T00:00:00Z' }))
    act(() => useTranscriptStore.getState().appendStreaming(SESSION, 'Half an ans'))
    act(() => useTranscriptStore.getState().ingest(ev(2, 'error', { message: 'retrying the API (attempt 2 of 10) after overloaded_error (529)…', retryable: true, code: 'api_retry' })))
    expect(screen.getByTestId('retry-notice').textContent).toContain('attempt 2 of 10')
    expect(screen.getByTestId('streaming').textContent).toContain('Half an ans')
    expect(screen.getByTestId('composer-hint').textContent).toMatch(/Working/)
  })

  it('a Claude Code compaction says why and how big, not "0 char summary"', () => {
    seed(
      ev(1, 'compaction', { summary: '', through_index: 0, trigger: 'manual', pre_tokens: 151200 }),
      ev(2, 'compaction', { summary: 'abc', through_index: 4 }),
    )
    mount()
    const rows = screen.getAllByTestId('compaction')
    expect(rows[0].textContent).toBe('✂ Conversation compacted (manual, 151k tokens before)')
    expect(rows[1].textContent).toBe('✂ Conversation compacted (3 char summary)')
    expect(compactionText({ summary: '', through_index: 0 })).toBe('Conversation compacted')
  })
})
