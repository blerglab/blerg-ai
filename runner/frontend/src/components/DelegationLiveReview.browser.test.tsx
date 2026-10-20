// Real browser proof: no transcript history is needed to review a pending approval in the rail.
import { afterEach, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import '@blerglab/chat/blerg.css'
import '../index.css'
import DelegationLiveReview from './DelegationLiveReview'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
afterEach(() => apiFetch.mockReset())
it.each([390, 1280])('reviews the full prompt and keyboard choices at width %i', async width => {
 await page.viewport(width,700)
 const request={request_id:'request-a',parent_session_id:'parent-a',status:'pending',expires_at:'2099-01-01T00:00:00Z',offered:['once','deny'],request:{engine:'codex',runtime:'docker',title:'Review fixture',prompt:'Public fixture instructions '.repeat(50)+'TAIL_FIXTURE'}}
 apiFetch.mockResolvedValue(new Response(JSON.stringify(request),{headers:{'Content-Type':'application/json'}}))
 render(<div className="rail-body" style={{width:300,maxWidth:'100%',height:550,overflow:'auto'}}><MemoryRouter><DelegationLiveReview sessionId="parent-a" requestId="request-a" /></MemoryRouter></div>)
 const open=screen.getByRole('button',{name:'Review request'});open.focus();await userEvent.keyboard('{Enter}')
 const all=await screen.findByRole('button',{name:'Show all'});all.focus();await userEvent.keyboard('{Enter}')
 expect(await screen.findByText(/TAIL_FIXTURE/)).toBeVisible()
 const allow=screen.getByRole('button',{name:'Allow once'});allow.focus()
 expect(document.activeElement).toBe(allow)
 expect(allow.getBoundingClientRect().width).toBeGreaterThan(20)
 expect(apiFetch).toHaveBeenCalledTimes(1)
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
