import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
beforeEach(() => {
  localStorage.clear()
  apiFetch.mockReset()
  apiFetch.mockResolvedValue({
    ok: true,
    json: async () => ({
      engine: 'claude',
      supported: true,
      session_apply: true,
      allowed_marketplaces: ['example/plugins'],
      any_marketplace: false,
      plugins: [],
      requests: [
        {
          id: 'p1',
          state: 'pending',
          arguments: { plugin: 'helper', marketplace: 'example/plugins', reason: 'Need a review skill' },
          result: null,
        },
      ],
    }),
  })
})
function show(owner: boolean) {
  return render(
    <MemoryRouter>
      <SessionRail
        id="rail"
        session={makeSession()}
        live={makeLive({ canReleasePrompt: owner })}
        toolChanges={0}
        mode="panel"
        open
        onClose={() => {}}
        onOpenArtifact={() => {}}
      />
    </MemoryRouter>,
  )
}
describe('plugin requests in the session panel', () => {
  it('counts owner requests under Needs you and offers the add form in Capabilities', async () => {
    show(true)
    const needs = screen.getByTestId('rail-needs')
    expect(await within(needs).findByRole('link', { name: 'Approve or decline helper' })).toHaveAttribute(
      'href',
      '/proposals?id=p1',
    )
    expect(within(needs).queryByText('Nothing needs you.')).toBeNull()
    fireEvent.click(screen.getByRole('tab', { name: /Capabilities/ }))
    expect(screen.getByRole('button', { name: 'Add plugin or skill' })).toBeInTheDocument()
  })
  it('does not fetch private requests or show add controls for a non-owner', () => {
    show(false)
    fireEvent.click(screen.getByRole('tab', { name: /Capabilities/ }))
    expect(screen.queryByRole('button', { name: 'Add plugin or skill' })).toBeNull()
    expect(apiFetch.mock.calls.some((c) => String(c[0]).endsWith('/plugins'))).toBe(false)
  })
})
