import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { ArtifactInfo, SessionMeta } from '../transport/types'
import type { AgentEvent, ReviewRequestPayload } from '../types'
import { reviewForArtifact, reviewRequestStates } from '../model/reviewRequests'

const running: SessionMeta = meta({ status: 'running', started_at: '2026-10-20T00:00:00Z' })
const ev = (seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent => mk({ seq, kind, ts: '2026-10-20T00:00:00Z', payload })

const DOC: ArtifactInfo = { id: 'a2', name: 'SECURITY.md', size: 20, content_type: 'text/markdown', view: 'markdown', origin: 'agent', version: 2, latest_version: 2 }
const REQ: ReviewRequestPayload = {
  request_id: 'r1', artifact_id: 'a2', artifact_name: 'SECURITY.md', artifact_version: 2, artifact_view: 'markdown',
  ask: 'OK to publish this policy?', state: 'pending', created_at: '2026-10-20T00:00:00Z',
}

function mount(opts: { canDecide: boolean; list?: ReviewRequestPayload[]; readOnly?: boolean }) {
  const t = createFakeTransport({ files: { items: [DOC] } })
  t.files!.serve('# Security\n\nReport issues privately.\n')
  const decide = vi.fn(async (_id: string, decision: 'approve' | 'request_changes', note?: string): Promise<ReviewRequestPayload> =>
    ({ ...REQ, state: decision === 'approve' ? 'approved' : 'changes_requested', ...(note ? { note } : {}) }))
  const reviewRequests = vi.fn(async () => ({ requests: opts.list ?? [REQ], canDecide: opts.canDecide }))
  Object.assign(t.transport, { reviewRequests, decideReview: decide })
  render(<ChatView session={SESSION} transport={t.transport} meta={running} readOnly={opts.readOnly} />)
  return { decide, reviewRequests }
}

const seedRequest = () => seed(ev(1, 'artifact', DOC), ev(2, 'review_requested', REQ))

describe('review requests', () => {
  beforeEach(() => resetChatState())

  it('shows a compact card with the ask, and Open shows the file with the review bar', async () => {
    seedRequest()
    mount({ canDecide: true })
    const card = await screen.findByTestId('review-request-card')
    expect(card.textContent).toContain('Review requested: SECURITY.md · v2')
    expect(within(card).getByTestId('review-request-ask').textContent).toBe('OK to publish this policy?')
    fireEvent.click(within(card).getByRole('button', { name: 'Open' }))
    const bar = await screen.findByTestId('review-request-bar')
    expect(bar.textContent).toContain('OK to publish this policy?')
    expect(within(bar).getByRole('button', { name: 'Approve' })).toBeTruthy()
    expect(within(bar).getByRole('button', { name: 'Request changes' })).toBeTruthy()
  })

  it('approves from the bar, and the card shows the outcome once decided', async () => {
    seedRequest()
    const { decide } = mount({ canDecide: true })
    fireEvent.click(within(await screen.findByTestId('review-request-card')).getByRole('button', { name: 'Open' }))
    fireEvent.click(within(await screen.findByTestId('review-request-bar')).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(decide).toHaveBeenCalledWith('r1', 'approve', undefined))
    await waitFor(() => expect(screen.getByTestId('review-request-bar').textContent).toContain('Approved'))
    expect(within(screen.getByTestId('review-request-bar')).queryByRole('button', { name: 'Approve' })).toBeNull()
    act(() => seed(ev(3, 'review_decided', { ...REQ, state: 'approved' })))
    await waitFor(() => expect(screen.getByTestId('review-request-outcome').textContent).toContain('Approved'))
  })

  it('requests changes with a note', async () => {
    seedRequest()
    const { decide } = mount({ canDecide: true })
    fireEvent.click(within(await screen.findByTestId('review-request-card')).getByRole('button', { name: 'Open' }))
    const bar = await screen.findByTestId('review-request-bar')
    fireEvent.click(within(bar).getByRole('button', { name: 'Request changes' }))
    fireEvent.change(within(bar).getByLabelText('What should change (optional)'), { target: { value: 'Add a disclosure window' } })
    fireEvent.click(within(bar).getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(decide).toHaveBeenCalledWith('r1', 'request_changes', 'Add a disclosure window'))
    await waitFor(() => expect(screen.getByTestId('review-request-bar').textContent).toContain('Changes requested'))
    expect(screen.getByTestId('review-request-bar').textContent).toContain('Add a disclosure window')
  })

  it('offers no decision to someone who is not the owner, or in a read-only chat', async () => {
    seedRequest()
    mount({ canDecide: false })
    fireEvent.click(within(await screen.findByTestId('review-request-card')).getByRole('button', { name: 'Open' }))
    const bar = await screen.findByTestId('review-request-bar')
    expect(bar.textContent).toContain("Waiting for the session owner's decision")
    expect(within(bar).queryByRole('button', { name: 'Approve' })).toBeNull()
  })

  it('shows no bar for a file without an explicit request', async () => {
    seed(ev(1, 'artifact', DOC))
    mount({ canDecide: true, list: [] })
    fireEvent.click(await screen.findByRole('button', { name: /View/ }))
    await screen.findByTestId('artifact-viewer-overlay')
    expect(screen.queryByTestId('review-request-bar')).toBeNull()
  })

  it('a superseded request says so', async () => {
    seedRequest()
    mount({ canDecide: true, list: [{ ...REQ, state: 'superseded', superseded_by: 3 }] })
    await waitFor(() => expect(screen.getByTestId('review-request-outcome').textContent).toContain('Superseded by v3'))
  })
})

describe('review request model', () => {
  it('a decision wins over the request, and the pending request applies to its version', () => {
    const states = reviewRequestStates([ev(1, 'review_requested', REQ), ev(2, 'review_decided', { ...REQ, state: 'approved' }), ev(3, 'review_requested', REQ)])
    expect(states.get('r1')?.state).toBe('approved')
    const later = { ...REQ, request_id: 'r2', created_at: '2026-10-20T01:00:00Z' }
    expect(reviewForArtifact([{ ...REQ, state: 'withdrawn' }, later], 'a2')?.request_id).toBe('r2')
    expect(reviewForArtifact([later], 'other')).toBeNull()
    expect(reviewRequestStates([ev(1, 'review_requested', { request_id: 'x' })]).size).toBe(0)
  })
})
