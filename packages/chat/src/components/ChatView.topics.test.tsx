import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { buildTimeline, isVisibleEvent } from '../model/toolGroups'
import { scrollToEvent } from './scrollToCall'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'

beforeEach(() => {
  resetChatState()
  Element.prototype.scrollIntoView = vi.fn() as unknown as Element['scrollIntoView']
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

function mount() {
  const t = createFakeTransport({ files: { items: [] } })
  render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
}

describe('topics in the transcript', () => {
  it('draws a topic start and its done as thin dividers the side panel can find', async () => {
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'Build the History tab per the spec', source: 'chat' } }),
      ev({ seq: 2, kind: 'topic', payload: { id: 'tp_1', title: 'Build the History tab', action: 'start' } }),
      ev({ seq: 3, kind: 'topic', payload: { id: 'tp_1', title: 'Build the History tab', action: 'done', outcome: 'Shipped on main' } }),
      ev({ seq: 4, kind: 'topic', payload: { id: 'tp_2', title: 'Fix the drawer', action: 'done' } }),
    )
    mount()
    const dividers = await screen.findAllByTestId('topic-divider')
    expect(dividers.map(d => d.textContent)).toEqual(['▸ Build the History tab', '✓ Done: Shipped on main', '✓ Done: Fix the drawer'])
    expect(dividers[0]).toHaveAttribute('data-event-id', 'e2')
  })

  it('marks person bubbles and artifact cards with their event id', async () => {
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'Make a plan', source: 'chat' } }),
      ev({ seq: 2, kind: 'artifact', payload: { id: 'a1', name: 'plan.md', size: 10, content_type: 'text/markdown', view: 'markdown', origin: 'agent', version: 1 } }),
    )
    mount()
    await screen.findByTestId('artifact-card')
    expect(document.querySelector('[data-event-id="e1"]')).toHaveTextContent('Make a plan')
    expect(document.querySelector('[data-event-id="e2"]')).toHaveAttribute('data-testid', 'artifact-card')
    expect(scrollToEvent('e1')).toBe(true)
    expect(scrollToEvent('e2')).toBe(true)
  })

  it('draws a live mark as a thin row the History feed jumps to, as text; a back-dated one draws nothing', async () => {
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'Ship it?', source: 'chat' } }),
      ev({ seq: 2, kind: 'mark', payload: { id: 'mk_1', kind: 'decision', by: 'person', posted_by: 'owner', text: 'Ship on Friday' } }),
      ev({ seq: 3, kind: 'mark', payload: { id: 'mk_2', kind: 'decision', by: 'agent', text: '<img src=x onerror="alert(1)">' } }),
      ev({ seq: 4, kind: 'mark', payload: { id: 'mk_3', kind: 'finding', by: 'agent', text: 'Old finding', at: '2026-01-01T00:00:00Z' } }),
      ev({ seq: 5, kind: 'mark', payload: { id: 'mk_4', kind: 'milestone', by: 'agent', text: 'Merged' } }),
      ev({ seq: 6, kind: 'mark', payload: { id: 'mk_5', kind: 'decision', by: 'person', posted_by: 'agent', text: 'Recorded ruling' } }),
    )
    mount()
    const rows = await screen.findAllByTestId('mark-row')
    expect(rows.map(r => r.textContent)).toEqual(['◆ Decision: Ship on Friday', '◇ Decision (agent): <img src=x onerror="alert(1)">', '✓ Milestone: Merged', '◇ Decision (agent): Recorded ruling'])
    expect(document.querySelector('img[src="x"]')).toBeNull()
    expect(rows[0]).toHaveAttribute('data-event-id', 'e2')
    expect(scrollToEvent('e2')).toBe(true)
  })

  it('a back-dated mark draws nothing and does not split a run of tool calls', () => {
    const run = [
      ev({ seq: 1, kind: 'tool_call', payload: { tool: 'Bash', call_id: 'c1', input: {} } }),
      ev({ seq: 2, kind: 'mark', payload: { id: 'mk_1', kind: 'finding', by: 'agent', text: 'Old', at: '2026-01-01T00:00:00Z' } }),
      ev({ seq: 3, kind: 'tool_call', payload: { tool: 'Bash', call_id: 'c2', input: {} } }),
    ]
    const items = buildTimeline(run)
    expect(items.some(i => i.type === 'event' && i.ev.kind === 'mark')).toBe(false)
    expect(items).toHaveLength(buildTimeline([run[0], run[2]]).length)
    expect(isVisibleEvent(run[1])).toBe(false)
  })

  it('a topic between two tool calls is a card of its own', () => {
    const items = buildTimeline([
      ev({ seq: 1, kind: 'tool_call', payload: { tool: 'Bash', call_id: 'c1', input: {} } }),
      ev({ seq: 2, kind: 'topic', payload: { id: 'tp_1', title: 'T', action: 'start' } }),
      ev({ seq: 3, kind: 'tool_call', payload: { tool: 'Bash', call_id: 'c2', input: {} } }),
    ])
    expect(items.some(i => i.type === 'event' && i.ev.kind === 'topic')).toBe(true)
  })
})
