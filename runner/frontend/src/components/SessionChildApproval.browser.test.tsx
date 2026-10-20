import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import '../index.css'
import './SessionRail.css'
import SessionChildApproval from './SessionChildApproval'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
afterEach(() => apiFetch.mockReset())
const runtime = (runtime: string) => ({runtime, enabled:false, partial:false, can_enable:true, reason:'', allowed_engines:['claude','codex','hermes'],granted_engines:[],standing_rule_engines:[],effective_allowed_engines:[]})
const view = {revision:'revision-a',daemon_id:'desktop-a',allowed_engines:['claude','codex','hermes'],runtimes:[runtime('docker'),runtime('daemon')]}
it.each([390,1280])('keeps child approval readable and keyboard operable at %i',async width=>{
 await page.viewport(width,700)
 apiFetch.mockImplementation(async (_url, init)=>new Response(JSON.stringify(init?.method==='PUT'?{...view,runtimes:[{...runtime('docker'),enabled:true,granted_engines:['claude','codex','hermes']},runtime('daemon')]}:view)))
 const mount=render(<div className="session-rail panel" style={{width:300,padding:12}}><SessionChildApproval sessionId="parent-a" /></div>)
 const checkbox=await screen.findByRole('checkbox',{name:'Start sandboxed children without asking'})
 expect(checkbox).not.toBeChecked()
 checkbox.focus();await userEvent.keyboard(' ')
 await waitFor(()=>expect(checkbox).toBeChecked())
 expect(screen.getByRole('checkbox',{name:/unsandboxed/})).not.toBeChecked()
 expect(apiFetch).toHaveBeenCalledTimes(2)
 expect(mount.container.scrollWidth).toBeLessThanOrEqual(width)
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
 expect(screen.getByTestId('session-child-approval').getBoundingClientRect().height).toBeLessThan(460)
})
