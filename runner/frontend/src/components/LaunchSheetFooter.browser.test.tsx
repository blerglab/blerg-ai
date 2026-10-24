// The sticky Launch footer shares the three-column grid on desktop: the
// acknowledgement and the button sit under the Run column at its width, not
// across the whole sheet. Real Chromium, because only real layout resolves
// the grid tracks. Screenshots for review go to $FOOTER_SHOTS_DIR when set.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  redirectToRefresh: vi.fn(),
}))

import '../index.css'
import LaunchSheet from './LaunchSheet'

const json = (body: unknown) =>
  Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

function okFetch(url: string) {
  if (url === '/api/repos') return json({ repos: [{ name: 'myrepo', full_name: 'org/myrepo', checked_out: ['ws'] }] })
  if (url.startsWith('/api/runner/launch-options?')) return json({ state: 'known', engines: [{ id: 'claude', name: 'Claude', restricted: true }] })
  if (url === '/api/daemons') return json([{ id: 'd1', name: 'ws', mode: 'local', repos_root: '/r', status: 'connected', sandbox_available: true, available_engines: ['claude'] }])
  if (url === '/api/cluster/status') return json({ configured: false })
  if (url === '/api/me/credentials') return json({ unavailable: true })
  return json({})
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

async function open(width: number, height: number) {
  vi.stubGlobal('fetch', vi.fn((url: string) => okFetch(url)))
  await page.viewport(width, height)
  await waitFor(() => expect(window.innerWidth).toBe(width))
  render(<MemoryRouter><LaunchSheet open onClose={() => {}} /></MemoryRouter>)
  await waitFor(() => expect(screen.getByTestId('launch-footer')).toBeTruthy())
  fireEvent.click(await screen.findByTestId('runtime-daemon'))
  await screen.findByTestId('agent-ack')
}

const rect = (el: Element) => el.getBoundingClientRect()

describe('LaunchSheet footer layout', () => {
  it.each([[1280, 900, 'footer-1280'], [1280, 700, 'footer-short']])('at %ix%i the footer content sits under the Run column', async (w, h, shot) => {
    await open(w, h)
    const run = rect(screen.getByTestId('launch-col-run'))
    const action = rect(screen.getByTestId('launch-footer-action'))
    const button = rect(screen.getByText(/Launch/).closest('button')!)
    expect(Math.abs(action.left - run.left)).toBeLessThan(1)
    expect(Math.abs(action.width - run.width)).toBeLessThan(1)
    expect(Math.abs(button.width - run.width)).toBeLessThan(1)
    // Sticky: the footer is inside the viewport, and the ack is above the button.
    const footer = rect(screen.getByTestId('launch-footer'))
    expect(footer.bottom).toBeLessThanOrEqual(h)
    expect(rect(screen.getByTestId('agent-ack')).bottom).toBeLessThanOrEqual(button.top)
    const dir = (import.meta as unknown as { env: Record<string, string> }).env.VITE_FOOTER_SHOTS_DIR
    if (dir) await page.screenshot({ path: `${dir}/${shot}.png` })
  })

  it('at 800 wide the footer content is full width', async () => {
    await open(800, 900)
    const footer = rect(screen.getByTestId('launch-footer'))
    const button = rect(screen.getByText(/Launch/).closest('button')!)
    expect(button.width).toBeGreaterThan(footer.width - 60)
  })
})

it.each([390,3840])('uses the approved single repository surface in the full sheet at %ipx',async width=>{
  const {clearLaunchCache}=await import('../lib/launchCache');clearLaunchCache()
  vi.stubGlobal('fetch',vi.fn((url:string,init?:RequestInit)=>{
    if(url==='/api/daemons')return json([{id:'d1',name:'Desktop',mode:'local',repos_root:'/repos',status:'connected',sandbox_available:true,available_engines:['claude'],discovery_version:1,ownership_revision:1,connection_generation:'current',workspace_capabilities:{manifest_version:2}}])
    if(url.startsWith('/api/workspace/discovery?'))return json({ok:true,page:{root_identity:'root',root:{path:'.',name:'Repositories',kind:'directory',source_identity:'root'},entries:[{path:'apps',name:'apps',kind:'directory',source_identity:'apps'}],truncated:false}})
    if(url==='/api/workspace/preview'){
      const body=JSON.parse(String(init?.body));const members=[{path:'apps/atlas',source_identity:'atlas'},{path:'apps/beacon',source_identity:'beacon'}]
      return json({ok:true,selection:{...body.selection,members},members:[{name:'atlas'},{name:'beacon'}]})
    }
    return okFetch(url)
  }))
  await page.viewport(width,900)
  render(<MemoryRouter><LaunchSheet open onClose={()=>{}}/></MemoryRouter>)
  fireEvent.click(await screen.findByTestId('runtime-daemon'))
  fireEvent.click(await screen.findByRole('checkbox',{name:/apps.*Folder/}))
  await screen.findByText('2 / 20')
  expect(screen.getAllByText('No repository · empty workspace')).toHaveLength(1)
  expect(screen.queryByTestId('no-repo-option')).toBeNull()
  const picker=screen.getByRole('region',{name:'Workspace repositories'})
  expect(getComputedStyle(picker).borderTopWidth).toBe('0px')
  expect(getComputedStyle(picker).paddingLeft).toBe('0px')
  expect(picker.querySelector('.workspace-chips')!.compareDocumentPosition(picker.querySelector('.workspace-tree')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(screen.getByPlaceholderText('Search repos, or owner/name or a URL...').closest('[hidden]')).not.toBeNull()
  expect(picker.getBoundingClientRect().right).toBeLessThanOrEqual(width)
})
