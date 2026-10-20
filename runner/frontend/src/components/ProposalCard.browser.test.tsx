// The proposal card in a real browser: actions and the created ticket fit a phone and a laptop.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ChatView, type FiledCard, type ProposalDetail } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import { ev, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'

const DETAIL: ProposalDetail = {
  id: 'p1', kind: 'board_file', state: 'pending', target: 'Product roadmap and platform reliability', tool: '',
  title: 'The export button on the very long reports page shows a stale count after a filter change',
  summary: 'Steps: open reports, filter by a long project name, export; the count stays at the old value.',
  argumentsRaw: '{"board_id":"b1"}', approvedByGrant: null, card: null, error: '',
}
const CARD: FiledCard = { id: 'c1', board_id: 'b1', board_name: 'Product roadmap and platform reliability', number: 1234, title: DETAIL.title, type: 'bug', priority: 'high', column: 'Needs triage' }

beforeEach(resetChatState)
afterEach(() => { localStorage.clear() })

it.each([390, 1280])('decides a proposal in the chat and shows the created ticket at %i px', async width => {
  await page.viewport(width, 800)
  seed(ev({ seq: 1, kind: 'user_message', payload: { text: 'file it', source: 'chat' } }), ev({ seq: 2, kind: 'proposal_request', payload: { proposal_id: 'p1', kind: 'board_file' } }))
  const t = createFakeTransport()
  const decide = vi.fn(async () => ({ ...DETAIL, state: 'done' as const, card: CARD }))
  Object.assign(t.transport, { proposal: async () => DETAIL, decideProposal: decide })
  const open = vi.fn()
  render(<div style={{ height: '100vh', width: '100%', minWidth: 0 }}><ChatView session={SESSION} transport={t.transport} meta={meta()} onEditProposal={() => {}} onOpenBoardCard={open} /></div>)
  const card = await screen.findByTestId('proposal-card')
  const actions = await within(card).findByTestId('proposal-actions')
  for (const name of ['Approve', 'Edit', 'Reject', 'Allow for this session']) {
    const b = within(actions).getByRole('button', { name })
    const box = b.getBoundingClientRect()
    expect(box.height).toBeGreaterThanOrEqual(20)
    expect(box.right).toBeLessThanOrEqual(card.getBoundingClientRect().right + 0.5)
  }
  expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth)
  await userEvent.click(within(actions).getByRole('button', { name: 'Approve' }))
  const ticket = await within(card).findByTestId('proposal-ticket')
  await waitFor(() => expect(within(card).queryByTestId('proposal-actions')).toBeNull())
  expect(ticket.textContent).toContain('#1234')
  expect(ticket.textContent).toContain('Needs triage')
  expect(ticket.scrollWidth).toBeLessThanOrEqual(ticket.clientWidth + 1)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await userEvent.click(within(ticket).getByRole('button', { name: 'Open' }))
  expect(open).toHaveBeenCalledWith(CARD)
})
