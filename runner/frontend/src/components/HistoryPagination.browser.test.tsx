import { expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { TopicList } from './HistoryTab'
import { reelReply } from '../test/reelFixture'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))

it.each([390, 3840].flatMap(width => ['earlier', 'newer'].map(direction => ({ width, direction }))))(
  'keeps keyboard focus through the $direction endpoint at $width px', async ({ width, direction }) => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    const now = new Date('2026-10-15T12:00:00Z')
    const reply = reelReply(now)
    reply.topics = []
    reply.unmarked = null
    reply.strokes = Array.from({ length: 201 }, (_, i) => ({
      id: `focus-${i}`, kind: 'finding', source: 'mark', text: `Finding ${i}`,
      seq: 1000 - i, at: now.toISOString(), jump: null,
    }))
    render(<TopicList sessionId="sess-a" view={{ status: 'ready', reply }} now={now}
      missing={null} onJump={() => {}} onOpenArtifact={() => {}} />)
    if (direction === 'newer') {
      await userEvent.click(screen.getByRole('button', { name: /Show earlier strokes/ }))
      await userEvent.click(screen.getByRole('button', { name: /Show earlier strokes/ }))
    }
    const button = screen.getByRole('button', { name: new RegExp(`Show ${direction} strokes`) })
    button.focus()
    await userEvent.keyboard('{Enter}')
    // In the middle window, the activated control still exists and retains focus.
    expect(document.activeElement).toBe(button)
    expect(screen.getAllByTestId('stroke')).toHaveLength(100)
    await userEvent.keyboard(' ')
    const other = direction === 'earlier' ? 'newer' : 'earlier'
    expect(document.activeElement).toBe(screen.getByRole('button', { name: new RegExp(`Show ${other} strokes`) }))
    expect(screen.queryByRole('button', { name: new RegExp(`Show ${direction} strokes`) })).toBeNull()
    expect(screen.getAllByTestId('stroke')).toHaveLength(direction === 'earlier' ? 1 : 100)
  },
)
