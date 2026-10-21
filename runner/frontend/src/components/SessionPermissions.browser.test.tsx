// The Permissions summary and modal in a real browser: chips that fit the panel, the modal as a
// dialog on a laptop and a full-screen sheet on a phone, and keyboard-first use (focus trap, arrows
// and space on lists, Esc back to Manage).
import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import '../index.css'
import './SessionRail.css'
import SessionPermissions from './SessionPermissions'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
afterEach(() => { apiFetch.mockReset() })

const approval = { revision: 'r'.repeat(64), daemon_id: 'd', allowed_engines: ['claude', 'codex'], runtimes: ['docker', 'daemon'].map(runtime => ({ runtime, enabled: false, partial: runtime === 'docker', allowed_engines: ['claude', 'codex'], granted_engines: runtime === 'docker' ? ['claude'] : [], standing_rule_engines: [], effective_allowed_engines: [], can_enable: true, reason: '' })) }
const LONG = 'Product roadmap and platform reliability'
const perms = {
  can_grant: true, reason: '',
  boards: [{ board_id: 'b1', board_name: LONG, level: 'full', grant_id: 'g1', created_via: 'launch', created_at: '2026-10-20T10:00:00Z', launch: true, active: true }],
  grants: [{ id: 'g1', kind: 'board_file', level: 'full', board_id: 'b1', board_name: LONG, created_via: 'launch', created_at: '2026-10-20T10:00:00Z' },
    { id: 'g2', kind: 'mcp_tool', connection_id: 'c1', connection_name: 'Team mail', tool: 'send_message_to_a_long_distribution_list', created_via: 'launch', created_at: '2026-10-20T10:00:00Z' }],
  mcp_tools: [{ connection_id: 'c1', connection_name: 'Team mail', tool: 'send_message_to_a_long_distribution_list' }, { connection_id: 'c1', connection_name: 'Team mail', tool: 'archive' }],
}
const boards = [{ id: 'b1', name: LONG }, ...Array.from({ length: 40 }, (_, i) => ({ id: `x${i}`, name: `Board ${String(i).padStart(2, '0')}` }))]
function serve() {
  apiFetch.mockImplementation(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/child-approval') ? approval : url === '/api/session-boards' ? { configured: true, boards } : perms)))
}
const grants = [{ id: 'd1', engine: 'claude', runtime: 'docker', daemon_id: 'desk' }]

it.each([390, 1280])('shows a compact summary with chips that fit the panel at %i px', async width => {
  await page.viewport(width, 800)
  serve()
  const panelWidth = width < 600 ? width : 340
  const view = render(<div className="session-rail panel" style={{ width: panelWidth, boxSizing: 'border-box' }}>
    <SessionPermissions sessionId="s1" active delegationGrants={grants} refreshKey="" /></div>)
  const block = screen.getByTestId('rail-permissions')
  expect(await within(block).findByTestId('perm-count')).toHaveTextContent('3 active')
  expect(within(block).getByRole('button', { name: 'Manage' })).toBeVisible()
  const xs = within(block).getAllByRole('button', { name: /^Revoke: / })
  expect(xs).toHaveLength(3)
  for (const x of xs) {
    const box = x.getBoundingClientRect()
    expect(box.width).toBeGreaterThanOrEqual(20)
    expect(box.right).toBeLessThanOrEqual(block.getBoundingClientRect().right + 0.5)
  }
  for (const chip of within(block).getAllByRole('listitem')) expect(chip.getBoundingClientRect().right).toBeLessThanOrEqual(block.getBoundingClientRect().right + 0.5)
  // The whole summary stays short: no list of toggles.
  expect(block.getBoundingClientRect().height).toBeLessThan(200)
  expect(view.container.scrollWidth).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it.each([390, 1280, 3840])('opens the modal as %s px requires: a sheet on a phone, a dialog on a laptop, a wide dialog on a 4K screen', async width => {
  await page.viewport(width, 800)
  serve()
  render(<aside className="session-rail" style={{ width: width < 600 ? width : 340 }}><SessionPermissions sessionId="s1" active delegationGrants={grants} refreshKey="" /></aside>)
  await screen.findByTestId('perm-count')
  await userEvent.click(screen.getByRole('button', { name: 'Manage' }))
  const dialog = screen.getByRole('dialog', { name: 'Permissions' })
  await within(dialog).findByRole('combobox', { name: 'Access to Board 00' })
  expect(within(dialog).getAllByRole('combobox')).toHaveLength(25)
  await userEvent.click(within(dialog).getByRole('button', { name: 'Next boards' }))
  expect(within(dialog).getByRole('combobox', { name: 'Access to Board 39' })).toBeVisible()
  await userEvent.click(within(dialog).getByRole('button', { name: 'Previous boards' }))
  const box = dialog.getBoundingClientRect()
  if (width < 600) {
    expect(box.left).toBe(0); expect(box.top).toBe(0)
    expect(box.width).toBe(width); expect(box.height).toBe(800)
    expect(within(dialog).queryByRole('button', { name: 'Maximize' })).toBeNull()
  } else if (width < 2000) {
    expect(box.width).toBeLessThanOrEqual(720)
    expect(box.left).toBeGreaterThan(100)
    expect(box.height).toBeLessThan(800)
  } else {
    // A dense dialog scales with a large screen instead of staying a 720 px strip.
    expect(box.width).toBeGreaterThan(1200)
    expect(box.left).toBeGreaterThan(100)
  }
  // Each board's level select stays on its row, inside the dialog.
  const level = within(dialog).getByRole('combobox', { name: `Access to ${LONG}` })
  expect(level).toHaveValue('full')
  expect(level.getBoundingClientRect().right).toBeLessThanOrEqual(box.right)
  // Header and footer stay put; the body scrolls; nothing spills sideways.
  const save = within(dialog).getByRole('button', { name: 'Save' })
  expect(save.getBoundingClientRect().bottom).toBeLessThanOrEqual(box.bottom)
  expect(save.getBoundingClientRect().right).toBeLessThanOrEqual(box.right)
  for (const el of dialog.querySelectorAll<HTMLElement>('*')) if (el.clientWidth > 0) expect(el.getBoundingClientRect().right, el.className).toBeLessThanOrEqual(box.right + 0.5)
  // The board list scrolls inside itself rather than stretching the dialog.
  const list = within(dialog).getByRole('list', { name: 'Board access' })
  expect(list.scrollHeight).toBeGreaterThan(list.clientHeight)
  if (width >= 600) {
    // Maximize takes the whole window (less the overlay's margin), and restores.
    await userEvent.click(within(dialog).getByRole('button', { name: 'Maximize' }))
    const big = dialog.getBoundingClientRect()
    expect(big.width).toBeGreaterThanOrEqual(width - 48 - 1)
    expect(big.height).toBeGreaterThanOrEqual(800 - 48 - 1)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Restore size' }))
    expect(dialog.getBoundingClientRect().width).toBeCloseTo(box.width, 0)
  }
  const rev = within(dialog).getAllByRole('button', { name: /^Revoke: / })[0]
  expect(getComputedStyle(rev).borderTopStyle).toBe('solid')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it('is keyboard-first: focus starts in the search, lists take arrows and space, Tab is trapped, Esc returns to Manage', async () => {
  await page.viewport(1280, 800)
  serve()
  render(<aside className="session-rail" style={{ width: 340 }}><SessionPermissions sessionId="s1" active delegationGrants={grants} refreshKey="" /></aside>)
  await screen.findByTestId('perm-count')
  const manage = screen.getByRole('button', { name: 'Manage' })
  manage.focus()
  await userEvent.keyboard('{Enter}')
  const dialog = screen.getByRole('dialog', { name: 'Permissions' })
  const search = within(dialog).getByRole('searchbox', { name: 'Search permissions' })
  expect(document.activeElement).toBe(search)
  await within(dialog).findByRole('combobox', { name: 'Access to Board 00' })
  await userEvent.keyboard('board 0')
  expect(within(dialog).queryByRole('combobox', { name: `Access to ${LONG}` })).toBeNull()
  // A board's level is a select: a keyboard picks it, and the footer counts the change.
  const level = within(dialog).getByRole('combobox', { name: 'Access to Board 01' })
  level.focus()
  await userEvent.selectOptions(level, 'read')
  expect(level).toHaveValue('read')
  expect(level.closest('li')).toHaveClass('pm-changed')
  expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 1')
  expect(getComputedStyle(level).outlineStyle).toBe('solid')
  // Checkbox lists are one tab stop each (roving tabindex): arrows move within them.
  await userEvent.clear(search)
  await userEvent.type(search, 'team mail')
  const first = within(dialog).getByRole('checkbox', { name: 'Run Team mail · send_message_to_a_long_distribution_list without asking' })
  first.focus()
  expect(first.tabIndex).toBe(0)
  await userEvent.keyboard('{ArrowDown}')
  const second = within(dialog).getByRole('checkbox', { name: 'Run Team mail · archive without asking' })
  expect(document.activeElement).toBe(second)
  expect(first.tabIndex).toBe(-1)
  await userEvent.keyboard(' ')
  expect(second).toBeChecked()
  await userEvent.keyboard('{Home}')
  expect(document.activeElement).toBe(first)
  expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 2')
  // Visible focus.
  expect(getComputedStyle(first).outlineStyle).toBe('solid')
  // The focus trap: Tab past Save wraps to the first control, Shift+Tab back again.
  within(dialog).getByRole('button', { name: 'Save' }).focus()
  await userEvent.keyboard('{Tab}')
  expect(dialog.contains(document.activeElement)).toBe(true)
  expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Maximize' }))
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
  expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Save' }))
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(document.activeElement).toBe(manage)
  expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
})

it.each([390, 3840])('fits the three child rows of a desktop session on an install with a cluster at %i px, each saying where it runs', async width => {
  await page.viewport(width, 900)
  const three = { ...approval, runtimes: ['docker', 'daemon', 'cluster'].map(runtime => ({ runtime, enabled: false, partial: false, allowed_engines: ['claude', 'codex'], granted_engines: [], standing_rule_engines: [], effective_allowed_engines: [], can_enable: true, reason: '' })) }
  apiFetch.mockImplementation(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/child-approval') ? three : url === '/api/session-boards' ? { configured: true, boards } : perms)))
  render(<aside className="session-rail" style={{ width: width < 600 ? width : 340 }}><SessionPermissions sessionId="s1" active delegationGrants={grants} refreshKey="" /></aside>)
  await screen.findByTestId('perm-count')
  await userEvent.click(screen.getByRole('button', { name: 'Manage' }))
  const dialog = screen.getByRole('dialog', { name: 'Permissions' })
  const cluster = await within(dialog).findByTestId('pm-children-cluster')
  const right = dialog.getBoundingClientRect().right + 0.5
  for (const [id, where] of [['docker', 'On this desktop.'], ['daemon', 'On this desktop.'], ['cluster', 'On the cluster.']] as const) {
    const row = within(dialog).getByTestId(`pm-children-${id}`)
    expect(row).toHaveTextContent(where)
    expect(row.getBoundingClientRect().right).toBeLessThanOrEqual(right)
    expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth + 1)
  }
  expect(within(cluster).getByRole('checkbox', { name: 'Start children on the cluster without asking' })).toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it.each([390, 3840])('shows a started session its cap on the board row without overflowing at %i px', async width => {
  await page.viewport(width, 800)
  const child = { ...perms,
    boards: [{ board_id: 'b1', board_name: LONG, level: 'read', own_level: 'full', parent_level: 'read', grant_id: 'g9', created_via: 'parent', created_at: '2026-10-20T10:00:00Z', active: true }],
    grants: [], started_by: { id: 'p1', title: 'Nightly batch over the release cards', boards: [{ board_id: 'b1', board_name: LONG, level: 'read' }] } }
  apiFetch.mockImplementation(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/child-approval') ? approval : url === '/api/session-boards' ? { configured: true, boards } : child)))
  render(<aside className="session-rail" style={{ width: width < 600 ? width : 340 }}><SessionPermissions sessionId="s1" active delegationGrants={[]} refreshKey="" /></aside>)
  await screen.findByTestId('perm-count')
  await userEvent.click(screen.getByRole('button', { name: 'Manage' }))
  const dialog = screen.getByRole('dialog', { name: 'Permissions' })
  const note = await within(dialog).findByTestId('pm-board-cap-b1')
  expect(note).toHaveTextContent('Acts at Read: capped by Nightly batch over the release cards, which has Read.')
  const row = within(dialog).getByTestId('pm-board-b1')
  expect(note.getBoundingClientRect().right).toBeLessThanOrEqual(row.getBoundingClientRect().right + 0.5)
  expect(within(dialog).getByRole('combobox', { name: `Access to ${LONG}` }).getBoundingClientRect().right).toBeLessThanOrEqual(dialog.getBoundingClientRect().right + 0.5)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
