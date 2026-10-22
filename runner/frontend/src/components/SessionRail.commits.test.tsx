// A Plan chip naming one commit with no published diff opens that commit's diff, generated on demand
// from the session's workspace, in Blerg's diff viewer (card #131).
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, render, screen, fireEvent, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { TransportFiles } from '@blerglab/chat'
import SessionRail, { type SessionRailProps } from './SessionRail'
import { makeSession } from '../test/fixtures'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { makeLive } from '../test/planTabFixture'

const SHA = '1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d'
const PATCH = `commit ${SHA}\nAuthor: Pat Example <pat@example.com>\nDate:   Wed, 21 Oct 2026 10:00:00 +0000\n\n    Read it on demand\n\ndiff --git a/lookup.go b/lookup.go\n--- a/lookup.go\n+++ b/lookup.go\n@@ -1 +1 @@\n-old\n+new\n`

function rail(commitDiff: TransportFiles['commitDiff'], onOpenArtifact = vi.fn()) {
  localStorage.setItem(RAIL_TAB_KEY, 'plan')
  const ops = { list: vi.fn(async () => []), raw: vi.fn(), download: vi.fn(), remove: vi.fn(), upload: vi.fn(), commit: vi.fn(async () => null), commitDiff: vi.fn(commitDiff) }
  const items = [
    { id: 'one', text: 'Read commits on demand', done: true, commits: [SHA] },
    { id: 'two', text: 'Publish the diff', done: false, commits: ['9f8e7d6c5b'], diff: 'd1' },
  ]
  const live = makeLive({ checklist: { items, eventId: 'c1', updatedAt: null } as never, files: { ops: ops as never, items: [], status: 'ready' as never, refresh: () => {} } })
  const props: SessionRailProps = {
    id: 'rail', session: makeSession({ status: 'running', repo: 'blerg' }), live, toolChanges: 0, mode: 'panel', open: true, onClose: () => {}, onOpenArtifact,
  }
  render(<MemoryRouter><SessionRail {...props} /></MemoryRouter>)
  return { ops, onOpenArtifact }
}

beforeEach(() => { localStorage.clear(); Element.prototype.scrollIntoView = vi.fn() })

describe('Plan: a commit chip with no published diff', () => {
  it('opens the commit\'s diff from the session\'s workspace, inside Blerg', async () => {
    const { ops, onOpenArtifact } = rail(async () => ({ text: PATCH }))
    const toggle = screen.queryByTestId('rail-plan-done-toggle')
    if (toggle && toggle.getAttribute('aria-expanded') === 'false') fireEvent.click(toggle)
    const chip = screen.getByRole('button', { name: 'Open the diff of commit 1a2b3c4' })
    await act(async () => { fireEvent.click(chip) })
    expect(ops.commitDiff).toHaveBeenCalledWith(expect.any(String), SHA, expect.any(AbortSignal))
    const dialog = screen.getByRole('dialog', { name: 'Commit 1a2b3c4' })
    expect(within(dialog).getByText(/From the session’s workspace · blerg/)).toBeTruthy()
    expect(dialog).toHaveTextContent('lookup.go')
    expect(onOpenArtifact).not.toHaveBeenCalled()
  })

  it('the chip with a published diff still opens that diff', async () => {
    const { ops, onOpenArtifact } = rail(async () => ({ text: PATCH }))
    fireEvent.click(screen.getByRole('button', { name: /Open the diff of commit 9f8e7d6/ }))
    expect(onOpenArtifact).toHaveBeenCalledWith('d1', undefined)
    expect(ops.commitDiff).not.toHaveBeenCalled()
  })

  it('a commit that cannot be read: a short neutral note', async () => {
    rail(async () => null)
    const toggle = screen.queryByTestId('rail-plan-done-toggle')
    if (toggle && toggle.getAttribute('aria-expanded') === 'false') fireEvent.click(toggle)
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Open the diff of commit 1a2b3c4' })) })
    expect(screen.getByTestId('commit-none')).toHaveTextContent('can’t be read from the session’s workspace right now')
  })
})
