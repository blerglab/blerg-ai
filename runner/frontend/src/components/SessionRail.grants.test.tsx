// Owner ruling (#132): in the session panel ↳ means "runs inside Main". A child-session approval in
// Needs you wears the fork ⑂ ("can start sessions"), as the permission chips do, never ↳.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
beforeEach(() => {
  localStorage.clear()
  apiFetch.mockReset()
  apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({}) })
})

describe('the child-session approval in Needs you', () => {
  it('wears ⑂, never ↳, with its words beside it', () => {
    render(
      <MemoryRouter>
        <SessionRail id="rail" session={makeSession()} live={makeLive()} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={() => {}}
          delegationRequests={[{ request_id: 'r1', engine: 'codex', title: 'Port the parser', expires_at: new Date(Date.now() + 60_000).toISOString() }]} />
      </MemoryRouter>,
    )
    const needs = screen.getByTestId('rail-needs')
    const glyph = within(needs).getByTestId('rail-child-approval-glyph')
    expect(glyph.textContent).toBe('⑂')
    expect(glyph.getAttribute('aria-hidden')).toBe('true')
    expect(glyph.getAttribute('title')).toBe('Can start sessions')
    expect(glyph.parentElement?.textContent).toContain('Approve started session: Port the parser')
    expect(needs.textContent).not.toContain('↳')
  })
})
