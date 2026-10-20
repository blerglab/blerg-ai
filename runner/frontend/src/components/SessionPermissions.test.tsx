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
const boardGrant = { id: 'g1', kind: 'board_file', board_id: 'b1', board_name: 'Product', created_via: 'card', created_at: '2026-10-20T10:00:00Z' }
type Perms = { can_grant: boolean; reason: string; grants: unknown[]; mcp_tools: unknown[] }
const perms = (over: Partial<Perms> = {}): Perms => ({ can_grant: true, reason: '', grants: [], mcp_tools: [{ connection_id: 'c1', connection_name: 'Mail', tool: 'send' }, { connection_id: 'c1', connection_name: 'Mail', tool: 'delete' }, { connection_id: 'c2', connection_name: 'Tracker', tool: 'create_issue' }], ...over })
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
const posts = () => request.mock.calls.filter(([, i]) => i?.method === 'POST' || i?.method === 'PUT').map(([u, i]) => [u, i?.body ? JSON.parse(i.body) : undefined])
async function openModal() {
  fireEvent.click(await screen.findByRole('button', { name: 'Manage' }))
  return screen.getByRole('dialog', { name: 'Permissions' })
}

describe('the panel summary', () => {
  beforeEach(() => { request.mockReset() })

  it('shows the title, how many are active, Manage, and each active permission as a chip with ×', async () => {
    serve({ perms: perms({ grants: [boardGrant] }) })
    mount([{ id: 'd1', engine: 'codex', runtime: 'docker' }])
    const block = screen.getByTestId('rail-permissions')
    expect(within(block).getByRole('heading', { name: 'Permissions' })).toBeInTheDocument()
    expect(await within(block).findByTestId('perm-count')).toHaveTextContent('2 active')
    const chips = within(within(block).getByTestId('perm-active')).getAllByRole('listitem')
    expect(chips.map(c => c.textContent)).toEqual(['↳Codex · Local sandbox×', '▦Product×'])
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

  it('revokes a chip inline with ×', async () => {
    const state = { perms: perms({ grants: [boardGrant] }) }
    serve(state, url => { if (url === '/api/proposal-grants/g1/revoke') { state.perms = perms(); return ok({ revoked: true }) } })
    mount()
    const x = await screen.findByRole('button', { name: 'Revoke: File cards on Product without asking' })
    expect(x).toHaveClass('perm-chip-x')
    fireEvent.click(x)
    await waitFor(() => expect(request).toHaveBeenCalledWith('/api/proposal-grants/g1/revoke', { method: 'POST' }))
    await waitFor(() => expect(screen.getByTestId('perm-count')).toHaveTextContent('0 active'))
  })

  it('keeps a grant that needs you signed in apart, with why, and its × revokes it', async () => {
    serve({ perms: perms({ grants: [{ ...boardGrant, active: false, inactive: 'signed_out' }, { id: 'g2', kind: 'mcp_tool', connection_id: 'c1', connection_name: 'Mail', tool: 'send', created_via: 'panel', created_at: '2026-10-20T10:00:00Z', active: false, inactive: 'tool_changed' }] }) })
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
    serve({ perms: perms({ grants: [boardGrant] }) }, url => url.endsWith('/revoke') ? refused(409, 'The session has ended.') : undefined)
    mount()
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke: File cards on Product without asking' }))
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
    expect(await within(dialog).findByRole('checkbox', { name: 'File cards on Ops without asking' })).toBeInTheDocument()
    fireEvent.change(within(dialog).getByRole('searchbox', { name: 'Search permissions' }), { target: { value: 'product' } })
    expect(within(dialog).queryByRole('checkbox', { name: 'File cards on Ops without asking' })).toBeNull()
    expect(within(dialog).getByRole('checkbox', { name: 'File cards on Product without asking' })).toBeInTheDocument()
    expect(within(dialog).getByRole('checkbox', { name: 'File cards on Product research without asking' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('checkbox', { name: /^Run / })).toBeNull()
    expect(within(dialog).getByText('No tools match “product”.')).toBeInTheDocument()
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: 'mail del' } })
    expect(within(dialog).getByRole('checkbox', { name: 'Run Mail · delete without asking' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('checkbox', { name: 'Run Mail · send without asking' })).toBeNull()
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: 'codex' } })
    expect(within(dialog).getByRole('checkbox', { name: 'Start sandboxed children without asking' })).toBeInTheDocument()
  })

  it('"Select all shown" selects only what the filter shows, counts it, and Save grants exactly those', async () => {
    serve({ perms: perms() })
    mount()
    const dialog = await openModal()
    await within(dialog).findByRole('checkbox', { name: 'File cards on Ops without asking' })
    expect(within(dialog).getByTestId('pm-board-count')).toHaveTextContent('0 of 3 selected')
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: 'product' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Select all shown' }))
    expect(within(dialog).getByTestId('pm-board-count')).toHaveTextContent('2 of 3 selected')
    expect(within(dialog).getByRole('button', { name: 'Clear shown' })).toBeInTheDocument()
    fireEvent.change(within(dialog).getByRole('searchbox'), { target: { value: '' } })
    expect(within(dialog).getByRole('checkbox', { name: 'File cards on Ops without asking' })).not.toBeChecked()
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 2')
    // Nothing is sent before Save.
    expect(posts()).toEqual([])
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts()).toEqual([
      ['/api/sessions/s1/proposal-grants', { kind: 'board_file', board_id: 'b1' }],
      ['/api/sessions/s1/proposal-grants', { kind: 'board_file', board_id: 'b3' }],
    ])
    expect(await screen.findByText('Saved: grant 2.')).toBeInTheDocument()
  })

  it('Save applies children, revokes and grants through the existing APIs, with a one-line summary', async () => {
    const state = { perms: perms({ grants: [boardGrant] }) }
    serve(state, (_url, init) => init?.method === 'PUT' ? ok(approval({ revision: 'rev-b', runtimes: [runtime('docker', { enabled: true, granted_engines: ['claude', 'codex'] }), runtime('daemon')] })) : undefined)
    mount()
    const dialog = await openModal()
    fireEvent.click(await within(dialog).findByRole('checkbox', { name: 'Start sandboxed children without asking' }))
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'File cards on Product without asking' }))
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'Run Tracker · create_issue without asking' }))
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 2, revoke 1')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(posts()).toEqual([
      ['/api/sessions/s1/child-approval', { runtime: 'docker', enabled: true, revision: 'rev-a' }],
      ['/api/proposal-grants/g1/revoke', undefined],
      ['/api/sessions/s1/proposal-grants', { kind: 'mcp_tool', connection_id: 'c2', tool: 'create_issue' }],
    ])
    expect(await screen.findByText('Saved: grant 2, revoke 1.')).toBeInTheDocument()
  })

  it('lists what is active with how and when, and Revoke there is staged until Save; Cancel throws it away', async () => {
    serve({ perms: perms({ grants: [boardGrant] }) })
    mount([{ id: 'd1', engine: 'codex', runtime: 'docker' }])
    const dialog = await openModal()
    const rows = await within(dialog).findAllByTestId('pm-active-row')
    expect(rows[0]).toHaveTextContent('Start Codex children · Local sandbox')
    expect(rows[1]).toHaveTextContent('File cards on Product without asking')
    expect(rows[1]).toHaveTextContent(/Given from a card in the chat, /)
    fireEvent.click(within(rows[1]).getByRole('button', { name: 'Revoke: File cards on Product without asking' }))
    expect(rows[1]).toHaveTextContent('Revoked on Save')
    expect(within(dialog).getByRole('checkbox', { name: 'File cards on Product without asking' })).not.toBeChecked()
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Revoke 1')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(posts()).toEqual([])
  })

  it('stays open after a partial failure, keeps the failed choices ticked and marks them; what applied is applied', async () => {
    const state = { perms: perms() }
    serve(state, (url, init) => {
      if (!(url.endsWith('/proposal-grants') && init?.method === 'POST')) return undefined
      const body = JSON.parse(String(init.body))
      if (body.kind === 'mcp_tool') return refused(409, 'A cron session cannot.')
      state.perms = perms({ grants: [{ id: 'g9', kind: 'board_file', board_id: 'b2', board_name: 'Ops', created_via: 'panel', created_at: '2026-10-20T10:00:00Z' }] })
      return ok({})
    })
    mount()
    const dialog = await openModal()
    fireEvent.click(await within(dialog).findByRole('checkbox', { name: 'Run Mail · send without asking' }))
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'File cards on Ops without asking' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Not everything was saved: A cron session cannot.')
    const tool = within(dialog).getByRole('checkbox', { name: 'Run Mail · send without asking' })
    expect(tool).toBeChecked()
    expect(tool.closest('li')).toHaveTextContent('Not saved')
    const board = within(dialog).getByRole('checkbox', { name: 'File cards on Ops without asking' })
    expect(board).toBeChecked()
    expect(board.closest('li')).not.toHaveTextContent('Not saved')
    expect(within(dialog).getByTestId('pm-summary')).toHaveTextContent('Grant 1')
  })

  it('a backdrop click does not close the dialog while it saves, nor with unsaved changes', async () => {
    let finish!: () => void
    serve({ perms: perms() }, (url, init) => url.endsWith('/proposal-grants') && init?.method === 'POST' ? new Promise(resolve => { finish = () => resolve(ok({})) }) : undefined)
    mount()
    const dialog = await openModal()
    const overlay = screen.getByTestId('permissions-overlay')
    fireEvent.click(await within(dialog).findByRole('checkbox', { name: 'File cards on Ops without asking' }))
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
    expect(within(dialog).queryByRole('checkbox', { name: /File cards|^Run / })).toBeNull()
    expect(request.mock.calls.some(([u]) => u === '/api/session-boards')).toBe(false)
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
    serve({ perms: perms({ grants: [{ id: 'g2', kind: 'mcp_tool', connection_id: 'c1', connection_name: 'Mail', tool: 'send', created_via: 'panel', created_at: '2026-10-20T10:00:00Z', active: false, inactive: 'tool_changed' }] }) })
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
    fireEvent.click(await within(dialog).findByRole('checkbox', { name: 'File cards on Ops without asking' }))
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(posts()).toEqual([])
  })
})
