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
  grants: [{ id: 'g1', kind: 'board_file', board_id: 'b1', board_name: LONG, created_via: 'card', created_at: '2026-10-20T10:00:00Z' },
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

it.each([390, 1280])('opens the modal as %s px requires: a sheet on a phone, a dialog on a laptop', async width => {
  await page.viewport(width, 800)
  serve()
  render(<aside className="session-rail" style={{ width: width < 600 ? width : 340 }}><SessionPermissions sessionId="s1" active delegationGrants={grants} refreshKey="" /></aside>)
  await screen.findByTestId('perm-count')
  await userEvent.click(screen.getByRole('button', { name: 'Manage' }))
  const dialog = screen.getByRole('dialog', { name: 'Permissions' })
  await within(dialog).findByRole('checkbox', { name: 'File cards on Board 39 without asking' })
  const box = dialog.getBoundingClientRect()
  if (width < 600) {
    expect(box.left).toBe(0); expect(box.top).toBe(0)
    expect(box.width).toBe(width); expect(box.height).toBe(800)
  } else {
    expect(box.width).toBeLessThanOrEqual(720)
    expect(box.left).toBeGreaterThan(100)
    expect(box.height).toBeLessThan(800)
  }
  // Header and footer stay put; the body scrolls; nothing spills sideways.
  const save = within(dialog).getByRole('button', { name: 'Save' })
  expect(save.getBoundingClientRect().bottom).toBeLessThanOrEqual(box.bottom)
  expect(save.getBoundingClientRect().right).toBeLessThanOrEqual(box.right)
  for (const el of dialog.querySelectorAll<HTMLElement>('*')) if (el.clientWidth > 0) expect(el.getBoundingClientRect().right, el.className).toBeLessThanOrEqual(box.right + 0.5)
  // The board list scrolls inside itself rather than stretching the dialog.
  const list = within(dialog).getByRole('list', { name: 'Boards to file cards on without asking' })
  expect(list.scrollHeight).toBeGreaterThan(list.clientHeight)
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
  await within(dialog).findByRole('checkbox', { name: 'File cards on Board 00 without asking' })
  await userEvent.keyboard('board 0')
  const first = within(dialog).getByRole('checkbox', { name: 'File cards on Board 00 without asking' })
  expect(within(dialog).queryByRole('checkbox', { name: `File cards on ${LONG} without asking` })).toBeNull()
  // One tab stop per list (roving tabindex): arrows move within it.
  first.focus()
  expect(first.tabIndex).toBe(0)
  await userEvent.keyboard('{ArrowDown}')
  const second = within(dialog).getByRole('checkbox', { name: 'File cards on Board 01 without asking' })
  expect(document.activeElement).toBe(second)
  expect(first.tabIndex).toBe(-1)
  await userEvent.keyboard(' ')
  expect(second).toBeChecked()
  await userEvent.keyboard('{End}')
  // "board 0" leaves every board with a 0 in its name: 00 to 09, 10, 20, 30.
  expect(document.activeElement).toBe(within(dialog).getByRole('checkbox', { name: 'File cards on Board 30 without asking' }))
  await userEvent.keyboard('{Home}')
  expect(document.activeElement).toBe(first)
  expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 1')
  // Visible focus.
  expect(getComputedStyle(first).outlineStyle).toBe('solid')
  // The focus trap: Tab past Save wraps to the first control, Shift+Tab back again.
  within(dialog).getByRole('button', { name: 'Save' }).focus()
  await userEvent.keyboard('{Tab}')
  expect(dialog.contains(document.activeElement)).toBe(true)
  expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Close' }))
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
  expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Save' }))
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(document.activeElement).toBe(manage)
  expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
})
