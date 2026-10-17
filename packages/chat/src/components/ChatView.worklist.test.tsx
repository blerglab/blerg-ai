import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
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

  it('an item shows its short shas; with a diff, a sha opens the diff at that commit, and View diff has its counts', async () => {
    const diffId = 'd'.repeat(32)
    const sha1 = 'abcdef1234abcdef1234abcdef1234abcdef1234'
    const sha2 = '1234567890123456789012345678901234567890'
    seed(ev({ seq: 1, kind: 'checklist', payload: {
      items: [{ text: 'Fix', done: true, commits: [sha1, sha2], diff: diffId }, { text: 'Ship', done: false }], op: 'tick',
    } }))
    const patch = [
      `commit ${sha1}`, 'Author: A <a@example.test>', '', '    First', '',
      'diff --git a/x.txt b/x.txt', '--- a/x.txt', '+++ b/x.txt', '@@ -1 +1,2 @@', ' a', '+b',
      `commit ${sha2}`, 'Author: A <a@example.test>', '', '    Second', '',
      'diff --git a/y.txt b/y.txt', '--- a/y.txt', '+++ b/y.txt', '@@ -1,2 +1 @@', ' a', '-b', '',
    ].join('\n')
    const t = createFakeTransport({ files: { items: [
      { id: diffId, name: 'fix.diff', size: patch.length, content_type: 'text/x-diff', view: 'diff', origin: 'agent' },
    ] } })
    t.files!.serve(patch)
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })}
      commitUrl={() => 'https://forge.example.test/c/x'} />)
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    const shas = await within(row).findAllByTestId('reel-commit')
    expect(shas.map(s => s.textContent)).toEqual(['abcdef1', '1234567'])
    expect(row.querySelector('a[href^="https://forge"]')).toBeNull() // never a link out of Blerg
    const view = await within(row).findByRole('button', { name: /View diff/ })
    await within(view).findByText('+1')
    expect(view).toHaveTextContent('−1')
    fireEvent.click(shas[1])
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('fix.diff')).toBeTruthy()
    const heads = await within(dialog).findAllByTestId('diff-commit')
    expect(heads[1]).toHaveClass('target')
    expect(heads[0]).not.toHaveClass('target')
  })

  it('shows three shas, then "+N more" that opens in place', async () => {
    const commits = ['a1', 'b2', 'c3', 'd4', 'e5'].map(x => x.repeat(4).padEnd(7, '0'))
    seed(ev({ seq: 1, kind: 'checklist', payload: { items: [{ text: 'Fix', done: true, commits }], op: 'tick' } }))
    mount()
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    expect(within(row).getAllByTestId('reel-commit')).toHaveLength(3)
    const more = within(row).getByRole('button', { name: '+2 more' })
    fireEvent.click(more)
    expect(within(row).getAllByTestId('reel-commit')).toHaveLength(5)
    expect(within(row).queryByRole('button', { name: '+2 more' })).toBeNull()
  })

  it('an item whose diff is not in the file list has no View diff', async () => {
    seed(ev({ seq: 1, kind: 'checklist', payload: { items: [{ text: 'Fix', done: true, commits: ['abcdef1'], diff: 'e'.repeat(32) }], op: 'tick' } }))
    const t = createFakeTransport({ files: { items: [] } })
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    expect(within(row).queryByRole('button', { name: /View diff/ })).toBeNull()
  })

  it('without a diff a sha copies itself and says Copied; there is no View diff', async () => {
    const write = vi.fn(() => Promise.resolve())
    Object.assign(navigator, { clipboard: { writeText: write } })
    seed(ev({ seq: 1, kind: 'checklist', payload: { items: [{ text: 'Fix', done: true, commits: ['abcdef1234'] }], op: 'tick' } }))
    mount()
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    const chip = within(row).getByRole('button', { name: 'Copy commit abcdef1234' })
    await act(async () => { fireEvent.click(chip) })
    expect(write).toHaveBeenCalledWith('abcdef1234')
    expect(within(row).getByRole('status')).toHaveTextContent('Copied')
    expect(within(row).queryByRole('button', { name: /View diff/ })).toBeNull()
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
    expect(card).toHaveTextContent('Pinned ticket')
    expect(card).toHaveTextContent('JIRA')
    expect(card).toHaveTextContent('Crash <img src=x onerror=alert(1)>')
    expect(card.querySelector('img, script')).toBeNull()
    expect(card.querySelector('[data-card-kind]')).toBeNull()
    expect(within(card).getByText('ABC-1').tagName).toBe('SPAN') // the javascript: url links nothing
    for (const a of card.querySelectorAll('a')) expect(a.getAttribute('href')).toMatch(/^https:/)
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined()
    fireEvent.click(within(card).getByRole('button', { name: 'Open ticket' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.querySelector('img, script')).toBeNull()
    expect(dialog.querySelector('[data-card-kind]')).toBeNull()
    for (const a of dialog.querySelectorAll('a')) expect(a.getAttribute('href')).toMatch(/^https:/)
    expect(within(dialog).queryByRole('link', { name: /Open in/ })).toBeNull()
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined()
  })

  it('the card is a summary: header, title, one meta line, an excerpt; no fields, comments or criteria', async () => {
    seed(ev({ seq: 1, kind: 'ticket', payload: { action: 'pin', ticket: { ...evil, url: 'https://tracker.example.test/ABC-1', type: 'Task', priority: 'High', assignee: 'Release owner', description: 'First line\n\n## Second\n\nThird' } } }))
    mount()
    const card = await screen.findByTestId('ticket-card')
    const chip = within(card).getByTestId('ticket-status')
    expect(chip).toHaveTextContent('In Progress')
    expect(chip).toHaveAttribute('data-tone', 'active')
    expect(within(card).getByTestId('ticket-meta')).toHaveTextContent('Task · High · Release owner')
    expect(within(card).getByTestId('ticket-excerpt')).toHaveTextContent('First line')
    expect(card).not.toHaveTextContent('Sprint')
    expect(card).not.toHaveTextContent('No crash')
    expect(card).not.toHaveTextContent('seen it')
    expect(card.querySelector('.ticket-fields, .ticket-comments, .ticket-acceptance')).toBeNull()
    expect(within(card).getByRole('link', { name: 'ABC-1' })).toHaveAttribute('href', 'https://tracker.example.test/ABC-1')
  })

  it('Open ticket opens the modal; it closes and gives the focus back', async () => {
    seed(ev({ seq: 1, kind: 'ticket', payload: { action: 'pin', ticket: evil } }))
    mount()
    const card = await screen.findByTestId('ticket-card')
    const open = within(card).getByRole('button', { name: 'Open ticket' })
    open.focus()
    fireEvent.click(open)
    const dialog = screen.getByRole('dialog', { name: evil.title })
    expect(dialog).toHaveTextContent('Sprint')
    expect(dialog).toHaveTextContent('seen it')
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(open)
  })

  it('a re-pin says what changed, from its own payload; an unpin is a line', async () => {
    seed(
      ev({ seq: 1, kind: 'ticket', payload: { action: 'update', ticket: { source: 'bugzilla', key: '77', title: 'Crash', status: 'RESOLVED' }, previous: { status: 'NEW', title: 'Crash' } } }),
      ev({ seq: 2, kind: 'ticket', payload: { action: 'unpin', ticket: { source: 'bugzilla', key: '77', title: 'Crash' } } }),
    )
    mount()
    expect(await screen.findByTestId('ticket-changes')).toHaveTextContent('status NEW → RESOLVED')
    expect(screen.getByTestId('ticket-card')).toHaveTextContent('Updated')
    expect(screen.getByTestId('ticket-status')).toHaveAttribute('data-tone', 'done')
    expect(screen.getByTestId('ticket-unpinned')).toHaveTextContent('Unpinned Bugzilla 77 · Crash')
  })

  it('a canceled ticket has its title struck through', async () => {
    seed(ev({ seq: 1, kind: 'ticket', payload: { action: 'pin', ticket: { source: 'jira', key: 'K-2', title: 'Dropped', status: "Won't Do" } } }))
    mount()
    const card = await screen.findByTestId('ticket-card')
    expect(within(card).getByTestId('ticket-status')).toHaveAttribute('data-tone', 'canceled')
    expect(within(card).getByText('Dropped')).toHaveClass('canceled')
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
})

describe('ticket modal', () => {
  const full = {
    ...evil, url: 'https://tracker.example.test/ABC-1', reporter: 'Cy', labels: ['ui', 'beta'],
    description: '## Steps\n\nOpen it.',
    comments: [{ author: 'Bo Lee', at: '2026-10-01T10:00:00Z', body: '**seen** it' }, { author: '', at: 'last week', body: 'me too' }],
  }

  it('shows the ticket and closes on Escape', () => {
    const onClose = vi.fn()
    render(<TicketModal ticket={{ source: 'jira', key: 'A-1', title: 'Title', description: 'Body' }} onClose={onClose} />)
    const dialog = screen.getByRole('dialog', { name: 'Title' })
    expect(dialog).toHaveTextContent('Body')
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })

  it('has its header, then Details, Description, Acceptance criteria and Comments, in that order', () => {
    render(<TicketModal ticket={full} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog')
    const header = dialog.querySelector('header')!
    expect(within(header).getByText('JIRA')).toBeInTheDocument()
    expect(within(header).getByTestId('ticket-status')).toHaveAttribute('data-tone', 'active')
    expect(within(header).getByRole('link', { name: /Open in JIRA/ })).toHaveAttribute('href', 'https://tracker.example.test/ABC-1')
    const heads = [...dialog.querySelectorAll('.ticket-section-title')].map(h => h.textContent)
    expect(heads).toEqual(['Details', 'Description', 'Acceptance criteria', 'Comments'])
    // Details come first in the page: on a narrow screen they are on top; a wide one puts them on the side.
    expect(dialog.querySelector('.ticket-modal-grid > :first-child')).toHaveClass('ticket-side')
  })

  it('lists the details with their names, labels as chips, then the extra fields', () => {
    render(<TicketModal ticket={full} onClose={() => {}} />)
    const details = screen.getByTestId('ticket-modal-details')
    const names = [...details.querySelectorAll('dt')].map(d => d.textContent)
    expect(names).toEqual(['Type', 'Priority', 'Assignee', 'Reporter', 'Labels', 'Sprint'])
    expect(details.querySelectorAll('.ticket-label')).toHaveLength(2)
  })

  it('draws acceptance criteria as an unticked list and comments with an avatar, a name and a time', () => {
    render(<TicketModal ticket={full} onClose={() => {}} />)
    const criteria = screen.getByTestId('ticket-modal-acceptance')
    expect(within(criteria).getAllByRole('listitem').map(li => li.textContent)).toEqual(['Saves', 'No crash'])
    expect(criteria.querySelectorAll('.ticket-box')).toHaveLength(2)
    const comments = screen.getAllByTestId('ticket-comment')
    expect(comments).toHaveLength(2)
    expect(within(comments[0]).getByText('BL')).toHaveClass('ticket-avatar')
    expect(comments[0]).toHaveTextContent('Bo Lee')
    expect(comments[0].querySelector('time')).not.toBeNull()
    expect(comments[0].querySelector('strong')).toHaveTextContent('seen')
    expect(comments[1]).toHaveTextContent('last week') // a time the tracker wrote in words stays as it came
    expect(screen.getByTestId('ticket-modal-description').querySelector('.md-doc h2')).toHaveTextContent('Steps')
  })

  it('leaves out the sections the ticket has nothing for', () => {
    render(<TicketModal ticket={{ source: 'github', key: '9', title: 'Bare' }} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog')
    expect([...dialog.querySelectorAll('.ticket-section-title')].map(h => h.textContent)).toEqual([])
    expect(dialog).toHaveTextContent('No description.')
  })
})
