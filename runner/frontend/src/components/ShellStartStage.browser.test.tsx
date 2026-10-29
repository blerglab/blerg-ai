import { createRef } from 'react'
import { act, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { WebSocketShell, ShellRateGroup } from '../shellTransport'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import type { ShellWorkspaceHandle } from './ShellWorkspace'
import '../index.css'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
// ensureFreshToken answers whether a token was stored, as the real one does.
vi.mock('../authClient', () => ({ ensureFreshToken: async () => true, getAccessToken: () => 'fixture-token' }))
afterEach(() => { localStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
it.each([390,3840])('shows the redacted launch stage in the shell popover at %i',async width=>{
 await page.viewport(width,width===390?844:2160)
 apiFetch.mockResolvedValue(new Response(JSON.stringify({code:'runtime_start_refused',cause:'containment_setup',error:'Bearer fixture-secret'}),{status:403}))
 const ref=createRef<ShellWorkspaceHandle>()
 render(<MemoryRouter><div style={{position:'fixed',inset:0}}>
  <ShellWorkspace ref={ref} accountId="owner" sessionId="stage" restoreShells={async()=>[]}><textarea aria-label="Composer" /></ShellWorkspace>
 </div></MemoryRouter>)
 await act(async()=>{ref.current?.open()})
 const error=await screen.findByRole('alertdialog',{name:"Shell didn't start"})
 expect(error).toHaveTextContent("Shell containment could not be established. Check the service's tracing and seccomp restrictions.")
 expect(error).not.toHaveTextContent('fixture-secret')
 const box=error.getBoundingClientRect()
 expect(box.left).toBeGreaterThanOrEqual(0);expect(box.right).toBeLessThanOrEqual(innerWidth)
 expect(error.scrollWidth).toBeLessThanOrEqual(error.clientWidth)
 expect(within(error).getByRole('button',{name:'Dismiss'})).toHaveFocus()
 await userEvent.keyboard('{Enter}')
 expect(screen.queryByRole('alertdialog',{name:"Shell didn't start"})).toBeNull()
})

it.each([390, 3840])('shows a live control failure in the popover at %i', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 class Socket {
  static OPEN = 1
  static instance: Socket
  readyState = 1
  bufferedAmount = 0
  onopen?: () => void
  onmessage?: (event: {data: string}) => void
  constructor() { Socket.instance = this }
  send() {}
  close() {}
 }
 vi.stubGlobal('WebSocket', Socket)
 const transport = new WebSocketShell({handle:'live',shell_id:'live',runtime:'host',folder:'project'}, new ShellRateGroup())
 const ref = createRef<ShellWorkspaceHandle>()
 render(<MemoryRouter><div style={{position:'fixed',inset:0}}>
  <ShellWorkspace ref={ref} accountId="owner" sessionId="live" createShell={async()=>transport} restoreShells={async()=>[]}><textarea aria-label="Composer" /></ShellWorkspace>
 </div></MemoryRouter>)
 await act(async()=>{ref.current?.open()})
 await act(async()=>{
  await transport.reconnect()
  Socket.instance.onopen?.()
  Socket.instance.onmessage?.({data:JSON.stringify({type:'error',cause:'input_sequence',error:'Bearer fixture-secret'})})
 })
 const error=await screen.findByRole('alertdialog',{name:'Shell stopped'})
 expect(error).toHaveTextContent('The shell helper rejected the input sequence.')
 expect(error).not.toHaveTextContent('before confirming startup')
 expect(error).not.toHaveTextContent('fixture-secret')
 const box=error.getBoundingClientRect()
 expect(box.left).toBeGreaterThanOrEqual(0);expect(box.right).toBeLessThanOrEqual(innerWidth)
 expect(error.scrollWidth).toBeLessThanOrEqual(error.clientWidth)
 expect(within(error).getByRole('button',{name:'Dismiss'})).toHaveFocus()
 await userEvent.keyboard('{Enter}')
 expect(screen.queryByRole('alertdialog',{name:'Shell stopped'})).toBeNull()
})
