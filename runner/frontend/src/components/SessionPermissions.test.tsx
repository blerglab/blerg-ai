import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import SessionPermissions from './SessionPermissions'
import type { LiveDelegationGrant } from '../lib/liveState'

const request = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => request(...args) }))

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body })
const refused = (status: number, error: string) => ({ ok: false, status, json: async () => ({ error }) })
const runtime = (name: string, over: Record<string, unknown> = {}) => ({ runtime: name, enabled: false, partial: false, allowed_engines: ['claude', 'codex'], granted_engines: [], standing_rule_engines: [], effective_allowed_engines: [], can_enable: true, reason: '', ...over })
const approval = (over: Record<string, unknown> = {}) => ({ revision: 'rev-a', daemon_id: 'd', allowed_engines: ['claude', 'codex'], runtimes: [runtime('docker'), runtime('daemon')], ...over })
const boardFile = { board_id: 'b1', board_name: 'Product', level: 'file', grant_id: 'g1', created_via: 'card', created_at: '2026-10-20T10:00:00Z', active: true }
const toolGrant = { id: 'g2', kind: 'mcp_tool', connection_id: 'c1', connection_name: 'Mail', tool: 'send', created_via: 'panel', created_at: '2026-10-20T10:00:00Z' }
type Perms = { can_grant: boolean; reason: string; grants: unknown[]; mcp_tools: unknown[]; boards: unknown[]; started_by?: unknown }
const perms = (over: Partial<Perms> = {}): Perms => ({ can_grant: true, reason: '', grants: [], boards: [], mcp_tools: [{ connection_id: 'c1', connection_name: 'Mail', tool: 'send' }, { connection_id: 'c1', connection_name: 'Mail', tool: 'delete' }, { connection_id: 'c2', connection_name: 'Tracker', tool: 'create_issue' }], ...over })
const BOARDS = [{ id: 'b1', name: 'Product' }, { id: 'b2', name: 'Ops' }, { id: 'b3', name: 'Product research' }]

function serve(state: { perms: Perms; approval?: unknown }, extra: (url: string, init?: RequestInit) => unknown = () => undefined) {
  request.mockImplementation(async (url: string, init?: RequestInit) => {
    const handled = extra(url, init)
    if (handled) return handled
    if (url.endsWith('/child-approval')) return state.approval === null ? { ok: false, status: 404, json: async () => ({}) } : ok(state.approval ?? approval())
    if (url === '/api/session-boards') return ok({ configured: true, boards: BOARDS })
    if (url.endsWith('/permissions')) return ok(state.perms)
    return ok({})
  })
}
const mount = (grants: LiveDelegationGrant[] = [], openRequest?: number) => render(<SessionPermissions sessionId="s1" active delegationGrants={grants} refreshKey="" openRequest={openRequest} />)
const boardPut = (board: string, level: string) => ['/api/sessions/s1/board-access', { board_id: board, level }]
const posts = () => request.mock.calls.filter(([, i]) => i?.method === 'POST' || i?.method === 'PUT').map(([u, i]) => [u, i?.body ? JSON.parse(i.body) : undefined])
async function openModal() {
  fireEvent.click(await screen.findByRole('button', { name: 'Manage' }))
  return screen.getByRole('dialog', { name: 'Permissions' })
}

describe('the panel summary', () => {
  beforeEach(() => { request.mockReset() })

  it('shows the title, how many are active, Manage, and each active permission as a chip with ×', async () => {
    serve({ perms: perms({ boards: [boardFile, { board_id: 'b2', board_name: 'Ops', level: 'full', created_via: 'launch', launch: true, active: true }] }) })
    mount([{ id: 'd1', engine: 'codex', runtime: 'docker' }])
    const block = screen.getByTestId('rail-permissions')
    expect(within(block).getByRole('heading', { name: 'Permissions' })).toBeInTheDocument()
    expect(await within(block).findByTestId('perm-count')).toHaveTextContent('3 active')
    const chips = within(within(block).getByTestId('perm-active')).getAllByRole('listitem')
    expect(chips.map(c => c.textContent)).toEqual(['↳Codex · Local sandbox×', '▦Product · File without asking×', '▦Ops · Full×'])
    // No long list of toggles in the panel any more.
    expect(within(block).queryByRole('checkbox')).toBeNull()
  })

  it('counts one permission per kind of child and place, however many engines it covers', async () => {
    serve({ perms: perms() })
    mount([{ id: 'd1', engine: 'claude', runtime: 'docker' }, { id: 'd2', engine: 'codex', runtime: 'docker' }, { id: 'd3', engine: 'codex', runtime: 'daemon' }])
    expect(await screen.findByTestId('perm-count')).toHaveTextContent('2 active')
    const chips = within(screen.getByTestId('perm-active')).getAllByRole('listitem')
    expect(chips.map(c => c.textContent)).toEqual(['↳Claude, Codex · Local sandbox×', '↳Codex · This machine×'])
    fireEvent.click(screen.getByRole('button', { name: 'Revoke: Start Claude and Codex children · Local sandbox' }))
    await waitFor(() => {
      for (const id of ['d1', 'd2']) expect(request).toHaveBeenCalledWith(`/api/delegation-grants/${id}/revoke`, { method: 'POST' })
    })
    expect(request).not.toHaveBeenCalledWith('/api/delegation-grants/d3/revoke', { method: 'POST' })
  })

  it('names a cluster permission by the cluster, beside a desktop one, with no desktop name on it', async () => {
    serve({ perms: perms() })
    mount([{ id: 'd1', engine: 'claude', runtime: 'docker', daemon_id: 'laptop', daemon_name: 'Laptop' }, { id: 'd2', engine: 'codex', runtime: 'cluster', daemon_id: 'cluster', daemon_name: 'the cluster' }])
    expect(await screen.findByTestId('perm-count')).toHaveTextContent('2 active')
    const chips = within(screen.getByTestId('perm-active')).getAllByRole('listitem')
    expect(chips.map(c => c.textContent)).toEqual(['↳Claude · Local sandbox×', '↳Codex · Cluster pod×'])
  })

  it('revokes a chip inline with ×: a board goes back to propose, a tool permission is revoked', async () => {
    const state = { perms: perms({ boards: [boardFile], grants: [toolGrant] }) }
    serve(state, (url, init) => {
      if (url === '/api/sessions/s1/board-access' && init?.method === 'PUT') { state.perms = perms({ grants: [toolGrant] }); return ok({ boards: [] }) }
      if (url === '/api/proposal-grants/g2/revoke') { state.perms = perms(); return ok({ revoked: true }) }
    })
    mount()
    const x = await screen.findByRole('button', { name: 'Revoke: File without asking access to Product' })
    expect(x).toHaveClass('perm-chip-x')
    fireEvent.click(x)
    await waitFor(() => expect(posts()).toEqual([boardPut('b1', 'propose')]))
    await waitFor(() => expect(screen.getByTestId('perm-count')).toHaveTextContent('1 active'))
    fireEvent.click(screen.getByRole('button', { name: 'Revoke: Run Mail · send without asking' }))
    await waitFor(() => expect(request).toHaveBeenCalledWith('/api/proposal-grants/g2/revoke', { method: 'POST' }))
    await waitFor(() => expect(screen.getByTestId('perm-count')).toHaveTextContent('0 active'))
  })

  it('keeps a grant that needs you signed in apart, with why, and its × revokes it', async () => {
    serve({ perms: perms({ boards: [{ ...boardFile, active: false, inactive: 'signed_out' }], grants: [{ ...toolGrant, active: false, inactive: 'tool_changed' }] }) })
    mount()
    const dormant = await screen.findByTestId('perm-inactive')
    expect(dormant).toHaveTextContent('Needs you signed in')
    expect(dormant).toHaveTextContent('tool changed')
    expect(within(dormant).getAllByRole('button', { name: /^Revoke/ })).toHaveLength(2)
    expect(screen.getByTestId('perm-count')).toHaveTextContent('0 active')
  })

  it('shows nothing to a person who is not the owner (the server says 404)', async () => {
    request.mockImplementation(async () => ({ ok: false, status: 404, json: async () => ({}) }))
    const view = mount()
    await waitFor(() => expect(view.container).toBeEmptyDOMElement())
    expect(screen.queryByRole('button', { name: 'Manage' })).toBeNull()
  })

  it('reports a failed revoke without pretending it happened', async () => {
    serve({ perms: perms({ boards: [boardFile] }) }, (_url, init) => init?.method === 'PUT' ? refused(409, 'The session has ended.') : undefined)
    mount()
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke: File without asking access to Product' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The session has ended.')
    expect(screen.getByTestId('perm-count')).toHaveTextContent('1 active')
  })
})

describe('the Permissions modal over a running session', () => {
  beforeEach(() => { request.mockReset() })

  it('filters every section by the search', async () => {
    serve({ perms: perms() })
    mount()
    const dialog = await openModal()
    expect(await within(dialog).findByRole('combobox', { name: 'Access to Ops' })).toBeInTheDocument()
    fireEvent.change(within(dialog).getByRole('searchbox', { name: 'Search permissions' }), { target: { value: 'product' } })
    expect(within(dialog).queryByRole('combobox', { name: 'Access to Ops' })).toBeNull()
    expect(within(dialog).getByRole('combobox', { name: 'Access to Product' })).toBeInTheDocument()
    expect(within(dialog).getByRole('combobox', { name: 'Access to Product research' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('checkbox', { name: /^Run / })).toBeNull()
    expect(within(dialog).getByText('No tools match “product”.')).toBeInTheDocument()
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: 'mail del' } })
    expect(within(dialog).getByRole('checkbox', { name: 'Run Mail · delete without asking' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('checkbox', { name: 'Run Mail · send without asking' })).toBeNull()
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: 'codex' } })
    expect(within(dialog).getByRole('checkbox', { name: 'Start sandboxed children without asking' })).toBeInTheDocument()
  })

  it('sets one level per board, counts the boards with access, and Save sets exactly the changed ones', async () => {
    serve({ perms: perms({ boards: [boardFile] }) })
    mount()
    const dialog = await openModal()
    const ops = await within(dialog).findByRole('combobox', { name: 'Access to Ops' })
    expect(within(dialog).getByTestId('pm-board-count')).toHaveTextContent('1 of 3 with access')
    expect(within(dialog).getByRole('combobox', { name: 'Access to Product' })).toHaveValue('file')
    expect(ops).toHaveValue('propose')
    // Every level is offered, lowest first.
    expect(within(ops).getAllByRole('option').map(o => o.textContent)).toEqual(['Propose', 'Read', 'File without asking', 'Full'])
    fireEvent.change(ops, { target: { value: 'read' } })
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Product research' }), { target: { value: 'full' } })
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Product' }), { target: { value: 'read' } })
    expect(within(dialog).getByTestId('pm-board-count')).toHaveTextContent('3 of 3 with access')
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 2, revoke 1')
    expect(ops.closest('li')).toHaveClass('pm-changed')
    // Setting a board back to where it was is no change.
    fireEvent.change(ops, { target: { value: 'propose' } })
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 1, revoke 1')
    fireEvent.change(ops, { target: { value: 'read' } })
    // Nothing is sent before Save.
    expect(posts()).toEqual([])
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts()).toHaveLength(3)
    expect(posts()).toEqual(expect.arrayContaining([boardPut('b1', 'read'), boardPut('b2', 'read'), boardPut('b3', 'full')]))
    expect(await screen.findByText('Saved: grant 2, revoke 1.')).toBeInTheDocument()
  })

  it('renews a signed-out board at the level it shows, in one Save', async () => {
    serve({ perms: perms({ boards: [{ ...boardFile, level: 'full', active: false, inactive: 'signed_out' }] }) })
    mount()
    const dialog = await openModal()
    const product = await within(dialog).findByRole('combobox', { name: 'Access to Product' })
    expect(product).toHaveValue('full')
    expect(within(dialog).getByTestId('pm-board-b1')).toHaveTextContent('Needs you signed in')
    // Choosing the level it already shows changes nothing in a select; Renew does it.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Renew access to Product' }))
    expect(within(dialog).getByTestId('pm-board-b1')).toHaveTextContent('Renewed from your current sign-in on Save')
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 1')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts()).toEqual([boardPut('b1', 'full')])
  })

  it('shows a started session its levels with the cap of the session that started it, and never offers more', async () => {
    serve({ perms: perms({
      boards: [{ board_id: 'b1', board_name: 'Product', level: 'read', own_level: 'full', parent_level: 'read', grant_id: 'g7', created_via: 'parent', created_at: '2026-10-20T10:00:00Z', active: true }],
      started_by: { id: 'p1', title: 'Batch runner', boards: [{ board_id: 'b1', board_name: 'Product', level: 'read' }, { board_id: 'b2', board_name: 'Ops', level: 'file' }] },
    }) })
    mount()
    // The chip says what the session acts at; its title says why.
    const chip = (await screen.findAllByTestId('perm-board'))[0]
    expect(chip).toHaveTextContent('Product · Read')
    expect(chip.getAttribute('title')).toContain('capped at Read by the session that started it (its own level is Full)')
    const dialog = await openModal()
    expect(await within(dialog).findByTestId('pm-board-cap-note')).toHaveTextContent('Started by Batch runner')
    const product = await within(dialog).findByRole('combobox', { name: 'Access to Product' })
    // The select holds its own level; the row says it acts at the parent's.
    expect(product).toHaveValue('full')
    expect(within(dialog).getByTestId('pm-board-cap-b1')).toHaveTextContent('Acts at Read: capped by Batch runner, which has Read.')
    const ops = within(dialog).getByRole('combobox', { name: 'Access to Ops' })
    expect(within(dialog).getByTestId('pm-board-cap-b2')).toHaveTextContent('At most File without asking')
    const enabled = (sel: HTMLElement) => within(sel).getAllByRole('option').filter(o => !(o as HTMLOptionElement).disabled).map(o => (o as HTMLOptionElement).value)
    expect(enabled(ops)).toEqual(['propose', 'read', 'file'])
    // A board the parent has no access to offers nothing above propose.
    expect(enabled(within(dialog).getByRole('combobox', { name: 'Access to Product research' }))).toEqual(['propose'])
    // Lowering a single child is a Save like any other.
    fireEvent.change(product, { target: { value: 'propose' } })
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Revoke 1')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts()).toEqual([boardPut('b1', 'propose')])
  })

  it('shows a board chosen at launch as Full, and it can be lowered', async () => {
    serve({ perms: perms({ boards: [{ board_id: 'b2', board_name: 'Ops', level: 'full', grant_id: 'g5', created_via: 'launch', created_at: '2026-10-20T10:00:00Z', launch: true, active: true }] }) })
    mount()
    const dialog = await openModal()
    const ops = await within(dialog).findByRole('combobox', { name: 'Access to Ops' })
    expect(ops).toHaveValue('full')
    expect(within(dialog).getByTestId('pm-board-b2')).toHaveTextContent('Chosen at launch')
    const row = within(dialog).getAllByTestId('pm-active-row').find(r => r.textContent?.includes('Ops · Full'))!
    expect(row).toHaveTextContent(/Given at launch, .* the board chosen at launch/)
    fireEvent.change(ops, { target: { value: 'read' } })
    expect(row).toHaveTextContent('Becomes Read on Save')
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Revoke 1')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts()).toEqual([boardPut('b2', 'read')])
  })

  it('Save applies children, revokes and grants through the existing APIs, with a one-line summary', async () => {
    const state = { perms: perms({ boards: [boardFile] }) }
    serve(state, (url, init) => init?.method === 'PUT' && url.endsWith('/child-approval') ? ok(approval({ revision: 'rev-b', runtimes: [runtime('docker', { enabled: true, granted_engines: ['claude', 'codex'] }), runtime('daemon')] })) : undefined)
    mount()
    const dialog = await openModal()
    fireEvent.click(await within(dialog).findByRole('checkbox', { name: 'Start sandboxed children without asking' }))
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Product' }), { target: { value: 'propose' } })
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'Run Tracker · create_issue without asking' }))
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 2, revoke 1')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts()).toEqual([
      ['/api/sessions/s1/child-approval', { runtime: 'docker', enabled: true, revision: 'rev-a' }],
      boardPut('b1', 'propose'),
      ['/api/sessions/s1/proposal-grants', { kind: 'mcp_tool', connection_id: 'c2', tool: 'create_issue' }],
    ])
    expect(await screen.findByText('Saved: grant 2, revoke 1.')).toBeInTheDocument()
  })

  it('lists what is active with how and when, and Revoke there is staged until Save; Cancel throws it away', async () => {
    serve({ perms: perms({ boards: [boardFile] }) })
    mount([{ id: 'd1', engine: 'codex', runtime: 'docker' }])
    const dialog = await openModal()
    const rows = await within(dialog).findAllByTestId('pm-active-row')
    expect(rows[0]).toHaveTextContent('Start Codex children · Local sandbox')
    expect(rows[1]).toHaveTextContent('Product · File without asking')
    expect(rows[1]).toHaveTextContent(/Given from a card in the chat, /)
    fireEvent.click(within(rows[1]).getByRole('button', { name: 'Revoke: Product · File without asking' }))
    expect(rows[1]).toHaveTextContent('Revoked on Save')
    expect(await within(dialog).findByRole('combobox', { name: 'Access to Product' })).toHaveValue('propose')
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Revoke 1')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(posts()).toEqual([])
  })

  it('stays open after a partial failure, keeps the failed choices ticked and marks them; what applied is applied', async () => {
    const state = { perms: perms() }
    serve(state, (url, init) => {
      if (url.endsWith('/proposal-grants') && init?.method === 'POST') return refused(409, 'A cron session cannot.')
      if (url.endsWith('/board-access') && init?.method === 'PUT') {
        const body = JSON.parse(String(init.body))
        if (body.board_id === 'b3') return refused(404, 'That board is not available to this account.')
        state.perms = perms({ boards: [{ board_id: 'b2', board_name: 'Ops', level: 'read', grant_id: 'g9', created_via: 'panel', created_at: '2026-10-20T10:00:00Z', active: true }] })
        return ok({ boards: [] })
      }
      return undefined
    })
    mount()
    const dialog = await openModal()
    fireEvent.click(await within(dialog).findByRole('checkbox', { name: 'Run Mail · send without asking' }))
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Ops' }), { target: { value: 'read' } })
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Product research' }), { target: { value: 'full' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Not everything was saved: That board is not available to this account. (and 1 more)')
    const tool = within(dialog).getByRole('checkbox', { name: 'Run Mail · send without asking' })
    expect(tool).toBeChecked()
    expect(tool.closest('li')).toHaveTextContent('Not saved')
    const failedBoard = within(dialog).getByRole('combobox', { name: 'Access to Product research' })
    expect(failedBoard).toHaveValue('full')
    expect(failedBoard.closest('li')).toHaveTextContent('Not saved')
    const board = within(dialog).getByRole('combobox', { name: 'Access to Ops' })
    expect(board).toHaveValue('read')
    expect(board.closest('li')).not.toHaveTextContent('Not saved')
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 2')
  })

  it('names saved and failed rows and retries only the failed rows', async () => {
    const state = { perms: perms() }
    let retry = false
    serve(state, (url, init) => {
      if (url.endsWith('/board-access') && init?.method === 'PUT') {
        const body = JSON.parse(String(init.body))
        if (body.board_id === 'b3' && !retry) return refused(503, 'Try again later.')
        state.perms = perms({ boards: [{ ...boardFile, board_id: 'b2', board_name: 'Ops', level: 'read' }, ...(retry ? [{ ...boardFile, board_id: 'b3', board_name: 'Product research', level: 'full' }] : [])] })
        return ok({})
      }
    })
    mount()
    const dialog = await openModal()
    fireEvent.change(await within(dialog).findByRole('combobox', { name: 'Access to Ops' }), { target: { value: 'read' } })
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Product research' }), { target: { value: 'full' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    expect(await within(dialog).findByRole('region', { name: 'Save results' })).toHaveTextContent('Saved: Ops')
    expect(within(dialog).getByRole('region', { name: 'Save results' })).toHaveTextContent('Not saved: Product research')
    expect(within(dialog).getByRole('combobox', { name: 'Access to Ops' }).closest('li')).toHaveTextContent('Saved')
    retry = true
    fireEvent.click(within(dialog).getByRole('button', { name: 'Retry failed changes' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts().filter(([url]) => String(url).endsWith('/board-access')).map(([, body]) => body.board_id)).toEqual(['b2', 'b3', 'b3'])
  })

  it('clears stale save results once the person edits again', async () => {
    const state = { perms: perms() }
    serve(state, (url, init) => {
      if (url.endsWith('/board-access') && init?.method === 'PUT') {
        const body = JSON.parse(String(init.body))
        if (body.board_id === 'b3') return refused(503, 'Try again later.')
        state.perms = perms({ boards: [{ ...boardFile, board_id: 'b2', board_name: 'Ops', level: 'read' }] })
        return ok({})
      }
    })
    mount()
    const dialog = await openModal()
    fireEvent.change(await within(dialog).findByRole('combobox', { name: 'Access to Ops' }), { target: { value: 'read' } })
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Product research' }), { target: { value: 'full' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    expect(await within(dialog).findByRole('region', { name: 'Save results' })).toHaveTextContent('Saved: Ops')
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Ops' }), { target: { value: 'full' } })
    expect(within(dialog).queryByRole('region', { name: 'Save results' })).toBeNull()
    expect(within(dialog).getByRole('combobox', { name: 'Access to Ops' }).closest('li')).not.toHaveTextContent('Saved')
  })

  it('windows a large board list and retains edits between pages and searches', async () => {
    serve({ perms: perms() }, (url) => url === '/api/session-boards' ? ok({ configured: true, boards: Array.from({ length: 300 }, (_, i) => ({ id: `many-${i}`, name: `Board ${i}` })) }) : undefined)
    mount()
    const dialog = await openModal()
    await within(dialog).findByRole('combobox', { name: 'Access to Board 0' })
    expect(within(dialog).getAllByRole('combobox').length).toBeLessThanOrEqual(25)
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Access to Board 0' }), { target: { value: 'read' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Next boards' }))
    expect(within(dialog).getByRole('combobox', { name: 'Access to Board 25' })).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Previous boards' }))
    expect(within(dialog).getByRole('combobox', { name: 'Access to Board 0' })).toHaveValue('read')
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: 'Board 299' } })
    expect(within(dialog).getByRole('combobox', { name: 'Access to Board 299' })).toBeInTheDocument()
  })

  it('a backdrop click does not close the dialog while it saves, nor with unsaved changes', async () => {
    let finish!: () => void
    serve({ perms: perms() }, (url, init) => url.endsWith('/board-access') && init?.method === 'PUT' ? new Promise(resolve => { finish = () => resolve(ok({})) }) : undefined)
    mount()
    const dialog = await openModal()
    const overlay = screen.getByTestId('permissions-overlay')
    fireEvent.change(await within(dialog).findByRole('combobox', { name: 'Access to Ops' }), { target: { value: 'read' } })
    fireEvent.pointerDown(overlay); fireEvent.click(overlay)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    fireEvent.pointerDown(overlay); fireEvent.click(overlay)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    finish()
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('a backdrop click closes the dialog when nothing is changed', async () => {
    serve({ perms: perms() })
    mount()
    await openModal()
    const overlay = screen.getByTestId('permissions-overlay')
    fireEvent.pointerDown(overlay); fireEvent.click(overlay)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('says why a session cannot be given board or tool permissions, and offers no toggles for them', async () => {
    serve({ perms: perms({ can_grant: false, reason: "A restricted session's proposals always wait for you." }) })
    mount()
    const dialog = await openModal()
    expect(await within(dialog).findByTestId('pm-grant-reason')).toHaveTextContent('restricted')
    expect(within(dialog).queryByRole('combobox', { name: /^Access to/ })).toBeNull()
    expect(within(dialog).queryByRole('checkbox', { name: /^Run / })).toBeNull()
    expect(request.mock.calls.some(([u]) => u === '/api/session-boards')).toBe(false)
  })

  it('shows the Cluster row of a desktop session beside its desktop rows, each saying where its children run, and saves it', async () => {
    const state = { perms: perms(), approval: approval({ runtimes: [runtime('docker'), runtime('daemon'), runtime('cluster', { allowed_engines: ['claude'] })] }) }
    serve(state, (url, init) => init?.method === 'PUT' && url.endsWith('/child-approval') ? ok(approval({ revision: 'rev-b', runtimes: [runtime('docker'), runtime('daemon'), runtime('cluster', { enabled: true, allowed_engines: ['claude'], granted_engines: ['claude'] })] })) : undefined)
    mount()
    const dialog = await openModal()
    const box = await within(dialog).findByRole('checkbox', { name: 'Start children on the cluster without asking' })
    expect(box).toBeEnabled()
    expect(within(dialog).getByTestId('pm-children-cluster')).toHaveTextContent('On the cluster.')
    expect(within(dialog).getByTestId('pm-children-docker')).toHaveTextContent('On this desktop.')
    expect(within(dialog).getByTestId('pm-children-daemon')).toHaveTextContent('On this desktop.')
    // No wholesale "on this desktop" for every row.
    expect(dialog.textContent).not.toMatch(/Children of this session on this desktop/)
    fireEvent.click(box)
    fireEvent.click(within(dialog).getByRole('button', { name: /^Save/ }))
    await waitFor(() => expect(posts()).toContainEqual(['/api/sessions/s1/child-approval', { runtime: 'cluster', enabled: true, revision: 'rev-a' }]))
  })

  it('has no Cluster row when the server reports none (an install without a cluster)', async () => {
    serve({ perms: perms() })
    mount()
    const dialog = await openModal()
    await within(dialog).findByRole('checkbox', { name: 'Start sandboxed children without asking' })
    expect(within(dialog).queryByRole('checkbox', { name: 'Start children on the cluster without asking' })).toBeNull()
  })

  it('shows a runtime the server will not enable as disabled, with its reason', async () => {
    serve({ perms: perms(), approval: approval({ runtimes: [runtime('docker'), runtime('daemon', { can_enable: false, reason: 'A sandboxed parent cannot pre-approve unsandboxed children.' })] }) })
    mount()
    const dialog = await openModal()
    const box = await within(dialog).findByRole('checkbox', { name: 'Start unsandboxed children without asking' })
    expect(box).toBeDisabled()
    expect(within(dialog).getByTestId('pm-children-daemon')).toHaveTextContent('A sandboxed parent cannot pre-approve unsandboxed children.')
  })

  it('marks a tool whose definition changed since it was allowed', async () => {
    serve({ perms: perms({ grants: [{ ...toolGrant, active: false, inactive: 'tool_changed' }] }) })
    mount()
    const dialog = await openModal()
    const row = (await within(dialog).findByRole('checkbox', { name: 'Run Mail · send without asking' })).closest('li')!
    expect(row).toHaveTextContent('Its definition changed since it was allowed')
  })

  it('a chat card opens the modal at the children', async () => {
    serve({ perms: perms() })
    mount([], 1)
    const dialog = await screen.findByRole('dialog', { name: 'Permissions' })
    const box = await within(dialog).findByRole('checkbox', { name: 'Start sandboxed children without asking' })
    await waitFor(() => expect(document.activeElement).toBe(box))
  })

  it('still manages children when board and tool permissions cannot be read, and says why the rest is missing', async () => {
    serve({ perms: perms() }, url => url.endsWith('/permissions') ? { ok: false, status: 500, json: async () => ({}) } : undefined)
    mount()
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load the session permissions.')
    const dialog = await openModal()
    expect(await within(dialog).findByRole('checkbox', { name: 'Start sandboxed children without asking' })).toBeEnabled()
    expect(within(dialog).getByTestId('pm-grant-reason')).toHaveTextContent('could not be loaded')
  })

  it('Esc in a non-empty search clears it first; a second Esc closes the dialog', async () => {
    serve({ perms: perms() })
    mount()
    const dialog = await openModal()
    const search = within(dialog).getByRole('searchbox', { name: 'Search permissions' })
    fireEvent.change(search, { target: { value: 'ops' } })
    fireEvent.keyDown(search, { key: 'Escape' })
    expect(search).toHaveValue('')
    expect(screen.getByRole('dialog', { name: 'Permissions' })).toBeInTheDocument()
    fireEvent.keyDown(search, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('Esc closes without saving', async () => {
    serve({ perms: perms() })
    mount()
    const dialog = await openModal()
    fireEvent.change(await within(dialog).findByRole('combobox', { name: 'Access to Ops' }), { target: { value: 'full' } })
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(posts()).toEqual([])
  })
})
