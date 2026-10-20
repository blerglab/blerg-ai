import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent, FiledCard, ProposalDetail } from '../types'
import { readProposalView } from '../model/proposals'

const running: SessionMeta = meta({ status: 'running', started_at: '2026-10-20T00:00:00Z' })
const ev = (seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent => mk({ seq, kind, ts: '2026-10-20T00:00:00Z', payload })

const BOARD: ProposalDetail = {
  id: 'p1', kind: 'board_file', state: 'pending', target: 'Product', tool: '', title: 'Export button is stale',
  summary: 'Steps: open the list', argumentsRaw: '{"board_id":"b1"}', approvedByGrant: null, card: null, error: '',
}
const CARD: FiledCard = { id: 'c1', board_id: 'b1', board_name: 'Product', number: 42, title: 'Export button is stale', type: 'bug', priority: 'high', column: 'Backlog' }

function mount(detail: ProposalDetail | 'forbidden', props: Partial<Parameters<typeof ChatView>[0]> = {}, readOnly = false) {
  const t = createFakeTransport({})
  const decide = vi.fn(async (_id: string, action: 'approve' | 'reject', _opts?: { expected?: string; allowForSession?: boolean }): Promise<ProposalDetail> => {
    if (detail === 'forbidden') throw new Error('no')
    return action === 'approve' ? { ...detail, state: 'done', card: CARD } : { ...detail, state: 'rejected' }
  })
  const load = vi.fn(async (): Promise<ProposalDetail> => {
    if (detail === 'forbidden') throw Object.assign(new Error('proposal not found'), { status: 404 })
    return detail
  })
  Object.assign(t.transport, { proposal: load, decideProposal: decide })
  render(<ChatView session={SESSION} transport={t.transport} meta={running} readOnly={readOnly} {...props} />)
  return { decide, load }
}

const seedRequest = (kind = 'board_file') => seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'proposal_request', { proposal_id: 'p1', kind }))

describe('the proposal card', () => {
  beforeEach(() => resetChatState())

  it('shows the kind, the target, the title and summary, and the four actions', async () => {
    seedRequest()
    mount(BOARD, { onEditProposal: () => {} })
    const card = await screen.findByTestId('proposal-card')
    await waitFor(() => expect(card.textContent).toContain('Export button is stale'))
    expect(card.textContent).toContain('This session wants to file a card on Product')
    expect(card.textContent).toContain('Steps: open the list')
    expect(within(card).getAllByRole('button').map(b => b.textContent)).toEqual(['Approve', 'Edit', 'Reject', 'Allow for this session'])
  })

  it('approves the exact draft reviewed, then shows the created card with Open', async () => {
    seedRequest()
    const open = vi.fn()
    const { decide } = mount(BOARD, { onOpenBoardCard: open })
    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(screen.getByTestId('proposal-ticket')).toBeTruthy())
    expect(decide).toHaveBeenCalledWith('p1', 'approve', { expected: BOARD.argumentsRaw })
    const ticket = screen.getByTestId('proposal-ticket')
    expect(ticket.textContent).toContain('#42')
    expect(ticket.textContent).toContain('Backlog')
    expect(ticket.textContent).toContain('bug')
    expect(ticket.textContent).toContain('high')
    fireEvent.click(within(ticket).getByRole('button', { name: 'Open' }))
    expect(open).toHaveBeenCalledWith(CARD)
    expect(screen.getByTestId('proposal-outcome').textContent).toContain('Card created')
    expect(screen.queryByTestId('proposal-actions')).toBeNull()
  })

  it('allows the scope for the session', async () => {
    seedRequest()
    const { decide } = mount(BOARD)
    fireEvent.click(await screen.findByRole('button', { name: 'Allow for this session' }))
    await waitFor(() => expect(decide).toHaveBeenCalledWith('p1', 'approve', { expected: BOARD.argumentsRaw, allowForSession: true }))
  })

  it('rejects, and says no card was created', async () => {
    seedRequest()
    mount(BOARD)
    fireEvent.click(await screen.findByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(screen.getByTestId('proposal-outcome').textContent).toContain('Rejected · no card was created'))
    expect(screen.queryByTestId('proposal-ticket')).toBeNull()
  })

  it('opens the host editor for Edit', async () => {
    seedRequest()
    const edit = vi.fn()
    mount(BOARD, { onEditProposal: edit })
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
    expect(edit).toHaveBeenCalledWith('p1')
  })

  it('names an MCP tool and offers no Edit', async () => {
    seedRequest('mcp')
    mount({ ...BOARD, kind: 'mcp', target: 'Mail', tool: 'send_mail', title: 'send_mail: to=a@example.com', summary: '' }, { onEditProposal: () => {} })
    const card = await screen.findByTestId('proposal-card')
    await waitFor(() => expect(card.textContent).toContain('This session wants to run send_mail on Mail'))
    expect(within(card).queryByRole('button', { name: 'Edit' })).toBeNull()
  })

  it('a decision by a session permission shows its outcome and the created card for everyone', async () => {
    seedRequest()
    const manage = vi.fn()
    mount('forbidden', { onManagePermissions: manage })
    act(() => seed(ev(3, 'proposal_decided', { proposal_id: 'p1', kind: 'board_file', state: 'done', auto: true, grant_id: 'g1', card: CARD })))
    await waitFor(() => expect(screen.getByTestId('proposal-outcome').textContent).toContain('approved by a session permission'))
    expect(screen.getByTestId('proposal-ticket').textContent).toContain('Export button is stale')
    fireEvent.click(screen.getByRole('button', { name: 'Permissions' }))
    expect(manage).toHaveBeenCalledOnce()
  })

  it('tells someone who is not the owner that it waits for the owner, with no actions', async () => {
    seedRequest()
    mount('forbidden')
    await waitFor(() => expect(screen.getByTestId('proposal-card').textContent).toContain("Waiting for the session owner's decision"))
    expect(screen.queryByTestId('proposal-actions')).toBeNull()
  })

  it('shows failed and unknown outcomes without a ticket', async () => {
    seedRequest()
    mount({ ...BOARD, state: 'unknown', error: 'The filing could not be confirmed.' })
    await waitFor(() => expect(screen.getByTestId('proposal-outcome').textContent).toContain('Outcome unknown'))
    expect(screen.getByTestId('proposal-outcome').textContent).toContain('could not be confirmed')
    expect(screen.queryByTestId('proposal-ticket')).toBeNull()
  })

  it('is read-only in a read-only chat', async () => {
    seedRequest()
    mount(BOARD, {}, true)
    await waitFor(() => expect(screen.getByTestId('proposal-title')).toBeTruthy())
    expect(screen.queryByTestId('proposal-actions')).toBeNull()
  })
})

describe('the proposal card, fix round 1', () => {
  beforeEach(() => resetChatState())

  it('says why a session permission did not approve it (its hourly limit)', async () => {
    seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'proposal_request', { proposal_id: 'p1', kind: 'board_file', note: 'The session permission already approved 20 in the last hour (its limit), so this one waits for you.' }))
    mount(BOARD)
    await waitFor(() => expect(screen.getByTestId('proposal-note').textContent).toContain('its limit'))
  })

  it('after a refused approval with Allow for this session, says the permission stands and offers to revoke it', async () => {
    seedRequest()
    const manage = vi.fn()
    const t = mount(BOARD, { onManagePermissions: manage })
    t.decide.mockRejectedValueOnce(Object.assign(new Error('Board refused the draft. The permission for this session was kept.'), { status: 422, permissionKept: true }))
    fireEvent.click(await screen.findByRole('button', { name: 'Allow for this session' }))
    const err = await screen.findByTestId('proposal-error')
    expect(err.textContent).toContain('was kept')
    fireEvent.click(within(err).getByRole('button', { name: 'Revoke in Permissions' }))
    expect(manage).toHaveBeenCalledOnce()
  })
})

describe('readProposalView', () => {
  it('reads a board filing view, its created card and a grant', () => {
    const v = readProposalView({ id: 'p1', kind: 'board_file', state: 'done', arguments_raw: '{}', connection_name: 'board-file', tool: 'file_ticket',
      arguments: { board_id: 'b1', board_name: 'Product', title: 'T', body: 'x'.repeat(300) }, approved_by_grant: 'g1',
      result: { id: 'c1', board_id: 'b1', number: 3, column: 'Backlog' } })
    expect(v.target).toBe('Product')
    expect(v.summary.length).toBe(241)
    expect(v.card?.number).toBe(3)
    expect(v.approvedByGrant).toBe('g1')
  })
})
