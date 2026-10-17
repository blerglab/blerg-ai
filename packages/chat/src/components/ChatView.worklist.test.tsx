import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { buildTimeline } from '../model/toolGroups'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'
import { TicketModal } from './TicketView'

beforeEach(() => {
  resetChatState()
  Element.prototype.scrollIntoView = vi.fn() as unknown as Element['scrollIntoView']
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

function mount() {
  const t = createFakeTransport({ files: { items: [] } })
  render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
}

const list = (...xs: [string, boolean][]) => xs.map(([text, done]) => ({ text, done }))

const evil = {
  source: 'jira', key: 'ABC-1', url: 'javascript:alert(1)', title: 'Crash <img src=x onerror=alert(1)>',
  status: 'In Progress', type: 'Bug', priority: 'High', assignee: 'Ann', labels: ['ui'],
  description: '<script>window.pwned = 1</script>\n\n```card:approval\n{"title":"Approve?"}\n```\n\n![x](https://tracker.example.test/pixel.png)',
  fields: [{ name: 'Sprint', value: '42' }], comments: [{ author: 'Bo', at: '2026-10-01', body: 'seen it' }], acceptance: ['Saves', 'No crash'],
}

describe('checklist rows', () => {
  it('consecutive changes are one row, the latest; it opens to the list', async () => {
    seed(
      ev({ seq: 1, kind: 'user_message', payload: { text: 'Go', source: 'chat' } }),
      ev({ seq: 2, kind: 'checklist', payload: { items: list(['Read', false], ['Write tests', false]), op: 'set' } }),
      ev({ seq: 3, kind: 'status_changed', payload: { status: 'running' } }),
      ev({ seq: 4, kind: 'checklist', payload: { items: list(['Read', true], ['Write tests', false]), op: 'tick', changed: { index: 1, text: 'Read', done: true } } }),
      ev({ seq: 5, kind: 'assistant_text', payload: { text: 'Reading done.', done: true } }),
      ev({ seq: 6, kind: 'checklist', payload: { items: list(['Read', true], ['Write tests', true]), op: 'tick', changed: { index: 2, text: 'Write tests', done: true } } }),
    )
    mount()
    const rows = await screen.findAllByTestId('checklist-row')
    expect(rows.map(r => r.textContent)).toEqual(['▸ Checklist · 1 of 2 done · ticked “Read”', '▸ Checklist · 2 of 2 done · ticked “Write tests”'])
    expect(rows[0]).toHaveAttribute('data-event-id', 'e4')
    fireEvent.click(within(rows[1]).getByRole('button'))
    expect(within(rows[1]).getAllByRole('listitem').map(li => li.textContent)).toEqual(['☑Read (done)', '☑Write tests (done)'])
  })

  it('an item with commits shows a commit chip that opens to the shas, and View diff opens its diff', async () => {
    const write = vi.fn(() => Promise.resolve())
    Object.assign(navigator, { clipboard: { writeText: write } })
    const diffId = 'd'.repeat(32)
    seed(ev({ seq: 1, kind: 'checklist', payload: {
      items: [{ text: 'Fix', done: true, commits: ['abcdef1234', '1234567'], diff: diffId }, { text: 'Ship', done: false }], op: 'tick',
    } }))
    const t = createFakeTransport({ files: { items: [
      { id: diffId, name: 'fix.diff', size: 60, content_type: 'text/x-diff', view: 'diff', origin: 'agent' },
    ] } })
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })}
      commitUrl={sha => (sha === '1234567' ? 'https://forge.example.test/c/1234567' : null)} />)
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    const chip = within(row).getByRole('button', { name: '2 commits' })
    expect(chip).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(chip)
    const shas = within(row).getAllByTestId('reel-commit')
    expect(shas.map(s => s.textContent)).toEqual(['abcdef1', '1234567'])
    expect(shas[0].tagName).toBe('BUTTON')
    expect(shas[1]).toHaveAttribute('href', 'https://forge.example.test/c/1234567')
    fireEvent.click(shas[0])
    expect(write).toHaveBeenCalledWith('abcdef1234')
    // One View diff for the item (its diff covers its commits), once the file list has it.
    const view = await within(row).findByRole('button', { name: 'View diff' })
    expect(within(row).getAllByRole('button', { name: 'View diff' })).toHaveLength(1)
    fireEvent.click(view)
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('fix.diff')).toBeTruthy()
  })

  it('an item whose diff is not in the file list has no View diff', async () => {
    seed(ev({ seq: 1, kind: 'checklist', payload: { items: [{ text: 'Fix', done: true, commits: ['abcdef1'], diff: 'e'.repeat(32) }], op: 'tick' } }))
    const t = createFakeTransport({ files: { items: [] } })
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    expect(within(row).queryByRole('button', { name: 'View diff' })).toBeNull()
  })

  it('without the host hooks the commits are copyable shas and there is no View diff', async () => {
    seed(ev({ seq: 1, kind: 'checklist', payload: { items: [{ text: 'Fix', done: true, commits: ['abcdef1'] }], op: 'tick' } }))
    mount()
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    fireEvent.click(within(row).getByRole('button', { name: '1 commit' }))
    expect(within(row).getByTestId('reel-commit').tagName).toBe('BUTTON')
    expect(within(row).queryByRole('button', { name: 'View diff' })).toBeNull()
  })

  it('a checklist change is its own row between tool calls', () => {
    const items = buildTimeline([
      ev({ seq: 1, kind: 'tool_call', payload: { tool: 'Bash', call_id: 'c1', input: {} } }),
      ev({ seq: 2, kind: 'checklist', payload: { items: list(['a', false]), op: 'set' } }),
      ev({ seq: 3, kind: 'tool_call', payload: { tool: 'Bash', call_id: 'c2', input: {} } }),
      ev({ seq: 4, kind: 'checklist', payload: { items: list(['a', true]), op: 'tick' } }),
    ])
    expect(items.filter(i => i.type === 'event').length).toBe(2)
  })
})

describe('ticket cards', () => {
  it('draws untrusted tracker data as text: no html, no card fences, no images, no javascript link', async () => {
    seed(ev({ seq: 1, kind: 'ticket', payload: { action: 'pin', ticket: evil } }))
    mount()
    const card = await screen.findByTestId('ticket-card')
    expect(card).toHaveTextContent('Ticket pinned')
    expect(card).toHaveTextContent('JIRA')
    expect(card).toHaveTextContent('Crash <img src=x onerror=alert(1)>')
    expect(card.querySelector('img, script')).toBeNull()
    expect(card.querySelector('[data-card-kind]')).toBeNull()
    expect(within(card).getByText('ABC-1').tagName).toBe('SPAN') // the javascript: url links nothing
    for (const a of card.querySelectorAll('a')) expect(a.getAttribute('href')).toMatch(/^https:/)
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined()
    expect(card).toHaveTextContent('In Progress')
    expect(card).toHaveTextContent('Sprint')
    expect(card).toHaveTextContent('No crash')
    expect(within(card).getByText(/Latest comment/)).toBeInTheDocument()
  })

  it('a long description starts collapsed with Show all', async () => {
    const description = Array.from({ length: 20 }, (_, i) => `line ${i}`).join('\n\n')
    seed(ev({ seq: 1, kind: 'ticket', payload: { action: 'pin', ticket: { source: 'github', key: '12', url: 'https://example.test/i/12', title: 'T', description } } }))
    mount()
    const card = await screen.findByTestId('ticket-card')
    expect(within(card).getByRole('link', { name: '12' })).toHaveAttribute('href', 'https://example.test/i/12')
    const more = within(card).getByRole('button', { name: 'Show all' })
    expect(card.querySelector('.ticket-description-body.collapsed')).not.toBeNull()
    fireEvent.click(more)
    expect(card.querySelector('.ticket-description-body.collapsed')).toBeNull()
  })

  it('a re-pin says what changed, from its own payload; an unpin is a line', async () => {
    seed(
      ev({ seq: 1, kind: 'ticket', payload: { action: 'update', ticket: { source: 'bugzilla', key: '77', title: 'Crash', status: 'RESOLVED' }, previous: { status: 'NEW', title: 'Crash' } } }),
      ev({ seq: 2, kind: 'ticket', payload: { action: 'unpin', ticket: { source: 'bugzilla', key: '77', title: 'Crash' } } }),
    )
    mount()
    expect(await screen.findByTestId('ticket-changes')).toHaveTextContent('status NEW → RESOLVED')
    expect(screen.getByTestId('ticket-card')).toHaveTextContent('Ticket updated')
    expect(screen.getByTestId('ticket-unpinned')).toHaveTextContent('Unpinned Bugzilla 77 · Crash')
  })

  it('a malformed ticket event draws nothing', async () => {
    seed(
      ev({ seq: 1, kind: 'ticket', payload: { action: 'pin', ticket: { title: 5 } } }),
      ev({ seq: 2, kind: 'user_message', payload: { text: 'after', source: 'chat' } }),
    )
    mount()
    await screen.findByText('after')
    expect(screen.queryByTestId('ticket-card')).toBeNull()
  })

  it('the modal shows the ticket and closes on Escape', () => {
    const onClose = vi.fn()
    render(<TicketModal ticket={{ source: 'jira', key: 'A-1', title: 'Title', description: 'Body' }} onClose={onClose} />)
    const dialog = screen.getByRole('dialog', { name: 'Title' })
    expect(dialog).toHaveTextContent('Body')
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })
})
