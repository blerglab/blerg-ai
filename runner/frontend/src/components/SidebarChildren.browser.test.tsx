import { browserWidths } from '../test/browserWidths'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionList from './SessionList'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) })) }))
afterEach(() => { localStorage.clear(); useMessageStore.setState({ messages: [] }) })

const child = (id: string, title: string, status: 'idle' | 'running' | 'waiting') =>
  makeSession({ id, title, status, parent_session_id: 'parent', started_by: { kind: 'session', name: 'Parent' } })

describe('top-level sessions in the sidebar', () => {
  it.each(browserWidths([390, 1024, 3840]))('keeps two-line parent cards and counts without child cards at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const sessions = [makeSession({ id: 'parent', status: 'idle', title: 'Parent work' }), child('a', 'Idle worker', 'idle'), child('b', 'Asking worker', 'waiting'), child('c', 'Busy worker', 'running')]
    useSessionStore.setState({ daemons: [makeDaemon()], sessions })
    render(<MemoryRouter><div style={{ width: width === 390 ? '100%' : 420 }}><SessionList variant="sidebar" /></div></MemoryRouter>)
    expect(document.querySelectorAll('[data-session-branch]')).toHaveLength(1)
    expect(screen.getByTestId('hidden-children')).toHaveTextContent('↳ 3')
    expect(screen.queryByTestId('children-toggle')).toBeNull()
    for (const title of ['Idle worker', 'Asking worker', 'Busy worker']) expect(screen.queryByText(title)).toBeNull()
    const card = document.querySelector<HTMLElement>('[data-session-branch="parent"]')!
    expect(within(card).getByTestId('status-dot')).toHaveAttribute('title', 'running')
    await userEvent.click(screen.getByRole('button', { name: 'Collapse workspace list' }))
    const tile = document.querySelector<HTMLButtonElement>('[data-session-id="parent"]')!
    expect(tile).toHaveAccessibleName(/running, 3 workers/)
    expect(tile).toHaveTextContent('↳3')
    await userEvent.hover(tile)
    const preview = await screen.findByTestId('hovercard')
    expect(within(preview).queryByTestId('children-toggle')).toBeNull()
    for (const title of ['Idle worker', 'Asking worker', 'Busy worker']) expect(within(preview).queryByText(title)).toBeNull()
    act(() => useSessionStore.getState().updateSessionStatus('c', 'idle'))
    expect(tile).toHaveAccessibleName(/idle, 3 workers/)
    expect(within(tile).getByTestId('status-dot')).toHaveTextContent('Zz')
  })
})

it.each(browserWidths([390, 3840]))('keeps runtime icons first and fixed when task chips change at %ipx', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  localStorage.clear()
  const sessions = [
    makeSession({ id: 'parent', title: 'Parent work', runtime: 'daemon', kind: 'agent', repo: 'widget', model: 'model-next', status: 'running' }),
    makeSession({ id: 'child', title: 'Child work', runtime: 'daemon', kind: 'agent', repo: 'widget', model: 'model-next', status: 'running', parent_session_id: 'parent' }),
    makeSession({ id: 'other', title: 'Other repo work', runtime: 'daemon', kind: 'agent', repo: 'other-widget', model: 'model-next', status: 'running', parent_session_id: 'parent' }),
  ]
  useSessionStore.setState({ sessions, daemons: [makeDaemon()] })
  render(<MemoryRouter><div style={{ width: width === 390 ? '100%' : 420 }}><SessionList variant="sidebar" /></div></MemoryRouter>)
  const cards = sessions.filter(s => !s.parent_session_id).map(session => document.querySelector<HTMLElement>(`[data-session-branch="${session.id}"] > div`)!)
  const icons = cards.map(card => within(card).getByTestId('runtime-chip'))
  await document.fonts.ready
  const positions = icons.map(icon => icon.querySelector('.runtime-icon')!.getBoundingClientRect().x)
  for (const tasks of [0, 4, 0]) {
    act(() => useSessionStore.setState({ sessions: sessions.map(session => ({ ...session, background_tasks: tasks })) }))
    for (const [i, card] of cards.entries()) {
      const icon = within(card).getByTestId('runtime-chip')
      const row = icon.parentElement!
      expect.soft(row.firstElementChild).toBe(icon)
      expect.soft(icon.querySelector('.runtime-icon')!.getBoundingClientRect().x).toBeCloseTo(positions[i], 5)
      const items = [...row.children].map(item => item.getAttribute('data-testid'))
      expect.soft(items).toEqual(['runtime-chip', ...['session-repo'], 'model-chip', ...(tasks ? ['background-chip'] : [])])
      expect(row.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    }
  }
})

it.each(browserWidths([390, 3840]))('promotes an orphan question to the waiting banner and tile at %ipx', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const parent = makeSession({ id: 'parent', title: 'Parent', status: 'idle' })
  const child = makeSession({ id: 'child', title: 'Child question', parent_session_id: 'parent', started_by: { kind: 'session' }, status: 'waiting', unread: true })
  useSessionStore.setState({ sessions: [parent, child], daemons: [makeDaemon()] })
  render(<MemoryRouter><SessionList variant="sidebar" /></MemoryRouter>)
  expect(screen.queryByText('Child question')).toBeNull()
  expect(screen.queryByRole('img', { name: 'Waiting for Main' })).toBeNull()
  act(() => useSessionStore.setState({ sessions: [{ ...parent, archived_at: '2026-10-19T12:00:00Z' }, child] }))
  expect(screen.getByRole('img', { name: 'waiting' })).toHaveTextContent('?')
  await userEvent.click(screen.getByRole('button', { name: 'Collapse workspace list' }))
  const tile = document.querySelector<HTMLButtonElement>('[data-session-id="child"]')!
  expect(tile).toHaveAccessibleName('Child question, waiting, unread')
  expect(within(tile).getByTestId('status-dot')).toHaveTextContent('?')
})

it('keeps the phone page flat, explains child matches, and surfaces failed workers without unread dots', async () => {
  await page.viewport(390, 844)
  const parent = makeSession({ id: 'parent', title: 'Phone workspace', status: 'idle' })
  const failed = makeSession({ id: 'failed', title: 'Compiler failed', status: 'error', parent_session_id: 'parent', started_by: { kind: 'session' }, unread: true })
  const blocked = makeSession({ id: 'blocked', title: 'Review decision', status: 'waiting', parent_session_id: 'parent', started_by: { kind: 'session' }, unread: true })
  useMessageStore.setState({ messages: [{ id: 'phone-question', session_id: 'blocked', kind: 'ask', status: 'open', answerable: true, body: 'Choose', created_at: '2026-10-31T12:00:00Z' }] })
  const orphan = makeSession({ id: 'orphan', title: 'Opened worker', status: 'idle', started_by: { kind: 'session' }, unread: true })
  const channel = makeSession({ id: 'channel', title: 'Personal channel', unread: true })
  useSessionStore.setState({ sessions: [parent, failed, blocked, orphan, channel], daemons: [makeDaemon()] })
  render(<MemoryRouter><SessionList /></MemoryRouter>)
  expect(screen.queryByTestId('nested-sessions')).toBeNull()
  const card = document.querySelector<HTMLElement>('[data-session-branch="parent"]')!
  expect(within(card).getByTestId('hidden-children')).toHaveTextContent('↳ 2')
  expect(within(card).getByRole('img', { name: 'Compiler failed: failed' })).toBeVisible()
  expect(within(card).queryByTestId('unread-dot')).toBeNull()
  expect(within(card).getByRole('img', { name: 'Review decision: Needs you' })).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: /Workers and runs/ }))
  const orphanCard = screen.getByText('Opened worker').parentElement!.parentElement!
  expect(within(orphanCard).getByTestId('unread-dot')).toBeVisible()
  const channelCard = document.querySelector<HTMLElement>('[data-session-branch="channel"]')!
  expect(within(channelCard).getByTestId('unread-dot')).toBeVisible()
  const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/phone-workspace-390.png` })
  await userEvent.fill(screen.getByPlaceholderText('Search workspaces…'), 'Compiler')
  expect(screen.queryByTestId('nested-sessions')).toBeNull()
  expect(within(card).getByText(/Matches in/)).toHaveTextContent('Compiler failed')
})

it.each(browserWidths([390, 3840]))('counts a channel waiting on its person while Main is idle at %ipx', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const parent = makeSession({ id: 'parent', title: 'Main', status: 'idle' })
  const channel = makeSession({ id: 'channel', title: 'Channel question', parent_session_id: parent.id, started_by: null, status: 'waiting' })
  useSessionStore.setState({ sessions: [parent, channel], daemons: [makeDaemon()] })
  render(<MemoryRouter><SessionList variant="sidebar" /></MemoryRouter>)
  expect(screen.getByTestId('status-dot')).toHaveAccessibleName('idle')
})
