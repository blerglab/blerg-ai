// @vitest-environment jsdom
import { act, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import BlackboardTabs from './BlackboardTabs'
import type { SessionTabs } from '../hooks/useSessionTabs'

const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../hooks/useBlackboardDraft', () => ({ useBlackboardRevision: () => 0, useBlackboardDraft: () => null }))

it.each(['project', 'milestone', 'thrust'] as const)('fills in %s card details after showing early structure', async level => {
 let finish!: () => void
 const pending = new Promise<void>(resolve => { finish = resolve })
 read.mockImplementation((url: string) => url.includes('/slice') ? pending.then(() => new Response(JSON.stringify({ cards: url.includes('/milestones/') ? [{ card: { board_id: 'board', number: 1, title: 'Loaded work' }, membership: { thrust_id: 't', state: 'planned' }, stage: 'done' }] : [], complete: true }))) : Promise.resolve(new Response(JSON.stringify({ record: { name: 'Visible plan', vision: 'A visible structure', milestones: [{ id: 'm', name: 'Next release' }], goals: [], thrusts: [{ id: 't', name: 'Ship safely', milestone_id: 'm' }], snapshots: [], events: [] }, pending_proposals: 0 }))))
 const target = document.createElement('div'); document.body.append(target)
 const tabs = { tabs: [{ id: 'tab', view: { kind: 'blackboard', blackboardId: 'b', path: { level, milestoneId: 'm', thrustId: level === 'thrust' ? 't' : undefined } } }], visible: ['tab'], targetFor: () => target, navigateBlackboard: vi.fn() } as unknown as SessionTabs
 const app = render(<BlackboardTabs tabs={tabs} sessionId="s" />)
 try {
  expect(await screen.findByRole('heading', { name: level === 'project' ? 'Visible plan' : level === 'milestone' ? 'Next release' : 'Ship safely' }, { timeout: 1000 })).toBeTruthy()
  expect(screen.getByText('Loading card details…')).toBeTruthy()
  await act(async () => { finish() })
  expect(screen.queryByText('Loading card details…')).toBeNull()
  if (level === 'project') expect(screen.getByRole('button', { name: /Ship safely1 of 1 shipped/ })).toBeTruthy()
  if (level === 'milestone') {
   expect(screen.getByText('1 cards')).toBeTruthy()
   // The whole release in one bar, even before two days of history draw the chart.
   const summary = screen.getByTestId('blackboard-summary')
   expect(summary.textContent).toBe('1 of 1 shipped')
   expect(summary.querySelector('[role="img"]')?.getAttribute('aria-label')).toBe('1 Done, 0 To test, 0 In work, 0 Next, 0 Needs you')
  } else expect(screen.queryByTestId('blackboard-summary')).toBeNull()
  if (level === 'thrust') expect(screen.getByText('Everything else · 100% built')).toBeTruthy()
 } finally { app.unmount(); target.remove() }
})

const card = (number: number, stage: string, state = 'planned') => ({ card: { board_id: 'board', number, title: `Card ${number}` }, membership: { thrust_id: 't', state }, stage })

it.each([
 ['mixed stages', [card(1, 'done'), card(2, 'to_test'), card(3, 'in_work'), card(4, 'next', 'unplanned'), card(5, 'done', 'moved_out'), card(6, 'moved_out')], '2 of 4 shipped', '1 Done, 1 To test, 1 In work, 1 Next, 0 Needs you'],
 ['no cards', [], null, null],
])('summarizes the whole release: %s', async (_name, cards, text, stagesLabel) => {
 read.mockImplementation((url: string) => Promise.resolve(new Response(JSON.stringify(url.includes('/slice') ? { cards: url.includes('/milestones/') ? cards : [], complete: true } : { record: { name: 'Plan', milestones: [{ id: 'm', name: 'Next release' }], goals: [], thrusts: [{ id: 't', name: 'Ship safely', milestone_id: 'm' }], snapshots: [], events: [] }, pending_proposals: 0 }))))
 const target = document.createElement('div'); document.body.append(target)
 const tabs = { tabs: [{ id: 'tab', view: { kind: 'blackboard', blackboardId: 'b', path: { level: 'milestone', milestoneId: 'm' } } }], visible: ['tab'], targetFor: () => target, navigateBlackboard: vi.fn() } as unknown as SessionTabs
 const app = render(<BlackboardTabs tabs={tabs} sessionId="s" />)
 try {
  await screen.findByRole('heading', { name: 'Next release' }, { timeout: 1000 })
  await act(async () => {})
  const summary = screen.queryByTestId('blackboard-summary')
  if (text === null) { expect(summary).toBeNull(); return }
  // Moved-out cards leave the totals (by membership or by stage, e.g. a deleted card); unplanned ones still count.
  expect(summary?.textContent).toBe(text)
  expect(summary?.querySelector('[role="img"]')?.getAttribute('aria-label')).toBe(stagesLabel)
  // Keyboard users reach the stage breakdown the same way pointer users do.
  expect(summary?.getAttribute('tabindex')).toBe('0')
 } finally { app.unmount(); target.remove() }
})

it('keeps the loaded cards quiet while a refresh runs', async () => {
 let slices = 0
 let release!: () => void
 const slow = new Promise<void>(resolve => { release = resolve })
 read.mockImplementation((url: string) => {
  if (url.includes('/slice')) {
   slices++
   const respond = () => new Response(JSON.stringify({ cards: url.includes('/milestones/') ? [card(1, 'done')] : [], complete: true }))
   // The first load answers at once; the refresh waits, so its in-flight state is visible.
   return slices <= 2 ? Promise.resolve(respond()) : slow.then(respond)
  }
  return Promise.resolve(new Response(JSON.stringify({ record: { name: 'Plan', milestones: [{ id: 'm', name: 'Next release' }], goals: [], thrusts: [{ id: 't', name: 'Ship safely', milestone_id: 'm' }], snapshots: [], events: [] }, pending_proposals: 0 })))
 })
 const target = document.createElement('div'); document.body.append(target)
 const tabs = { tabs: [{ id: 'tab', view: { kind: 'blackboard', blackboardId: 'b', path: { level: 'milestone', milestoneId: 'm' } } }], visible: ['tab'], targetFor: () => target, navigateBlackboard: vi.fn() } as unknown as SessionTabs
 const app = render(<BlackboardTabs tabs={tabs} sessionId="s" />)
 try {
  await screen.findByText('1 of 1 shipped', undefined, { timeout: 1000 })
  expect(screen.queryByText('Loading card details…')).toBeNull()
  // A focus refresh re-reads everything; the cards already shown must not flash a loading line.
  await act(async () => { window.dispatchEvent(new Event('focus')); await Promise.resolve() })
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
  expect(slices).toBeGreaterThan(2)
  expect(screen.queryByText('Loading card details…')).toBeNull()
  expect(screen.getByText('1 of 1 shipped')).toBeTruthy()
  await act(async () => { release() })
 } finally { app.unmount(); target.remove() }
})
