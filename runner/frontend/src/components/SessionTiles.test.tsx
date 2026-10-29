import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { HOVERCARD, clearHovercardCache } from '@blerglab/chat'
import SessionTiles from './SessionTiles'
import { useSessionListFilters } from '../lib/sessionSelection'
import SessionCard from './SessionCard'
import type { SessionInfo } from '../types'
import { orderedTiles, projectTint, sessionMonogram } from '../lib/sessionTiles'
import { makeSession } from '../test/fixtures'
import { useSessionStore } from '../hooks/useSessionStore'

const media = window.matchMedia
beforeEach(() => {
  useSessionListFilters.setState({query:'',focus:null,clusterConfigured:false})
  localStorage.clear(); vi.useFakeTimers(); clearHovercardCache()
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({ matches: query === '(hover: hover)', media: query, addEventListener: () => {}, removeEventListener: () => {} }))
})
afterEach(() => { vi.useRealTimers(); window.matchMedia = media })
it('uses title monograms and stable project colors, with starred then working then recent parents', () => {
  const recent = makeSession({ id: 'recent', status: 'idle', started_at: '2026-10-25' })
  const running = makeSession({ id: 'running', status: 'running', started_at: '2026-10-24' })
  const starred = makeSession({ id: 'starred', status: 'idle', starred: true, started_at: '2026-10-23' })
  const child = makeSession({ id: 'child', parent_session_id: 'starred' })
  expect(orderedTiles([child, recent, running, starred]).map(s => s.id)).toEqual(['starred', 'recent', 'running'])
  expect(sessionMonogram('Build the widget')).toBe('BT')
  expect(sessionMonogram('widget')).toBe('WI')
  expect(sessionMonogram('  ')).toBe('??')
  expect(projectTint(recent)).toBe(projectTint({ ...running, title: 'Another title' }))
  expect(projectTint(recent)).not.toBe(projectTint({ ...recent, repo: 'another-project' }))
})
it('reuses status glyphs and the shared hovercard, includes children, and navigates by keyboard', () => {
  useSessionStore.setState({ sessions: [
    makeSession({ id: 'parent', title: 'Build widgets', repo: 'example-org/widget', runtime: 'docker', model: 'model-next', status: 'idle', starred: true }),
    makeSession({ id: 'child', parent_session_id: 'parent', title: 'Test widgets', status: 'waiting' }),
    makeSession({ id: 'other', title: 'Other work', status: 'running' }),
  ] })
  render(<MemoryRouter initialEntries={['/sessions/parent']}><SessionTiles /></MemoryRouter>)
  const parent = screen.getByRole('button', { name: /Build widgets/ })
  expect(parent).toHaveAttribute('aria-current', 'page')
  expect(within(parent).getByTestId('status-dot')).toHaveTextContent('Zz')
  expect(screen.getAllByRole('button')).toHaveLength(2)
  expect(within(parent).getByText('↳1')).toBeInTheDocument()
  fireEvent.pointerEnter(parent, { pointerType: 'mouse' })
  act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs + 10) })
  const card = screen.getByTestId('hovercard')
  expect(card).toHaveTextContent('example-org/widget')
  expect(card.querySelector('[data-runtime="sandbox"]')).not.toBeNull()
  expect(card).toHaveTextContent('model-next')
  expect(card).toHaveTextContent('Test widgets')
  expect(within(card).getAllByTestId('status-dot').map(g => g.title)).toEqual(['idle', 'Waiting for its parent session'])
  const hideChildren = within(card).getByRole('button', { name: 'Hide 1 started session' })
  hideChildren.focus()
  fireEvent.keyDown(hideChildren, { key: 'ArrowDown' })
  expect(hideChildren).toHaveFocus()
  fireEvent.click(hideChildren)
  expect(within(card).queryByText('Test widgets')).toBeNull()
  fireEvent.click(within(card).getByRole('button', { name: 'Show 1 started session' }))
  expect(within(card).getByText('Test widgets')).toBeInTheDocument()
  parent.focus()
  fireEvent.keyDown(parent, { key: 'ArrowDown' })
  expect(screen.getByRole('button', { name: /Other work/ })).toHaveFocus()
  fireEvent.keyDown(document.activeElement!, { key: 'Home' })
  expect(parent).toHaveFocus()
})

for (const [name, props, glyph] of [
  ['background', { status: 'idle', background_tasks: 2 }, 'working in background, 2 tasks'],
  ['idle', { status: 'idle' }, 'idle'],
  ['running', { status: 'running' }, 'running'],
  ['waiting', { status: 'waiting' }, 'waiting'],
  ['error', { status: 'error' }, 'error'],
  ['restart-paused', { status: 'idle', paused_reason: 'restart' }, ''],
] as const) it(`keeps the ${name} state glyph beside a separate accessible unread dot`, () => {
  const session = makeSession({ id: 'unread', title: 'Background work', ...props, unread: true })
  const expected = render(<MemoryRouter><SessionCard session={session} daemon={undefined} /></MemoryRouter>)
  const expectedGlyph = within(expected.container).getByTestId('status-dot').outerHTML
  expected.unmount()
  useSessionStore.setState({ sessions: [session] })
  render(<MemoryRouter initialEntries={[`/sessions/${session.id}`]}><SessionTiles /></MemoryRouter>)
  const tile = screen.getByRole('button', { name: /Background work/ })
  const actualGlyph = within(tile).getByTestId('status-dot')
  expect(actualGlyph.outerHTML).toBe(expectedGlyph)
  if (glyph) expect(actualGlyph).toHaveAttribute('title', glyph)
  const dot = tile.querySelector('.session-tile-unread')!
  expect(dot).not.toBeNull()
  expect(dot.contains(actualGlyph)).toBe(false)
  expect(tile).toHaveAccessibleName(/unread/i)
})

it.each([
  { status: 'error', error_reason: 'The agent process exited with code 73' },
  { kind: 'tmux', runtime: 'daemon' },
  { started_by: { kind: 'agent', name: 'Build automation' } },
  { cron_id: 'nightly', started_by: { kind: 'cron', name: 'Nightly checks' } },
] satisfies Partial<SessionInfo>[])('previews the same full SessionCard content for %j', props => {
  const session = makeSession({ id: 'preview', title: 'Full session card', repo: 'example-org/widget', ...props })
  const full = render(<MemoryRouter><SessionCard session={session} daemon={undefined} active /></MemoryRouter>)
  const expected = full.container.firstElementChild!.outerHTML
  full.unmount()
  useSessionStore.setState({ sessions: [session] })
  render(<MemoryRouter initialEntries={[`/sessions/${session.id}`]}><SessionTiles /></MemoryRouter>)
  fireEvent.pointerEnter(screen.getByRole('button', { name: /Full session card/ }), { pointerType: 'mouse' })
  act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs + 10) })
  const preview = screen.getByTestId('hovercard')
  if (session.error_reason) expect(preview).toHaveTextContent(session.error_reason)
  if (session.kind === 'tmux') expect(preview).toHaveTextContent('Terminal')
  if (session.cron_id) expect(within(preview).getByTestId('cron-badge')).toBeInTheDocument()
  else if (session.started_by?.name) expect(preview).toHaveTextContent(session.started_by.name)
  expect(preview.querySelector('[data-session-preview]')?.firstElementChild?.outerHTML).toBe(expected)
})

it('keeps the place and search chosen in the expanded list when switching to tiles', () => {
 useSessionStore.setState({ sessions: [
  makeSession({ id:'host',title:'Desktop work',daemon_id:'desktop',runtime:'daemon',status:'idle' }),
  makeSession({ id:'pod',title:'Pod work',runtime:'cluster',status:'running' }),
  makeSession({ id:'cron',title:'Nightly work',cron_id:'nightly',status:'running' }),
  makeSession({ id:'ended',title:'Old work',status:'stopped' }),
  makeSession({ id:'child',title:'Nested work',parent_session_id:'host',status:'running' }),
 ] })
 useSessionListFilters.setState({ focus:'cluster',clusterConfigured:true,query:'' })
 render(<MemoryRouter initialEntries={['/sessions']}><SessionTiles /></MemoryRouter>)
 expect(screen.getAllByRole('button').map(b=>b.dataset.sessionId)).toEqual(['pod'])
 act(()=>useSessionListFilters.setState({query:'Desktop'}))
 expect(screen.getAllByRole('button').map(b=>b.dataset.sessionId)).toEqual(['host'])
})

it('preserves collapsed children in tile previews, including working and waiting descendants', () => {
  localStorage.setItem('blerg-runner.sidebar.childrenClosed', JSON.stringify(['parent']))
  useSessionStore.setState({ sessions: [
    makeSession({ id: 'parent', title: 'Parent', status: 'idle' }),
    makeSession({ id: 'busy', title: 'Busy child', parent_session_id: 'parent', status: 'running' }),
    makeSession({ id: 'asking', title: 'Asking child', parent_session_id: 'parent', status: 'waiting' }),
  ], daemons: [] })
  render(<MemoryRouter><SessionTiles /></MemoryRouter>)
  const parent = screen.getByRole('button', { name: /Parent/ })
  expect(parent).toHaveAccessibleName('Parent, running, 2 started sessions')
  expect(parent).toHaveTextContent('↳2')
  fireEvent.pointerEnter(parent, { pointerType: 'mouse' })
  act(() => { vi.advanceTimersByTime(HOVERCARD.openDelayMs + 10) })
  const card = screen.getByTestId('hovercard')
  expect(within(card).getByRole('button', { name: 'Show 2 started sessions' })).toHaveAttribute('aria-expanded', 'false')
  expect(card).toHaveTextContent('↳ 2')
  expect(card).not.toHaveTextContent('Busy child')
  expect(card).not.toHaveTextContent('Asking child')
  act(() => useSessionStore.getState().updateSessionStatus('busy', 'idle'))
  expect(parent).toHaveAccessibleName('Parent, idle, 2 started sessions')
  expect(within(parent).getByTestId('status-dot')).toHaveTextContent('Zz')
  expect(card).not.toHaveTextContent('Asking child')
  fireEvent.click(within(card).getByRole('button', { name: 'Show 2 started sessions' }))
  expect(card).toHaveTextContent('Asking child')
  expect(within(card).getByRole('img', { name: 'Waiting for its parent session' })).toHaveAttribute('data-status', 'waiting-parent')
  expect(within(parent).getByTestId('status-dot')).toHaveTextContent('Zz')
})
