import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import WorkspacePicker, { type WorkspaceSelection } from './WorkspacePicker'
import '../index.css'

vi.mock('../apiFetch', () => ({ apiFetch: (url: string, init?: RequestInit) => fetch(url, init) }))
const daemon = { id: 'desktop-a', name: 'Desktop', mode: 'local', repos_root: '/repos', status: 'connected', sandbox_available: true, available_engines: ['claude'], discovery_version: 1, ownership_revision: 1, connection_generation: 'connection-a' }
const entry = (path: string, kind = 'repository') => ({ path, name: path === '.' ? 'Repositories' : path.split('/').at(-1), kind, source_identity: `source:${path}` })
const json = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('workspace picker', () => {
  it.each([390, 3840])('shows one checked tree with explicit selection at %ipx', async width => {
    await page.viewport(width, 900)
    const chosen: {current: WorkspaceSelection | null} = {current: null}
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url.startsWith('/api/workspace/discovery?')) {
        const path = new URL(url, location.origin).searchParams.get('path')
        return json({ ok: true, page: { root_identity: 'root-a', root: entry('.', 'directory'), entries: path === '.' ? [entry('group', 'directory'), entry('standalone')] : [entry('group/api'), entry('group/web')], truncated: false } })
      }
      const body = JSON.parse(String(init?.body))
      const members = body.expand ? [entry('group/api'), entry('group/web')] : body.selection.members
      return json({ ok: true, selection: { ...body.selection, members, membership_snapshot: 'snapshot-a' }, members: members.map((m: { path: string }) => ({ name: m.path.split('/').at(-1) })) })
    }))
    render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={s => { chosen.current = s }} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Expand group' }))
    expect(await screen.findByText('api')).toBeTruthy()
    fireEvent.click(screen.getByRole('checkbox', { name: /group.*Folder/ }))
    await waitFor(() => expect(chosen.current?.members).toHaveLength(2))
    expect(chosen.current?.folder?.path).toBe('group')
    expect(screen.getByText('2 / 20')).toBeTruthy()
    expect(screen.getByText(/relative paths are preserved/)).toBeTruthy()
    const box = screen.getByRole('region', { name: 'Workspace repositories' }).getBoundingClientRect()
    expect(box.left).toBeGreaterThanOrEqual(0)
    expect(box.right).toBeLessThanOrEqual(width)
    expect(within(screen.getByRole('group', { name: 'Configured folder tree' })).getAllByRole('checkbox').length).toBe(5)
  })
  it('never silently selects twenty from a larger folder', async () => {
    const change = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (url: string) => url.startsWith('/api/workspace/discovery?')
      ? json({ ok: true, page: { root_identity: 'root', root: entry('.', 'directory'), entries: [entry('large', 'directory')], truncated: false } })
      : json({ ok: false, code: 'too_many_members' }, 409)))
    render(<WorkspacePicker daemon={daemon} runtime="docker" repos={[]} onChange={change} />)
    fireEvent.click(await screen.findByRole('checkbox', { name: /large.*Folder/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('explicit subset')
    expect(change).toHaveBeenLastCalledWith(null, [])
  })
  it('keeps the folder anchor when explicitly selecting within a large folder', async () => {
    const change = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url.startsWith('/api/workspace/discovery?')) {
        const path = new URL(url, location.origin).searchParams.get('path')
        return json({ ok: true, page: { root_identity: 'root', root: entry('.', 'directory'), entries: path === '.' ? [entry('large', 'directory')] : [entry('large/api')], truncated: false } })
      }
      const body = JSON.parse(String(init?.body))
      return body.expand ? json({ ok: false, code: 'too_many_members' }, 409)
        : json({ ok: true, selection: { ...body.selection, membership_snapshot: 'explicit:checked' }, members: [{name: 'api'}] })
    }))
    render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change} />)
    fireEvent.click(await screen.findByRole('checkbox', { name: /large.*Folder/ }))
    await screen.findByRole('alert')
    fireEvent.click(screen.getByRole('button', { name: 'Expand large' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: /api.*Git/ }))
    await waitFor(() => expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ folder: { path: 'large', source_identity: 'source:large' }, members: [{path: 'large/api', source_identity: 'source:large/api'}] }), ['api']))
    expect(screen.getByText(/relative paths are preserved/)).toBeTruthy()
    expect((screen.getByRole('checkbox', { name: /large.*Folder/ }) as HTMLInputElement).indeterminate).toBe(true)
  })
  it.each([390, 3840])('offers searchable hosted multi-select at %ipx without local-only folders', async width => {
    await page.viewport(width, 900)
    render(<WorkspacePicker daemon={null} runtime="cluster" repos={[
      { name: 'api', full_name: 'team/api', provider: 'github', checked_out: [] },
      { name: 'web', full_name: 'team/web', provider: 'gitlab', checked_out: [] },
      { name: 'private-local', full_name: 'private-local', is_local: true, checked_out: [] },
    ]} onChange={() => {}} />)
    fireEvent.click(screen.getByRole('checkbox', { name: /team\/api/ }))
    fireEvent.click(screen.getByRole('checkbox', { name: /team\/web/ }))
    expect(screen.getByText('2 / 20')).toBeTruthy()
    expect(screen.queryByText('private-local')).toBeNull()
    fireEvent.change(screen.getByRole('textbox', { name: 'Search repositories' }), { target: { value: 'team/web' } })
    expect(screen.getAllByRole('checkbox')).toHaveLength(1)
  })
})

it.each([390,3840])('unions sibling folders and mixed repositories, and unchecks only descendants at %ipx',async width=>{
  await page.viewport(width,900)
  const change=vi.fn()
  vi.stubGlobal('fetch',vi.fn(async(url:string,init?:RequestInit)=>{
    if(url.startsWith('/api/workspace/discovery?')) return json({ok:true,page:{root_identity:'root',root:entry('.','directory'),entries:[entry('apps','directory'),entry('services','directory'),entry('standalone')],truncated:false}})
    const body=JSON.parse(String(init?.body));const folder=body.selection.folder?.path
    const members=body.expand ? (folder==='apps'?[entry('apps/atlas'),entry('apps/beacon')]:[entry('services/grove'),entry('services/harbor')]):body.selection.members
    return json({ok:true,selection:{...body.selection,members},members:members.map((m:{path:string})=>({name:m.path.split('/').at(-1)}))})
  }))
  render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change}/>)
  fireEvent.click(await screen.findByRole('checkbox',{name:/standalone.*Git/}))
  await waitFor(()=>expect(change).toHaveBeenLastCalledWith(expect.objectContaining({members:expect.any(Array)}),['standalone']))
  fireEvent.click(screen.getByRole('checkbox',{name:/apps.*Folder/}))
  await screen.findByText('3 / 20')
  fireEvent.click(screen.getByRole('checkbox',{name:/services.*Folder/}))
  await screen.findByText('5 / 20')
  const selected=change.mock.calls.at(-1)?.[0]
  expect(selected.members).toHaveLength(5);expect(selected.folder).toBeUndefined()
  fireEvent.click(screen.getByRole('checkbox',{name:/apps.*Folder/}))
  await screen.findByText('3 / 20')
  expect(change.mock.calls.at(-1)?.[0].members.map((m:{path:string})=>m.path)).toEqual(['standalone','services/grove','services/harbor'])
  const region=screen.getByRole('region',{name:'Workspace repositories'})
  expect(region.querySelector('.workspace-chips')!.compareDocumentPosition(region.querySelector('.workspace-tree')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  const probe=document.createElement('span');probe.style.color='var(--selected-border)';document.body.append(probe)
  expect(getComputedStyle(screen.getByRole('checkbox',{name:/services.*Folder/})).accentColor).toBe(getComputedStyle(probe).color);probe.remove()
})
it('refuses a folder union over twenty without replacing the earlier selection',async()=>{
 const change=vi.fn()
 vi.stubGlobal('fetch',vi.fn(async(url:string,init?:RequestInit)=>{
  if(url.startsWith('/api/workspace/discovery?')) return json({ok:true,page:{root_identity:'root',root:entry('.','directory'),entries:[entry('apps','directory'),entry('services','directory')],truncated:false}})
  const body=JSON.parse(String(init?.body));const folder=body.selection.folder.path
  const members=Array.from({length:folder==='apps'?15:6},(_,i)=>entry(`${folder}/repo${i}`))
  return json({ok:true,selection:{...body.selection,members},members:members.map(m=>({name:m.name}))})
 }))
 render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change}/>)
 fireEvent.click(await screen.findByRole('checkbox',{name:/apps.*Folder/}));await screen.findByText('15 / 20')
 fireEvent.click(screen.getByRole('checkbox',{name:/services.*Folder/}));await screen.findByRole('alert')
 expect(screen.getByText('15 / 20')).toBeTruthy()
 expect(change.mock.calls.at(-1)?.[0].members).toHaveLength(15)
})
