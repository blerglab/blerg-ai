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
    const view=render(<MemoryRouter><StartedSessions sessionId="parent" children={children} now={now}/></MemoryRouter>)
    expect(screen.getAllByTestId('rail-child')).toHaveLength(7)
    const row=screen.getByText('Child 0').closest('li')
    view.rerender(<MemoryRouter><StartedSessions sessionId="parent" children={children.map(c=>({...c,status:'ended',ended_at:'2026-10-17T10:01:00Z'}))} now={now}/></MemoryRouter>)
    expect(screen.getAllByTestId('rail-child')).toHaveLength(7)
    expect(screen.getByText('Child 0').closest('li')).toBe(row)
    expect(screen.queryByRole('button',{name:/Show all/})).toBeNull()
  })
  it('requires confirmation and keeps the child visible until authoritative archived data arrives', async () => {
    apiFetch.mockResolvedValue({ok:true})
    const view=render(<MemoryRouter><StartedSessions sessionId="parent /" children={[child]} now={now}/></MemoryRouter>)
    fireEvent.click(screen.getByRole('button',{name:'Discard Review runner'}))
    expect(screen.getByRole('dialog',{name:'Discard child session?'})).toBeInTheDocument()
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button',{name:'Cancel'}))
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button',{name:'Discard Review runner'}))
    fireEvent.click(screen.getByRole('button',{name:'Discard child session'}))
    await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/parent%20%2F/children/child/discard',{method:'POST'}))
    expect(screen.getAllByTestId('rail-child')).toHaveLength(1)
    view.rerender(<MemoryRouter><StartedSessions sessionId="parent /" children={[{...child,archived_at:'2026-10-19T12:00:00Z'}]} now={now}/></MemoryRouter>)
    expect(screen.queryByTestId('rail-child')).toBeNull()
  })
  it('leaves a refused child discard visible and reports the failure',async()=>{
    apiFetch.mockResolvedValue({ok:false})
    render(<MemoryRouter><StartedSessions sessionId="parent" children={[child]} now={now}/></MemoryRouter>)
    fireEvent.click(screen.getByRole('button',{name:'Discard Review runner'}))
    fireEvent.click(screen.getByRole('button',{name:'Discard child session'}))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not discard')
    expect(screen.getAllByTestId('rail-child')).toHaveLength(1)
  })
})


it.each([false, true])('shows the token unit exactly once in compact=%s rows', compact => {
  render(<MemoryRouter><ul><ChildSessionRow child={{...child, tokens: 0}} now={now} compact={compact}/></ul></MemoryRouter>)
  expect(screen.getByText('0 tokens', {exact: true})).toBeInTheDocument()
  expect(screen.queryByText(/tokens tokens/)).toBeNull()
})





it('shows reported native activity and local usage without guessing a zero before a report',()=>{
 const live={...child,engine:'hermes',tokens:null,usage_status:'pending' as const,tool_calls:13,activity:'running terminal · go test ./...',activity_since:'2026-10-17T10:01:50Z',local:true}
 render(<MemoryRouter><ul><ChildSessionRow child={live} now={now}/></ul></MemoryRouter>)
 expect(screen.getByText('Usage pending')).toBeInTheDocument()
 expect(screen.queryByText('0 tokens')).toBeNull()
 expect(screen.getByText('13 tool calls')).toBeInTheDocument()
 expect(screen.getByText('running terminal · go test ./...')).toBeInTheDocument()
 expect(screen.getByText('Local endpoint')).toBeInTheDocument();expect(screen.queryByText(/\$0/)).toBeNull()
})
it('keeps idle children idle and shows their completed turn without a running activity clock',()=>{
 const done={...child,status:'idle',asking:false,tokens:1021976,usage_status:'reported' as const,tool_calls:32,activity:'Turn complete',last_activity_at:'2026-10-17T10:01:30Z'}
 render(<MemoryRouter><ul><ChildSessionRow child={done} now={now}/></ul></MemoryRouter>)
 expect(screen.getByText('idle')).toBeInTheDocument();expect(screen.queryByText('running')).toBeNull()
 expect(screen.getByText('Turn complete')).toBeInTheDocument();expect(screen.getByText('32 tool calls')).toBeInTheDocument()
 expect(screen.queryByText('Usage pending')).toBeNull()
})
it('never labels hidden usage as pending or local zero',()=>{
 render(<MemoryRouter><ul><ChildSessionRow child={{...child,tokens:null,usage_status:'hidden',tool_calls:2}} now={now}/></ul></MemoryRouter>)
 expect(screen.queryByText('Usage pending')).toBeNull();expect(screen.queryByText(/\$0/)).toBeNull();expect(screen.queryByText(/tokens/)).toBeNull()
})








it('keeps stale archived-parent child activity readable without discard or permission controls', () => {
  render(<MemoryRouter><StartedSessions sessionId="parent" children={[child]} now={now} readOnly/></MemoryRouter>)
  expect(screen.getByTestId('rail-child')).toBeInTheDocument()
  expect(screen.queryByRole('button',{name:/Discard/})).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
})
