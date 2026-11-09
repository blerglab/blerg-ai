import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })
for (const width of [390, 3840]) it(`keeps workspace creation quiet and workers subdued through SessionDetail at ${width}px`, async () => {
 await page.viewport(width, 900)
 localStorage.clear()
 const members = [{ id: 'app', name: 'app', path: 'app' }]
 useMessageStore.setState({ messages: [{id:'ask', session_id:'waiting', kind:'ask', status:'open', answerable:true, owner_only:true, body:'Choose', created_at:'2026-10-31T12:00:00Z'}] })
 useSessionStore.setState({ daemons:[makeDaemon()], sessions:[
  makeSession({id:'sess-a', status:'idle', emoji:'🔎', workspace:{members}}),
  makeSession({id:'channel', title:'Design channel', parent_session_id:'sess-a', status:'idle'}),
  ...['worker', 'other', 'waiting'].map((id, i) => makeSession({id,title:id, emoji:'🔥', parent_session_id:'sess-a', started_by:{kind:'session'}, runtime:i === 1 ? 'cluster' : 'docker', status:i === 2 ? 'waiting' : 'running'})),
  makeSession({id:'done',title:'Finished work',parent_session_id:'sess-a',archived_at:'2026-10-31T12:00:00Z'})
 ] })
 const patches: unknown[] = []
 vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = new URL(String(input), location.origin)
  if (init?.method === 'PATCH') { patches.push(JSON.parse(String(init.body))); return new Response(null,{status:204}) }
  if (url.pathname.endsWith('/workspace')) return json({members, result:{ok:true,generation:1,entries:[]}})
  if (url.pathname.endsWith('/categories')) return json({categories:[]})
  if (url.pathname.endsWith('/shells')) return json([])
  if (url.pathname.endsWith('/shell-capability')) return json({available:false})
  if (url.pathname.endsWith('/live')) return json({can_release_prompt:true})
  return json({})
 }))
 invalidateChatTransport()
 render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></CommandPaletteProvider></MemoryRouter>)
 fireEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 const plus = await screen.findByRole('button',{name:'New channel'})
 expect(plus).toBeVisible()
 expect(plus).toHaveTextContent('+')
 expect(plus).toHaveAttribute('title','New channel')
 const rect = plus.getBoundingClientRect()
 expect(rect.width).toBeGreaterThanOrEqual(44)
 expect(rect.height).toBeGreaterThanOrEqual(44)
 const heading = screen.getByRole('heading', { name: /channels$/i })
 expect(heading.parentElement).toContainElement(plus)
 expect(plus.closest('nav')).toBeNull()
 expect(within(screen.getByRole('tab',{name:'Channels'}).closest('nav')!).queryByRole('button',{name:'New channel'})).toBeNull()
 expect(heading.getBoundingClientRect().bottom).toBeLessThanOrEqual(screen.getByRole('treeitem',{name:'Main'}).getBoundingClientRect().top)
 expect(heading.getBoundingClientRect().left).toBe(screen.getByRole('treeitem',{name:'Main'}).getBoundingClientRect().left + 6)
 expect(rect.right).toBeLessThanOrEqual(width)
 expect(rect.left).toBeGreaterThanOrEqual(0)
 expect(getComputedStyle(plus).borderTopWidth).toBe('0px')
 expect(getComputedStyle(plus).backgroundColor).toBe('rgba(0, 0, 0, 0)')
 if (import.meta.env.VITE_CHROME_SHOTS_DIR) await page.screenshot({ path: `${import.meta.env.VITE_CHROME_SHOTS_DIR}/channel-heading-${width}.png` })
 const muted = getComputedStyle(plus).color
 await page.getByRole('button',{name:'New channel'}).hover()
 expect(getComputedStyle(plus).color).not.toBe(muted)
 plus.focus()
 expect(plus).toHaveFocus()
 expect(getComputedStyle(plus).outlineStyle).not.toBe('none')
 expect(getComputedStyle(plus).color).not.toBe(muted)
 fireEvent.click(plus)
 expect(screen.getByRole('form',{name:'New channel'})).toBeVisible()
 fireEvent.keyDown(screen.getByLabelText('Channel name'),{key:'Escape'})
 await waitFor(() => expect(plus).toHaveFocus())
 fireEvent.click(screen.getByRole('tab',{name:'Questions'}))
 expect(screen.queryByRole('heading',{name:'Channels'})).toBeNull()
 expect(screen.queryByRole('button',{name:'New channel'})).toBeNull()
 fireEvent.click(screen.getByRole('tab',{name:'Channels'}))
 const group = screen.getByRole('treeitem',{name:/^Workers/})
 expect(group).toHaveAttribute('aria-expanded','true')
 expect(group.querySelector('.blerg-status-spinner')).toBeNull()
 const row = screen.getByRole('treeitem',{name:'worker'})
 const channel = screen.getByRole('treeitem',{name:'Design channel'})
 // This Workers folder mixes runtimes ('worker'/'waiting' are docker, 'other' is cluster),
 // so every row shows its own runtime glyph (BLG-501) — 'worker' is no longer the "same as
 // the group's baseline" row the single-runtime case would hide this for.
 expect(row.querySelector('[data-chat-role], .dock-channel-emoji')).toBeNull()
 expect(row.querySelector('.runtime-icon')).not.toBeNull()
 expect(row).not.toHaveTextContent('🔥')
 expect(row.querySelector('.blerg-status-spinner')).not.toBeNull()
 expect(parseFloat(getComputedStyle(row).fontSize)).toBeLessThan(parseFloat(getComputedStyle(channel).fontSize))
 expect(row.getBoundingClientRect().height).toBeLessThan(channel.getBoundingClientRect().height)
 expect(screen.getByRole('treeitem',{name:'other'}).querySelector('.runtime-icon')).not.toBeNull()
 expect(within(screen.getByRole('treeitem',{name:'waiting'})).getByRole('img',{name:'Needs you'})).toBeVisible()
 fireEvent.click(group)
 expect(group.querySelector('.blerg-status-spinner')).toHaveAttribute('title','2 running')
 expect(group).not.toHaveTextContent('●')
 expect(screen.getByRole('treeitem',{name:'Finished'})).toHaveAttribute('aria-expanded','false')
 expect(screen.queryByText('Finished work')).toBeNull()
 const main = screen.getByRole('treeitem',{name:'Main'})
 expect(main).toHaveTextContent('🔎')
 fireEvent.contextMenu(main)
 fireEvent.click(await screen.findByRole('menuitem',{name:'Change emoji'}))
 fireEvent.change(screen.getByRole('textbox',{name:'Emoji'}),{target:{value:'🧭'}})
 fireEvent.click(screen.getByRole('button',{name:'Save emoji'}))
 await waitFor(() => expect(main).toHaveTextContent('🧭'))
 expect(patches).toEqual([{emoji:'🧭'}])
})
