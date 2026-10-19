import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { ChildSessionRow, StartedSessions } from './ChildSessions'
import { OpenSessionContext } from '../lib/openSession'
import type { LiveChildSession } from '../lib/liveState'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
const child: LiveChildSession = { id: 'child', title: 'Review runner', engine: 'codex', model: 'fixture-model', effort: 'high', status: 'running', started_at: '2026-10-17T10:00:00Z', ended_at: null, asking: true, tokens: 12000 }
const now = Date.parse('2026-10-17T10:02:00Z')
beforeEach(() => {apiFetch.mockReset()})
describe('started-session rows', () => {
  it('shows actual engine/model/effort/tokens/asking and opens the child in app', () => {
    const open = vi.fn()
    render(<MemoryRouter><OpenSessionContext.Provider value={open}><ul><ChildSessionRow child={child} now={now} /></ul></OpenSessionContext.Provider></MemoryRouter>)
    expect(screen.getByLabelText('Codex')).toHaveTextContent('Cx')
    expect(screen.getByText('fixture-model')).toBeInTheDocument()
    expect(screen.getByText('high')).toBeInTheDocument()
    expect(screen.getByText('asking')).toBeInTheDocument()
    expect(screen.getByText(/tokens/)).toHaveTextContent('12k')
    expect(screen.queryByText(/\$/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button'))
    expect(open).toHaveBeenCalledWith('child')
  })
  it('shows cost only when provided and never guesses hidden tokens', () => {
    render(<MemoryRouter><ul><ChildSessionRow child={{ ...child, tokens: null, cost_usd: 1.25 }} now={now} /></ul></MemoryRouter>)
    expect(screen.getByText(/1.25/)).toBeInTheDocument()
    expect(screen.queryByText(/tokens/)).not.toBeInTheDocument()
  })
  it('keeps every child in a persistent section when its status changes', () => {
    const children = Array.from({ length: 7 }, (_, i) => ({...child,id:`c${i}`,title:`Child ${i}`}))
    const view=render(<MemoryRouter><StartedSessions sessionId="parent" children={children} grants={[]} now={now}/></MemoryRouter>)
    expect(screen.getAllByTestId('rail-child')).toHaveLength(7)
    const row=screen.getByText('Child 0').closest('li')
    view.rerender(<MemoryRouter><StartedSessions sessionId="parent" children={children.map(c=>({...c,status:'ended',ended_at:'2026-10-17T10:01:00Z'}))} grants={[]} now={now}/></MemoryRouter>)
    expect(screen.getAllByTestId('rail-child')).toHaveLength(7)
    expect(screen.getByText('Child 0').closest('li')).toBe(row)
    expect(screen.queryByRole('button',{name:/Show all/})).toBeNull()
  })
  it('requires confirmation and keeps the child visible until authoritative archived data arrives', async () => {
    apiFetch.mockResolvedValue({ok:true})
    const view=render(<MemoryRouter><StartedSessions sessionId="parent /" children={[child]} grants={[]} now={now}/></MemoryRouter>)
    fireEvent.click(screen.getByRole('button',{name:'Discard Review runner'}))
    expect(screen.getByRole('dialog',{name:'Discard child session?'})).toBeInTheDocument()
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button',{name:'Cancel'}))
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button',{name:'Discard Review runner'}))
    fireEvent.click(screen.getByRole('button',{name:'Discard child session'}))
    await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/parent%20%2F/children/child/discard',{method:'POST'}))
    expect(screen.getAllByTestId('rail-child')).toHaveLength(1)
    view.rerender(<MemoryRouter><StartedSessions sessionId="parent /" children={[{...child,archived_at:'2026-10-19T12:00:00Z'}]} grants={[]} now={now}/></MemoryRouter>)
    expect(screen.queryByTestId('rail-child')).toBeNull()
  })
  it('leaves a refused child discard visible and reports the failure',async()=>{
    apiFetch.mockResolvedValue({ok:false})
    render(<MemoryRouter><StartedSessions sessionId="parent" children={[child]} grants={[]} now={now}/></MemoryRouter>)
    fireEvent.click(screen.getByRole('button',{name:'Discard Review runner'}))
    fireEvent.click(screen.getByRole('button',{name:'Discard child session'}))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not discard')
    expect(screen.getAllByTestId('rail-child')).toHaveLength(1)
  })
  it('revokes the exact grant and reports a refusal without pretending success', async () => {
    apiFetch.mockResolvedValue({ ok: false })
    render(<MemoryRouter><StartedSessions sessionId="parent" children={[]} grants={[{ id: 'grant-id', engine: 'codex', runtime: 'docker' }]} now={now} /></MemoryRouter>)
    fireEvent.click(screen.getByRole('button', { name: /Revoke Codex/ }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/delegation-grants/grant-id/revoke', { method: 'POST' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not revoke')
  })
})


it.each([false, true])('shows the token unit exactly once in compact=%s rows', compact => {
  render(<MemoryRouter><ul><ChildSessionRow child={{...child, tokens: 0}} now={now} compact={compact}/></ul></MemoryRouter>)
  expect(screen.getByText('0 tokens', {exact: true})).toBeInTheDocument()
  expect(screen.queryByText(/tokens tokens/)).toBeNull()
})

it('groups legacy repo grants by engine/runtime/daemon and revokes every grant in that scope', async () => {
  apiFetch.mockResolvedValue({ok: true})
  const grants = ['one', 'two'].map(id => ({id, engine: 'codex', runtime: 'docker', daemon_id: 'desktop', daemon_name: 'Workstation', repo: id}))
  render(<MemoryRouter><StartedSessions sessionId="parent" children={[]} grants={grants} now={now}/></MemoryRouter>)
  const buttons = screen.getAllByRole('button', {name: 'Revoke Codex · Local sandbox'})
  expect(buttons).toHaveLength(1)
  expect(buttons[0]).toHaveTextContent('Revoke Codex · Local sandbox')
  fireEvent.click(buttons[0])
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  for (const id of ['one', 'two']) expect(apiFetch).toHaveBeenCalledWith(`/api/delegation-grants/${id}/revoke`, {method: 'POST'})
})

it('distinguishes grants on different daemons without listing repository restrictions', () => {
  const grants = ['Desk', 'Laptop'].map(name => ({id: name, engine: 'codex', runtime: 'docker', daemon_id: name, daemon_name: name}))
  render(<MemoryRouter><StartedSessions sessionId="parent" children={[]} grants={grants} now={now}/></MemoryRouter>)
  expect(screen.getByRole('button', {name: 'Revoke Codex · Local sandbox · Desk'})).toBeInTheDocument()
  expect(screen.getByRole('button', {name: 'Revoke Codex · Local sandbox · Laptop'})).toBeInTheDocument()
})


it('waits for every legacy revocation before allowing another attempt after a failure', async () => {
  let finish!: (value: {ok: boolean}) => void
  apiFetch.mockResolvedValueOnce({ok: false}).mockImplementationOnce(() => new Promise(resolve => {finish = resolve}))
  const grants = ['one', 'two'].map(id => ({id, engine: 'codex', runtime: 'docker', daemon_id: 'desktop'}))
  render(<MemoryRouter><StartedSessions sessionId="parent" children={[]} grants={grants} now={now}/></MemoryRouter>)
  const button = screen.getByRole('button', {name: 'Revoke Codex · Local sandbox'})
  fireEvent.click(button)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  expect(button).toBeDisabled()
  expect(screen.queryByRole('alert')).toBeNull()
  finish({ok: true})
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not revoke')
  expect(button).toBeEnabled()
})
