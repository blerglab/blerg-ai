import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, meta, resetChatState, seed } from '../test/chat'
import type { ArtifactInfo } from '../transport/types'
import type { AgentEvent } from '../types'

const plan: ArtifactInfo = { id: 'doc1', name: 'plan.md', size: 20, content_type: 'text/markdown', view: 'markdown', origin: 'agent', version: 1, latest_version: 1 }

function reviewVersion(version: number): ArtifactInfo {
  return { id: `rv${version}`, name: 'plan.md.review.json', size: 300, content_type: 'application/json', view: 'json', origin: 'agent', version, latest_version: 3 }
}

function reviewBody(statuses: Array<'open' | 'done' | 'declined'>): string {
  return JSON.stringify({
    version: 1,
    file: { name: 'plan.md', artifactId: 'doc1', artifactVersion: 1 },
    requests: statuses.map((status, i) => ({
      id: `r${i}`, anchor: { kind: 'general' }, text: `ask ${i}`, status, createdAt: '2026-07-26T00:00:00Z',
      ...(status === 'open' ? {} : { reply: `answer ${i}` }),
    })),
  })
}

let seq = 0
function event(kind: string, payload: unknown): AgentEvent {
  seq++
  return { type: 'agent_event', session_id: SESSION, client_event_id: `x${seq}`, seq, ts: '2026-07-26T00:00:00Z', kind, payload } as AgentEvent
}

function mount(bodies: Record<string, string>) {
  const listed = [plan, reviewVersion(1), reviewVersion(2), reviewVersion(3)]
  const t = createFakeTransport({ files: { items: listed } })
  const files = t.files!
  files.serve('# Plan\n\ntext')
  const raw = files.raw.bind(files)
  files.raw = async (sid: string, id: string) => (id in bodies ? (files.rawCalls.push(id), new Blob([bodies[id]])) : raw(sid, id))
  render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
  return files
}

beforeEach(() => {
  resetChatState()
  seq = 0
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('review answers in the chat', () => {
  for (const compact of [true, false]) {
    it(`draw one line per round, not a file card per reply (${compact ? 'compact' : 'full'} view)`, async () => {
      if (!compact) localStorage.setItem(COMPACT, '0')
      seed(
        event('artifact', plan),
        event('artifact', reviewVersion(1)),
        event('artifact', reviewVersion(2)),
        event('artifact', reviewVersion(3)),
      )
      mount({ rv3: reviewBody(['done', 'declined', 'done']) })
      const line = await screen.findByTestId('review-round')
      expect(line).toHaveTextContent('3 requests answered')
      expect(line).toHaveTextContent('plan.md')
      expect(screen.getAllByTestId('review-round')).toHaveLength(1)
      // the published document keeps its card; its review state draws none
      const cards = screen.getAllByTestId('artifact-card')
      expect(cards).toHaveLength(1)
      expect(cards[0]).toHaveTextContent('plan.md')
      expect(screen.queryByText(/plan\.md\.review\.json/)).toBeNull()
      localStorage.removeItem(COMPACT)
    })
  }

  it('shows no guessed or stale count while a review version loads', async () => {
    seed(event('artifact', plan), event('artifact', reviewVersion(1)))
    const t = createFakeTransport({ files: { items: [plan, reviewVersion(1), reviewVersion(2)] } })
    const pending = new Map<string, (b: Blob) => void>()
    t.files!.raw = (_sid, id) => new Promise(resolve => { pending.set(id, resolve) })
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
    const line = screen.getByTestId('review-round')
    expect(line).not.toHaveTextContent(/requests? answered/)
    await act(async () => { pending.get('rv1')!(new Blob([reviewBody(['done', 'done', 'done'])])) })
    expect(line).toHaveTextContent('3 requests answered')
    act(() => t.emit(event('artifact', reviewVersion(2))))
    const nextLine = screen.getByTestId('review-round')
    expect(nextLine).not.toHaveTextContent(/requests? answered/)
    await act(async () => { pending.get('rv2')!(new Blob([reviewBody(['done', 'done'])])) })
    expect(nextLine).toHaveTextContent('2 requests answered')
  })

  it('counts what the round answered, against the review before it', async () => {
    seed(
      event('artifact', plan),
      event('artifact', reviewVersion(1)),
      event('user_message', { text: 'Review of plan.md (v1): 1 request.', source: 'human' }),
      event('artifact', reviewVersion(2)),
      event('artifact', reviewVersion(3)),
    )
    mount({
      rv1: reviewBody(['done', 'open', 'open']),
      rv3: reviewBody(['done', 'done', 'declined']),
    })
    const lines = await screen.findAllByTestId('review-round')
    expect(lines).toHaveLength(2)
    await waitFor(() => expect(lines[1]).toHaveTextContent('2 requests answered'))
    await waitFor(() => expect(lines[0]).toHaveTextContent('1 request answered'))
  })

  it('opens the review of the reviewed file', async () => {
    seed(event('artifact', plan), event('artifact', reviewVersion(1)))
    mount({ rv1: reviewBody(['done']) })
    const line = await screen.findByTestId('review-round')
    await waitFor(() => expect(line).toHaveTextContent('1 request answered'))
    fireEvent.click(within(line).getByRole('button'))
    const dialog = await screen.findByRole('dialog', { name: /plan\.md/ })
    expect(await within(dialog).findByTestId('review-mode')).toBeInTheDocument()
  })
})
