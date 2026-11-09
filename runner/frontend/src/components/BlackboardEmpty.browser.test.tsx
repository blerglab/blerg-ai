import { act, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import { setTheme } from '../../../../packages/settings/theme'
import BlackboardTabs from './BlackboardTabs'
import type { SessionTabs } from '../hooks/useSessionTabs'
import '../index.css'
import '@blerglab/chat/blerg.css'

const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../hooks/useBlackboardDraft', () => ({ useBlackboardRevision: () => 0, useBlackboardDraft: () => null }))

for (const width of [390, 3840]) it(`shows neutral empty thrusts at ${width}px`, async () => {
 await page.viewport(width, 1000)
 const record = { name: 'Plan', milestones: [{ id: 'm', name: 'Release' }], goals: [{ id: 'g', name: 'Reliability' }], thrusts: [{ id: 't', name: 'Ship safely', goal_id: 'g', milestone_id: 'm' }], snapshots: [], events: [] }
 read.mockImplementation((url: string) => Promise.resolve(new Response(JSON.stringify(url.includes('/slice') ? { cards: [], complete: true } : { record }))))
 const target = document.createElement('div'); document.body.append(target)
 const tabs = { tabs: [{ id: 'tab', view: { kind: 'blackboard', blackboardId: `empty-${width}`, path: { level: 'milestone', milestoneId: 'm' } } }], visible: ['tab'], targetFor: () => target } as unknown as SessionTabs
 const app = render(<BlackboardTabs tabs={tabs} sessionId="s" />)
 try {
  await screen.findByRole('heading', { name: 'Release' })
  await act(async () => {})
  const tile = screen.getByRole('button', { name: /Ship safely/ })
  expect(tile).toHaveTextContent('No cards yet')
  expect(tile).not.toHaveTextContent('All shipped')
  expect(tile).not.toHaveTextContent('No cards assigned yet')
  expect(tile.textContent?.match(/No cards yet/g)).toHaveLength(1)
  const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/empty-thrust-${width}.png` })
  setTheme('colorblind-dark')
  expect(tile).toHaveTextContent('No cards yet')
  expect(tile).not.toHaveTextContent('All shipped')
  if (shots) await page.screenshot({ path: `${shots}/empty-thrust-colorblind-${width}.png` })
 } finally { setTheme('light'); app.unmount(); target.remove() }
})
