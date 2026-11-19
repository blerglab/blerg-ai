import DockRepositories from './DockRepositories'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { render, screen, within, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import SessionDockWindows from './SessionDockWindows'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import '../index.css'
import './SessionDockWindows.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => false }))
beforeEach(() => {
  localStorage.clear()
  const members = ['app', 'library', 'tools'].map(name => ({ id: name, name, path: name }))
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', workspace: { members } })] })
  vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ revision: 3, blockers: {} })))
})
afterEach(() => vi.restoreAllMocks())
for (const width of [390, 3840]) it(`persists dismissed failures but never shows denied notices at ${width}px`, async () => {
  await page.viewport(width, 900)
  let last = { id: 'declined-operation', kind: 'add', phase: 'denied', requested_by: 'main', member_id: 'web', member_name: 'web', proposed_digest: 'proposal', refusal: '' }
  let reads = 0
  vi.mocked(fetch).mockImplementation(async () => { reads++; return new Response(JSON.stringify({ revision: 3, blockers: {}, last })) })
  const mount = async () => {
    const view = render(<MemoryRouter><div style={{ display: 'flex', height: 700, width: 'min(300px, 100%)' }}><SessionDockWindows sessionId="main" mobile={width === 390} files={<p>Files view</p>} /></div></MemoryRouter>)
    await userEvent.click(screen.getByRole('tab', { name: 'Repositories' }))
    await screen.findByRole('region', { name: 'Workspace repositories' })
    return view
  }
  const dock = () => within(screen.getByRole('region', { name: 'Workspace repositories' }))
  let view = await mount()
  await waitFor(() => expect(reads).toBeGreaterThan(0))
  expect(dock().queryByRole('status')).toBeNull()
  expect(screen.queryByText(/You declined/)).toBeNull()
  view.unmount()
  last = { ...last, id: 'failed-operation', phase: 'refused', refusal: 'Could not clone this repository. Check your Git access.' }
  view = await mount()
  expect(await dock().findByRole('status')).toHaveTextContent(last.refusal)
  const dismiss = screen.getByRole('button', { name: 'Dismiss repository notice' })
  expect(dismiss).toHaveAttribute('title', 'Dismiss repository notice')
  await userEvent.click(dismiss)
  expect(dock().queryByRole('status')).toBeNull()
  view.unmount()
  const previous = reads
  view = await mount()
  await waitFor(() => expect(reads).toBeGreaterThan(previous))
  expect(dock().queryByRole('status')).toBeNull()
  view.unmount()
  last = { ...last, id: 'new-failed-operation' }
  await mount()
  expect(await dock().findByRole('status')).toHaveTextContent(last.refusal)
})
for (const width of [390, 3840]) it(`shows matching headings and keyboard row actions at ${width}px`, async () => {
  await page.viewport(width, 900)
  const { container } = render(<MemoryRouter><div style={{ display: 'flex', height: 700, width: width === 390 ? '100%' : 300 }}><SessionDockWindows sessionId="main" mobile={width === 390} files={<p>Files view</p>} /></div></MemoryRouter>)
  const channelHeading = screen.getByRole('heading', { name: /channels$/i }).parentElement!
  const channelsClass = channelHeading.className
  const plusClass = channelHeading.querySelector('button')?.className
  if (import.meta.env.VITE_GRAPH_SHOTS_DIR) await page.screenshot({ path: `${import.meta.env.VITE_GRAPH_SHOTS_DIR}/channels-${width}.png` })
  await userEvent.click(screen.getByRole('tab', { name: 'Repositories' }))
  const heading = screen.getByRole('heading', { name: /repositories$/i }).parentElement!
  expect(heading.className).toBe(channelsClass)
  await waitFor(() => expect(heading.querySelector('button')?.className).toBe(plusClass))
  const list = screen.getByRole('list', { name: 'Repositories' })
  expect(within(list).getAllByRole('button').map(button => button.textContent)).toEqual(['app', 'library', 'tools'])
  expect(within(list).queryByRole('img')).toBeNull()
  expect(vi.mocked(fetch).mock.calls.filter(([url]) => /\/workspace\?|\/result$/.test(String(url)))).toHaveLength(0)
  const row = within(list).getByRole('button', { name: 'app' })
  row.focus()
  fireEvent.keyDown(row, { key: 'F10', shiftKey: true })
  expect(await screen.findByRole('menuitem', { name: 'Remove repository' })).toBeVisible()
  fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' })
  expect(row).toHaveFocus()
  expect(container.querySelector('.repository-graph')).toBeNull()
  if (import.meta.env.VITE_GRAPH_SHOTS_DIR) await page.screenshot({ path: `${import.meta.env.VITE_GRAPH_SHOTS_DIR}/repositories-list-${width}.png` })
})
it('stacks all three tools and restores two resize handles', async () => {
  await page.viewport(1024, 900)
  const mount = () => render(<MemoryRouter><div style={{ height: 700, width: 300, display: 'flex' }}><SessionDockWindows sessionId="main" mobile={false} files={<p>Files view</p>} /></div></MemoryRouter>)
  const view = mount()
  const transfer = new DataTransfer()
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Files' }), { dataTransfer: transfer })
  fireEvent.drop(document.querySelector('.session-group')!, { dataTransfer: transfer, clientX: 150, clientY: 690 })
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Repositories' }), { dataTransfer: transfer })
  fireEvent.drop(document.querySelectorAll('.session-group')[1], { dataTransfer: transfer, clientX: 150, clientY: 690 })
  expect(screen.getAllByRole('tabpanel')).toHaveLength(3)
  expect(screen.getAllByRole('separator')).toHaveLength(2)
  const divider = screen.getAllByRole('separator')[1]
  fireEvent.keyDown(divider, { key: 'ArrowDown' })
  const ratio = divider.getAttribute('aria-valuenow')
  view.unmount(); mount()
  expect(screen.getAllByRole('separator')[1]).toHaveAttribute('aria-valuenow', ratio)
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Repositories' }), { dataTransfer: transfer })
  fireEvent.drop(screen.getAllByRole('tablist')[0])
  expect(screen.getAllByRole('separator')).toHaveLength(1)
  expect(screen.getByRole('tabpanel', { name: 'Repositories' })).toBeVisible()
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Files' }), { dataTransfer: transfer })
  fireEvent.drop(screen.getAllByRole('tablist')[0], { dataTransfer: transfer })
  expect(screen.queryByRole('separator')).toBeNull()
})
for (const width of [390, 3840]) it(`names ignored files on the row and confirms before discarding them at ${width}px`, async () => {
  await page.viewport(width, 900)
  const posts: string[] = []
  vi.mocked(fetch).mockImplementation(async (url, init) => {
    const target = String(url)
    if (init?.method === 'POST') { posts.push(String(init.body)); return new Response('{}', { status: 202 }) }
    if (target.endsWith('/workspace/repositories')) return new Response(JSON.stringify({ revision: 3, blockers: {}, last: { id: 'op', kind: 'remove', phase: 'refused', requested_by: 'person', member_id: 'tools', member_name: 'tools', proposed_digest: 'd', refusal: 'Git ignores these local files, so they were never delivered. Removing would delete them.', local_files: ['.env.local', 'a-very-long-cache-folder-name-that-must-wrap-inside-the-dock-column/'], local_count: 5, local_digest: 'files' } }))
    return new Response(JSON.stringify({ result: { ok: true, status: { branch: 'main', changed: 0, untracked: 0, partial: false } } }))
  })
  useSessionStore.setState({ sessions: useSessionStore.getState().sessions.map(s => s.id === 'main' ? { ...s, workspace_results: [] } : s) })
  render(<MemoryRouter><div data-testid="dock" style={{ display: 'flex', height: 700, width: width === 390 ? '100%' : 300, minWidth: 0 }}><DockRepositories sessionId="main" active focusedMember="tools" /></div></MemoryRouter>)
  const files = await screen.findByRole('group', { name: 'Local files in tools' })
  expect(within(files).getByText('.env.local')).toBeVisible()
  expect(within(files).getByText('and 3 more')).toBeVisible()
  expect(screen.getByRole('listitem', { name: /^tools,/ })).toContainElement(files)
  const dock = screen.getByTestId('dock').getBoundingClientRect()
  expect(files.getBoundingClientRect().right).toBeLessThanOrEqual(dock.right + 1)

  await userEvent.click(within(files).getByRole('button', { name: 'Discard files…' }))
  expect(posts).toHaveLength(0)
  await userEvent.click(within(files).getByRole('button', { name: 'Keep files' }))
  expect(posts).toHaveLength(0)
  await userEvent.click(within(files).getByRole('button', { name: 'Discard files…' }))
  await userEvent.click(within(files).getByRole('button', { name: 'Delete files and remove' }))
  await waitFor(() => expect(posts).toHaveLength(1))
  expect(JSON.parse(posts[0])).toMatchObject({ kind: 'remove', member_id: 'tools', discard_local: 'files', expected_revision: 3 })
})

it('prefers the project the workspace recorded', async () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', workspace: { members: [{ id: 'app', name: 'app', path: '.', project: 'acme/app' }] } })] })
  render(<MemoryRouter><DockRepositories sessionId="main" active onOpen={() => undefined} /></MemoryRouter>)
  expect(await screen.findByRole('button', { name: 'acme/app' })).toBeVisible()
})
it('leaves a local folder bare and heads the list with the workspace', async () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Launch', workspace: { members: [{ id: 'tools', name: 'tools', path: 'tools' }] } })] })
  render(<MemoryRouter><DockRepositories sessionId="main" active onOpen={() => undefined} /></MemoryRouter>)
  expect(await screen.findByRole('heading', { name: 'Launch repositories' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'tools' })).toBeVisible()
})

for(const width of [390,3840])it(`offers workspace-scoped shell cleanup while the keeper is unavailable at ${width}px`,async()=>{
 await page.viewport(width,width===390?844:2160)
 const posts:string[]=[]
 vi.mocked(fetch).mockImplementation(async(url,init)=>{
  const target=String(url)
  if(target.endsWith('/workspace/repositories'))return new Response(JSON.stringify({revision:3,blockers:{},operation:{id:'pending',kind:'add',phase:'authorized',requested_by:'person',member_id:'new',member_name:'api',proposed_digest:'next',waiting_code:'keeper',waiting:'Waiting for the shell keeper to reconnect. Blerg will retry this repository change.'}}))
  if(target.includes('/shell-consents'))return new Response(JSON.stringify({consents:[{id:'this-workspace'}]}))
  if(init?.method==='POST'){posts.push(target);return new Response(JSON.stringify({withdrawn:true,cleanup_pending:true}))}
  return new Response('{}')
 })
 render(<MemoryRouter><div data-testid="keeper-dock" style={{width:width===390?'100%':320,minWidth:0}}><DockRepositories sessionId="main" active /></div></MemoryRouter>)
 const button=await screen.findByRole('button',{name:'End host shells and continue'})
 expect(screen.queryByText(/Waiting for the open shell/)).toBeNull()
 const dock=screen.getByTestId('keeper-dock').getBoundingClientRect()
 expect(button.getBoundingClientRect().width).toBeLessThan(dock.width)
 const shots=import.meta.env.VITE_KEEPER_SHOTS_DIR
 if(shots)await page.screenshot({path:`${shots}/membership-keeper-action-${width}.png`})
 await userEvent.click(button)
 expect(posts).toEqual([])
 await userEvent.click(await screen.findByRole('button',{name:'End host shells'}))
 expect(await screen.findByText('Host shell consent withdrawn. The repository change will continue after the keeper confirms cleanup.')).toBeVisible()
 expect(posts).toEqual(['/api/sessions/main/shell-consent/withdraw'])
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
 if(shots)await page.screenshot({path:`${shots}/membership-keeper-${width}.png`})
})
