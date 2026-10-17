// The session panel's Plan tab: tickets, the checklist or plan, and the diffs the session published;
// and Live's one summary line about the list.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionRail, { type SessionRailProps } from './SessionRail'
import { makeSession } from '../test/fixtures'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { diffFiles, filesOf, makeLive, planItems } from '../test/planTabFixture'

const ticket = { source: 'tracker', key: 'PRJ-12', title: 'Fix the thing', status: 'In progress', url: '', eventId: 'e1', updatedAt: null }

function rail(live = makeLive(), over: Partial<SessionRailProps> = {}) {
  const props: SessionRailProps = {
    id: 'rail', session: makeSession(), live, toolChanges: 0, mode: 'panel', open: true, onClose: () => {},
    onOpenArtifact: () => {}, ...over,
  }
  return render(<MemoryRouter><SessionRail {...props} /></MemoryRouter>)
}
const tabs = () => screen.getAllByRole('tab').map(t => t.textContent?.replace(/,.*$/, '').trim())

beforeEach(() => localStorage.clear())

describe('Plan tab', () => {
  it('is absent without a ticket, a list or a diff', () => {
    rail()
    expect(screen.queryByRole('tab', { name: /Plan/ })).toBeNull()
    expect(tabs()).toEqual(['Live', 'History', 'Files', 'Capabilities'])
    expect(screen.queryByTestId('rail-plan-summary')).toBeNull()
  })

  it('carries done/total of the checklist and sits after Live', () => {
    rail(makeLive({ checklist: { items: planItems, eventId: 'c1', updatedAt: null } }))
    expect(tabs()[1]).toMatch(/^Plan18\/23/)
    expect(screen.getByTestId('rail-plan-tab-count').textContent).toBe('18/23')
  })

  it('counts tickets and diffs when there is no list', () => {
    rail(makeLive({ tickets: [ticket], files: filesOf(diffFiles) }))
    expect(screen.getByTestId('rail-plan-tab-count').textContent).toBe('3')
  })

  it('falls back to Live when the saved tab is Plan and there is nothing', () => {
    localStorage.setItem(RAIL_TAB_KEY, 'plan')
    rail()
    expect(screen.getByRole('tab', { name: /Live/ }).getAttribute('aria-selected')).toBe('true')
  })

  it('shows the saved Plan tab when there is something', () => {
    localStorage.setItem(RAIL_TAB_KEY, 'plan')
    rail(makeLive({ tickets: [ticket] }))
    expect(screen.getByRole('tab', { name: /Plan/ }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByTestId('rail-ticket')).toBeTruthy()
  })

  it('moves the list, tickets and diffs off Live', () => {
    rail(makeLive({ tickets: [ticket], checklist: { items: planItems, eventId: 'c1', updatedAt: null }, files: filesOf(diffFiles) }))
    const live = document.getElementById('rail-panel-live')!
    const plan = document.getElementById('rail-panel-plan')!
    expect(within(live).queryByTestId('rail-checklist')).toBeNull()
    expect(within(live).queryByTestId('rail-ticket')).toBeNull()
    expect(within(plan).getByTestId('rail-checklist')).toBeTruthy()
    expect(within(plan).getByTestId('rail-ticket')).toBeTruthy()
  })

  it('lists the diffs newest first and opens one in the viewer', () => {
    const open = vi.fn()
    rail(makeLive({ files: filesOf(diffFiles) }), { onOpenArtifact: open })
    fireEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    const rows = screen.getAllByTestId('rail-diff-row')
    expect(rows.map(r => r.textContent)).toEqual([expect.stringContaining('second-change.diff'), expect.stringContaining('first-change.diff')])
    fireEvent.click(within(rows[1]).getByRole('button', { name: 'first-change.diff' }))
    expect(open).toHaveBeenCalledWith('d1')
  })
})

describe('Live summary line', () => {
  it('names the next unticked item with progress', () => {
    rail(makeLive({ checklist: { items: planItems, eventId: 'c1', updatedAt: null } }))
    const line = screen.getByTestId('rail-plan-summary')
    expect(within(line).getByTestId('rail-plan-summary-text').textContent).toBe('Checklist · 18 of 23 · Next: Step 19: update the docs')
    expect(within(line).getByRole('progressbar')).toBeTruthy()
  })

  it('says all done', () => {
    rail(makeLive({ checklist: { items: planItems.map(i => ({ ...i, done: true })), eventId: 'c1', updatedAt: null } }))
    expect(screen.getByTestId('rail-plan-summary-text').textContent).toBe('Checklist · all 23 done')
  })

  it('uses the active text of the in-progress plan item', () => {
    rail(makeLive({ plan: { updatedAt: null, items: [{ text: 'Build', status: 'completed' }, { text: 'Test it', active: 'Testing it', status: 'in_progress' }] } as never }))
    expect(screen.getByTestId('rail-plan-summary-text').textContent).toBe('Plan · 1 of 2 · Next: Testing it')
  })

  it('starts with the pinned ticket key and status', () => {
    rail(makeLive({ tickets: [ticket], checklist: { items: planItems, eventId: 'c1', updatedAt: null } }))
    const line = screen.getByTestId('rail-plan-summary')
    expect(line.textContent).toMatch(/^PRJ-12In progressChecklist/)
  })

  it('switches to the Plan tab and focuses it on click', () => {
    rail(makeLive({ checklist: { items: planItems, eventId: 'c1', updatedAt: null } }))
    fireEvent.click(screen.getByTestId('rail-plan-summary'))
    const tab = screen.getByRole('tab', { name: /Plan/ })
    expect(tab.getAttribute('aria-selected')).toBe('true')
    expect(document.activeElement).toBe(tab)
    expect(localStorage.getItem(RAIL_TAB_KEY)).toBe('plan')
  })
})
