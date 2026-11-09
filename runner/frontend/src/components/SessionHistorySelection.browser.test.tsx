import { browserWidths } from '../test/browserWidths'
import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter, useLocation } from 'react-router-dom'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { useSessionListFilters } from '../lib/sessionSelection'
import { makeDaemon, makeSession } from '../test/fixtures'
import { setChildrenExpanded } from '../lib/sidebarPrefs'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => new Response('{}')) }))
afterEach(() => { localStorage.clear(); useSessionListFilters.getState().setQuery(''); useSessionListFilters.getState().setPlace(null) })

function Location() { return <output data-testid="location">{useLocation().pathname}</output> }

it.each(browserWidths([390, 3840]).flatMap(width => [false, true].map(worker => ({ width, worker }))))('keeps an opened coordinator and its ended children under History at $width px (worker=$worker)', async ({ width, worker }) => {
  await page.viewport(width, 900)
  localStorage.clear()
  setChildrenExpanded('coordinator', false)
  useSessionListFilters.getState().setPlace(null)
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [
    makeSession({ id: 'live', title: 'Current work' }),
    makeSession({ id: 'coordinator', title: 'Previous coordinator', status: 'ended' }),
    makeSession({ id: 'child', title: 'Previous child', parent_session_id: 'coordinator', status: 'ended', started_by: worker ? { kind: 'session' } : undefined }),
  ] })
  render(<MemoryRouter initialEntries={['/sessions/live']}><SessionList variant="sidebar" /><Location /></MemoryRouter>)
  const history = screen.getByRole('button', { name: /History/ })
  fireEvent.click(history)
  const section = history.parentElement!
  fireEvent.click(within(section).getByText('Previous coordinator'))
  await expect.poll(() => screen.getByTestId('location').textContent).toBe('/sessions/coordinator')
  expect(within(section).getByText('Previous coordinator')).toBeVisible()
  expect(within(section).getByTestId('hidden-children')).toHaveAttribute('aria-label', worker ? '1 worker' : '1 channel')
  expect(within(section).getByTestId('status-dot')).toHaveAttribute('title', 'ended')
  expect(document.querySelector('[data-session-branch="coordinator"] [data-active="true"]')).not.toBeNull()
})
