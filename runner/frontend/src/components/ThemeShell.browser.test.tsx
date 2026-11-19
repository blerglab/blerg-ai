import { afterEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import App from '../App'
import { seedRichRail } from '../test/railFixture'
import { useTranscriptStore } from '@blerglab/chat'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({...await original<typeof import('../authClient')>(), getAccessToken: () => 'fixture', redirectToRefresh: vi.fn()}))
vi.mock('../claims', async original => ({...await original<typeof import('../claims')>(), tokenExpired: () => false}))
const request = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({apiFetch: request}))
const originalPath = location.pathname
const json = (value: unknown) => Promise.resolve(new Response(JSON.stringify(value)))
afterEach(() => { history.replaceState(null, '', originalPath); document.documentElement.removeAttribute('data-theme'); localStorage.clear(); vi.unstubAllGlobals() })
it.each([390,3840])('syncs on the real session route and repaints rendered Mermaid and Vega at %i px', async width => {
 await page.viewport(width, 1000)
 const {live} = seedRichRail()
 let theme = 'colorblind-dark'
 request.mockImplementation((url: string) => json(url === '/api/me/theme' ? {theme} : url.endsWith('/live') ? live : url.endsWith('/topics') ? {topics:[]} : url.includes('proposals') ? {proposals:[]} : {}))
 vi.stubGlobal('fetch', vi.fn(() => json([])))
 // mark:'tick' is a firm, permanent v1 exclusion for BLG-452's own renderer (classify.ts's
 // DEFERRED_MARKS) — this test is specifically about Vega's own canvas theme-repaint behavior,
 // so it needs a spec guaranteed to stay on that path even as more marks ship their own renderer.
 const chart = JSON.stringify({data:{values:[{x:'A',y:2},{x:'B',y:3}]},mark:'tick',encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'quantitative'},color:{field:'x',type:'nominal'}}})
 useTranscriptStore.getState().ingest({type:'agent_event',session_id:'sess-a',client_event_id:'theme-visuals',seq:1001,ts:new Date().toISOString(),kind:'assistant_text',payload:{text:'```mermaid\nflowchart LR\n Alpha --> Beta[(Gamma)]\n```\n\n```vega-lite\n'+chart+'\n```',done:true}})
 history.replaceState(null,'','/sessions/sess-a')
 render(<App />)
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe(theme))
 expect(screen.queryByRole('combobox',{name:'Theme'})).toBeNull()
 // Theme and layout effects can replace the rendered surfaces after a successful wait.
 // Read both snapshots in the same callback that checks their readiness.
 const { beforeChart, beforeImage } = await waitFor(() => {
  const image = document.querySelector<HTMLImageElement>('img.visual-diagram')
  const canvas = document.querySelector('canvas')
  expect(image?.naturalWidth).toBeGreaterThan(20)
  expect(canvas).not.toBeNull()
  return { beforeChart: canvas!.toDataURL(), beforeImage: image!.src }
 }, {timeout:30000})
 theme='colorblind-light'
 act(() => fireEvent.focus(window))
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe(theme))
 await waitFor(() => expect(document.querySelector<HTMLImageElement>('img.visual-diagram')!.src).not.toBe(beforeImage),{timeout:30000})
 await waitFor(() => { expect(document.querySelector('canvas')).not.toBeNull(); expect(document.querySelector('canvas')!.toDataURL()).not.toBe(beforeChart) },{timeout:30000})
 expect(readVisualTheme(document.querySelector('.agent-timeline')!).palette.length).toBeGreaterThanOrEqual(8)
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
},60000)
