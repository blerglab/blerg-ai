// Card #127: the one hovercard is on every chip of the panel — History (cards, commits, file
// versions), Plan (pinned tickets, the checklist's commit chip, diffs) and Files — on a device that
// can hover. (Live's pins and Running now: SessionRail.pins tests; the chat's: the package's.)
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, setHovercardsEnabled, type TransportFiles } from '@blerglab/chat'
import SessionRail, { type SessionRailProps } from './SessionRail'
import { TopicList } from './HistoryTab'
import { makeSession } from '../test/fixtures'
import { diffFiles, filesOf, makeLive } from '../test/planTabFixture'
import { reelReply, strokesReply } from '../test/reelFixture'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(() => Promise.resolve({ ok: false, json: () => Promise.resolve({}) })) }))

const desktop = window.matchMedia
beforeEach(() => {
  vi.useFakeTimers()
  clearHovercardCache()
  localStorage.clear()
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: query === '(hover: hover)', media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})
afterEach(() => { vi.useRealTimers(); window.matchMedia = desktop })

/** Hovers the chip and returns the card that opened. */
function hover(chip: HTMLElement): HTMLElement {
  fireEvent.pointerEnter(chip, { pointerType: 'mouse' })
  act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs + 10) })
  const card = screen.getByTestId('hovercard')
  return card
}
function leave() {
  fireEvent.keyDown(document.body, { key: 'Escape' })
  expect(screen.queryByTestId('hovercard')).toBeNull()
}

describe('History chips', () => {
  it('a card, a commit and a file version each preview on hover', () => {
    const now = new Date(2026, 9, 15, 12, 0)
    render(<TopicList sessionId="sess-a" view={{ status: 'ready', reply: strokesReply(now) }} now={now} missing={null} onJump={() => {}} onOpenArtifact={() => {}} />)
    for (const head of screen.getAllByTestId('reel-head')) if (head.getAttribute('aria-expanded') === 'false') fireEvent.click(head)
    const card = screen.getAllByTestId('stroke-card').find(c => c.textContent === '#42')!
    expect(hover(card)).toHaveAttribute('data-kind', 'card')
    expect(screen.getByTestId('hovercard-title')).toHaveTextContent('Export button is stale')
    leave()
    expect(hover(screen.getAllByTestId('reel-commit')[0])).toHaveAttribute('data-kind', 'commit')
    leave()
    leave()
    cleanup()
    render(<TopicList sessionId="sess-a" view={{ status: 'ready', reply: reelReply(now) }} now={now} missing={null} onJump={() => {}} onOpenArtifact={() => {}} />)
    fireEvent.click(within(screen.getByTestId('reel-unmarked')).getByTestId('reel-unmarked-head'))
    const version = screen.getAllByTestId('reel-chip').find(c => c.tagName === 'A')!
    expect(hover(version)).toHaveAttribute('data-kind', 'file')
  })
})

function rail(live: ReturnType<typeof makeLive>, over: Partial<SessionRailProps> = {}) {
  const props: SessionRailProps = {
    id: 'rail', session: makeSession(), live, toolChanges: 0, mode: 'panel', open: true, onClose: () => {}, onOpenArtifact: () => {}, ...over,
  }
  return render(<MemoryRouter><SessionRail {...props} /></MemoryRouter>)
}

describe('Plan and Files chips', () => {
  it('the pinned ticket, the checklist\'s commit chip and a diff preview on hover; a file row in Files too', () => {
    const SHA = 'c'.repeat(40)
    const files = filesOf(diffFiles)!
    const ops: TransportFiles = { list: async () => [], raw: vi.fn(async () => new Blob([''])), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('x') } }
    rail(makeLive({
      tickets: [{ source: 'tracker', key: 'PRJ-1', title: 'Pinned', status: 'Open', url: '', eventId: 'e1', updatedAt: 1 }],
      checklist: { eventId: 'c1', updatedAt: null, items: [{ id: 'i1', text: 'Do it', done: false, commits: [SHA], diff: 'd1' }] },
      files: { ...files, ops },
    }))
    fireEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    expect(hover(screen.getByTestId('rail-ticket-open'))).toHaveAttribute('data-kind', 'card')
    leave()
    expect(hover(screen.getByTestId('rail-plan-commit'))).toHaveAttribute('data-kind', 'commit')
    expect(screen.getByTestId('hovercard-sha')).toHaveTextContent('ccccccc')
    leave()
    expect(hover(screen.getByRole('button', { name: 'second-change.diff' }))).toHaveAttribute('data-kind', 'file')
    expect(screen.getByTestId('hovercard-name')).toHaveTextContent('second-change.diff')
    leave()
    fireEvent.click(screen.getByRole('tab', { name: /Files/ }))
    expect(hover(screen.getByRole('button', { name: 'View notes.md' }))).toHaveAttribute('data-kind', 'file')
    expect(screen.getByTestId('hovercard-name')).toHaveTextContent('notes.md')
  })

  it('a review waiting on a file says so in its card', () => {
    const files = filesOf(diffFiles)!
    rail(makeLive({ files }), { reviewRequests: [{ request_id: 'r1', artifact_id: 'n1', artifact_name: 'notes.md', artifact_version: 1, artifact_view: 'markdown', ask: 'Look', created_at: '2026-10-21T10:00:00Z' }] })
    fireEvent.click(screen.getByRole('tab', { name: /Files/ }))
    hover(screen.getByRole('button', { name: 'View notes.md' }))
    expect(screen.getByTestId('hovercard-review')).toHaveTextContent('review open')
  })
})

describe('Running now row cards and the Hover previews switch', () => {
  const live = () => makeLive({ subagents: [{ key: 'S1', title: 'Survey', type: 'Explore', model: 'claude-opus-5-5', tokens: null, doing: 'Read · a.ts', startedAt: Date.now(), background: false }] })
  it('off: the row has no card and keeps its tooltip', () => {
    setHovercardsEnabled(false)
    rail(live(), { session: makeSession({ status: 'running' }) })
    const row = screen.getByTestId('rail-subagent')
    fireEvent.pointerEnter(row, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    expect(within(row).getByTestId('rail-show')).toHaveAttribute('title')
    setHovercardsEnabled(true)
  })
  it('turned off while a row card is open: it closes, and turned back on nothing comes back or opens warm', () => {
    rail(live(), { session: makeSession({ status: 'running' }) })
    const row = screen.getByTestId('rail-subagent')
    fireEvent.pointerEnter(row, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs + 10) })
    expect(screen.getByTestId('hovercard')).toHaveAttribute('data-kind', 'run')
    expect(within(row).getByTestId('rail-show')).toHaveAttribute('aria-describedby', screen.getByTestId('hovercard').id)
    act(() => setHovercardsEnabled(false))
    expect(screen.queryByTestId('hovercard')).toBeNull()
    act(() => setHovercardsEnabled(true))
    expect(screen.queryByTestId('hovercard')).toBeNull()
    const again = screen.getByTestId('rail-subagent')
    fireEvent.pointerLeave(again, { pointerType: 'mouse' })
    fireEvent.pointerEnter(again, { pointerType: 'mouse' })
    act(() => { vi.advanceTimersByTime(HOVERCARD.warmDwellMs + 20) })
    expect(screen.queryByTestId('hovercard')).toBeNull()
    act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs) })
    expect(screen.getByTestId('hovercard')).toBeInTheDocument()
  })
})
