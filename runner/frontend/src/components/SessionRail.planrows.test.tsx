// The Plan tab's checklist, one line per item (card #111): rows, names never ids, the folded done
// items, a shared commit shown once, the chip opening the diff, and the keyboard.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import SessionRail, { type SessionRailProps } from './SessionRail'
import { makeSession } from '../test/fixtures'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { CHILD_ID, SHARED_DIFF, SUB_ID, clutteredItems, clutteredTitles, makeLive } from '../test/planTabFixture'

function rail(over: Partial<SessionRailProps> = {}, titles: Record<string, string> | null = clutteredTitles) {
  localStorage.setItem(RAIL_TAB_KEY, 'plan')
  const live = makeLive({ checklist: { items: clutteredItems, eventId: 'c1', updatedAt: null, ...(titles ? { workerTitles: titles } : {}) } as never })
  const props: SessionRailProps = {
    id: 'rail', session: makeSession({ status: 'running' }), live, toolChanges: 0, mode: 'panel', open: true, onClose: () => {}, onOpenArtifact: () => {}, ...over,
  }
  return render(<MemoryRouter><SessionRail {...props} /></MemoryRouter>)
}
const list = () => screen.getByTestId('rail-plan-list')
const rows = () => within(list()).getAllByTestId('rail-plan-row')
const shown = () => rows().filter(r => !r.hidden)
/** Every accessible name and visible text in the list. */
function everything(root: HTMLElement): string {
  const names = [...root.querySelectorAll<HTMLElement>('*')].flatMap(el => [el.getAttribute('aria-label') ?? '', el.getAttribute('title') ?? ''])
  return [root.textContent ?? '', ...names].join('\n')
}

beforeEach(() => { localStorage.clear(); Element.prototype.scrollIntoView = vi.fn() })

describe('Plan tab checklist rows', () => {
  it('draws one row per item, open and doing first, the done ones folded into one row', () => {
    rail()
    expect(rows()).toHaveLength(clutteredItems.length)
    expect(shown().map(r => r.dataset.checklistId)).toEqual(['doing-1', 'open-1', 'open-2'])
    const toggle = screen.getByTestId('rail-plan-done-toggle')
    expect(toggle).toHaveTextContent('✓ 13 done · show')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    // Each row is one flex line: box, title, glyphs, chip; nothing on a line of its own.
    for (const r of rows()) expect([...r.children].map(c => c.className.split(' ')[0])).toEqual(expect.arrayContaining(['checklist-box', 'checklist-text']))
    expect(list().querySelector('.checklist-commits, .rail-checklist-assignees, .commit-view-diff')).toBeNull()
  })

  it('unfolds and folds the done items', () => {
    rail()
    fireEvent.click(screen.getByTestId('rail-plan-done-toggle'))
    expect(shown()).toHaveLength(clutteredItems.length)
    expect(screen.getByTestId('rail-plan-done-toggle')).toHaveTextContent('✓ 13 done · hide')
    fireEvent.click(screen.getByTestId('rail-plan-done-toggle'))
    expect(shown()).toHaveLength(3)
  })

  it('shows a doing item with the spinner', () => {
    rail()
    const doing = rows().find(r => r.dataset.checklistId === 'doing-1')!
    expect(doing.querySelector('.checklist-progress.animated')).not.toBeNull()
    expect(within(doing).getByTestId('rail-plan-title').textContent).toBe('Wire the one-line rows, doing')
    expect(within(doing).getByRole('button', { name: 'Show the whole item' })).toHaveAttribute('aria-expanded', 'false')
  })

  it('names the assignees in glyph tooltips, never with a raw id', () => {
    rail()
    fireEvent.click(screen.getByTestId('rail-plan-done-toggle'))
    expect(screen.getAllByRole('img', { name: 'Subagent: Audit the plan tab' })).toHaveLength(2)
    expect(screen.getByRole('img', { name: 'Started session: Port the parser' })).toHaveAttribute('title', 'Started session: Port the parser')
    const all = everything(list())
    expect(all).not.toContain(SUB_ID)
    expect(all).not.toContain('toolu_')
    expect(all).not.toContain(CHILD_ID)
  })

  it('without recorded names falls back to the running worker, then to the kind, still no id', () => {
    rail({ childSessions: [{ id: CHILD_ID, title: 'Child from the live list', engine: 'codex', model: '', status: 'running', started_at: new Date().toISOString(), asking: false, tokens: null }] }, null)
    expect(screen.getByRole('img', { name: 'Started session (Codex): Child from the live list' })).toBeTruthy()
    expect(screen.getByRole('img', { name: 'Subagent: a subagent' })).toBeTruthy()
    const all = everything(list())
    expect(all).not.toContain(SUB_ID)
    expect(all).not.toContain(CHILD_ID)
  })

  it('shows a commit shared by several items once, saying how many it covers, and opens the diff from it', () => {
    const open = vi.fn()
    rail({ onOpenArtifact: open })
    fireEvent.click(screen.getByTestId('rail-plan-done-toggle'))
    const chips = within(list()).getAllByTestId('rail-plan-commit')
    expect(chips).toHaveLength(1)
    expect(chips[0].closest('li')!.dataset.checklistId).toBe('done-0')
    expect(chips[0]).toHaveAccessibleName(/^Open the diff of commit f2a965d.*; covers 3 items$/)
    // The tooltip a person sees says the same: the sha, how many items it covers, and what a click does.
    expect(chips[0].getAttribute('title')).toBe('f2a965d0c1\nCovers 3 items\nOpen the diff')
    expect(within(list()).queryAllByText('f2a965d')).toHaveLength(1)
    fireEvent.click(chips[0])
    expect(open).toHaveBeenCalledWith(SHARED_DIFF, undefined)
  })

  it('works from the keyboard: the item\'s chevron expands it on Enter, the done row and the chip are buttons', async () => {
    const open = vi.fn()
    rail({ onOpenArtifact: open })
    const user = userEvent.setup()
    const expand = within(rows()[0]).getByTestId('rail-plan-expand')
    expand.focus()
    await user.keyboard('{Enter}')
    expect(expand).toHaveAttribute('aria-expanded', 'true')
    expect(within(rows()[0]).getByTestId('rail-plan-title')).toHaveAttribute('data-expanded', 'true')
    const toggle = screen.getByTestId('rail-plan-done-toggle')
    toggle.focus()
    await user.keyboard('{Enter}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const chip = within(list()).getByTestId('rail-plan-commit')
    chip.focus()
    await user.keyboard('{Enter}')
    expect(open).toHaveBeenCalledWith(SHARED_DIFF, undefined)
  })

  it('a jump to a done item unfolds the done items', async () => {
    const { rerender } = rail()
    expect(rows().find(r => r.dataset.checklistId === 'done-4')!.hidden).toBe(true)
    const live = makeLive({ checklist: { items: clutteredItems, eventId: 'c1', updatedAt: null } })
    rerender(<MemoryRouter><SessionRail id="rail" session={makeSession({ status: 'running' })} live={live} toolChanges={0} mode="panel" open onClose={() => {}} onOpenArtifact={() => {}} openChecklistItem={{ id: 'done-4', n: 1 }} /></MemoryRouter>)
    await waitFor(() => expect(rows().find(r => r.dataset.checklistId === 'done-4')!.hidden).toBe(false))
    await waitFor(() => expect(document.activeElement).toHaveAttribute('data-checklist-id', 'done-4'))
  })
})
