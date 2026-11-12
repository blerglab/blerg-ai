import { browserWidths } from '../test/browserWidths'
import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import { TopicList } from './HistoryTab'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({
  ...(await original<typeof import('../authClient')>()),
  getAccessToken: () => 'tok', redirectToRefresh: vi.fn(),
}))
vi.mock('../claims', async original => ({ ...(await original<typeof import('../claims')>()), tokenExpired: () => false }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })) }))

afterEach(cleanup)
for (const width of browserWidths([390, 3840])) {
  it(`shows a structured review at ${width}px`, async () => {
    await page.viewport(width, 900)
    const now = new Date()
    render(<div style={{ width: 340, maxWidth: '100%' }}><TopicList sessionId="session-a" now={now} missing={null} onJump={vi.fn()} onOpenArtifact={vi.fn()} view={{ status: 'ready', reply: { session_id: 'session-a', through_seq: 1, generated_at: now.toISOString(), tokens_hidden: false, topics: [], unmarked: null, strokes: [{ id: 'review-a', kind: 'review', source: 'mark', by: 'codex', text: 'Review of a long change with several findings', verdict: 'changes', findings: ['high', 'medium'], target: 'abcdef1', round: 2, at: now.toISOString(), seq: 1, jump: null }] } }} /></div>)
    expect(screen.getByTestId('stroke')).toHaveAttribute('data-kind', 'review')
    expect(screen.getByTestId('stroke-text')).toHaveTextContent('codex · changes · round 2')
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
}
