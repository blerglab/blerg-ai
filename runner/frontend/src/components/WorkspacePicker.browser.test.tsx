import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { FileIcon } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import WorkspacePicker, { type WorkspaceSelection } from './WorkspacePicker'
import '../index.css'

const identity = vi.hoisted(() => ({ account: 'person-a' }))
vi.mock('../lib/launchCache', () => ({ currentAccount: () => identity.account }))
vi.mock('../apiFetch', () => ({ apiFetch: (url: string, init?: RequestInit) => fetch(url, init) }))
const daemon = { id: 'desktop-a', name: 'Desktop', mode: 'local', repos_root: '/repos', status: 'connected', sandbox_available: true, available_engines: ['claude'], discovery_version: 1, ownership_revision: 1, connection_generation: 'connection-a' }
const entry = (path: string, kind = 'repository') => ({ path, name: path === '.' ? 'Repositories' : path.split('/').at(-1), kind, source_identity: `source:${path}` })
const json = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
afterEach(() => { localStorage.clear(); identity.account = 'person-a'; cleanup(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })

describe('workspace picker', () => {
  for (const width of [390, 3840]) for (const theme of ['light', 'dark']) {
    it(`preserves shared named-folder colors at ${width}px in ${theme}`, async () => {
      await page.viewport(width, 900)
      document.documentElement.setAttribute('data-theme', theme)
      vi.stubGlobal('fetch', vi.fn(async () => json({ ok: true, page: {
        root_identity: 'root', root: entry('.', 'directory'),
        entries: [entry('src', 'directory'), entry('.github', 'directory'), entry('standalone')], truncated: false,
      } })))
      const { container } = render(<>
        <WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={() => {}} />
        <div data-testid="shared-folder-colors"><FileIcon name="src" directory /><FileIcon name=".github" directory /></div>
        <span data-testid="muted-color" style={{ color: 'var(--fog)' }} />
      </>)
      const folderIcon = async (name: string) => {
        const checkbox = await screen.findByRole('checkbox', { name: `${name} Folder` })
        return checkbox.closest('label')!.querySelector('svg')!
      }
      const src = await folderIcon('src')
      const github = await folderIcon('.github')
      const shared = screen.getByTestId('shared-folder-colors').querySelectorAll('svg')
      expect(getComputedStyle(src).color).toBe(getComputedStyle(shared[0]).color)
      expect(getComputedStyle(github).color).toBe(getComputedStyle(shared[1]).color)
      expect(getComputedStyle(src).color).not.toBe(getComputedStyle(github).color)
      const muted = getComputedStyle(screen.getByTestId('muted-color')).color
      for (const icon of [src, github]) {
        expect(getComputedStyle(icon).color).not.toBe(muted)
        const box = icon.getBoundingClientRect()
        expect(box.width).toBe(16)
        expect(box.height).toBe(16)
        expect(box.left).toBeGreaterThanOrEqual(0)
        expect(box.right).toBeLessThanOrEqual(width)
      }
      const repo = screen.getByRole('checkbox', { name: 'standalone Git repository' }).closest('label')!.querySelector('svg')!
      expect(getComputedStyle(repo).color).toBe(muted)
      const picker = container.querySelector('.workspace-picker')!.getBoundingClientRect()
      expect(picker.left).toBeGreaterThanOrEqual(0)
      expect(picker.right).toBeLessThanOrEqual(width)
    })
  }
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
    expect(screen.queryByRole('checkbox', { name: 'Keep folder structure' })).toBeNull()
    const box = screen.getByRole('region', { name: 'Workspace repositories' }).getBoundingClientRect()
    expect(box.left).toBeGreaterThanOrEqual(0)
    expect(box.right).toBeLessThanOrEqual(width)
    expect(within(screen.getByRole('tree', { name: 'Configured folder tree' })).getAllByRole('checkbox').length).toBe(5)
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

// #242: a host folder with no Git repository resolves to zero members. The server sent
// them as null and the picker threw "The selection could not be checked."
for (const members of [[], null] as const) for (const width of [390, 3840]) {
  it(`selects a host folder without repositories (members ${JSON.stringify(members)}) at ${width}px`, async () => {
    await page.viewport(width, 900)
    const change = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url.startsWith('/api/workspace/discovery?')) return json({ ok: true, page: { root_identity: 'root', root: entry('.', 'directory'), entries: [entry('notes', 'directory'), entry('standalone')], truncated: false } })
      const body = JSON.parse(String(init?.body))
      return json({ ok: true, selection: { ...body.selection, members, membership_snapshot: 'snapshot-empty' }, members: members === null ? null : [] })
    }))
    render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change} />)
    const folder = await screen.findByRole('checkbox', { name: /notes.*Folder/ })
    fireEvent.click(folder)
    await waitFor(() => expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ folder: { path: 'notes', source_identity: 'source:notes' }, members: [] }), []))
    expect(screen.queryByRole('alert')).toBeNull()
    const note = screen.getByRole('note', { name: 'Folder without repositories' })
    expect(note).toHaveTextContent('notes has no Git repositories. The session starts in it as a plain folder, on This machine only.')
    expect((folder as HTMLInputElement).checked).toBe(true)
    expect(screen.getByText('0 / 20')).toBeTruthy()
    const box = note.getBoundingClientRect()
    expect(box.left).toBeGreaterThanOrEqual(0)
    expect(box.right).toBeLessThanOrEqual(width)
    // Unchecking clears it like any folder.
    fireEvent.click(folder)
    await waitFor(() => expect(change).toHaveBeenLastCalledWith(null, []))
    expect(screen.queryByRole('note', { name: 'Folder without repositories' })).toBeNull()
  })
}

it('adds nothing for an empty folder next to repositories already selected', async () => {
  const change = vi.fn()
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url.startsWith('/api/workspace/discovery?')) return json({ ok: true, page: { root_identity: 'root', root: entry('.', 'directory'), entries: [entry('notes', 'directory'), entry('standalone')], truncated: false } })
    const body = JSON.parse(String(init?.body))
    if (body.expand) return json({ ok: true, selection: { ...body.selection, members: [] }, members: [] })
    return json({ ok: true, selection: { ...body.selection, members: body.selection.members }, members: body.selection.members.map((m: { path: string }) => ({ name: m.path })) })
  }))
  render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change} />)
  fireEvent.click(await screen.findByRole('checkbox', { name: /standalone.*Git/ }))
  await waitFor(() => expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ members: [entry('standalone')].map(e => ({ path: e.path, source_identity: e.source_identity })) }), ['standalone']))
  fireEvent.click(screen.getByRole('checkbox', { name: /notes.*Folder/ }))
  expect(await screen.findByRole('alert')).toHaveTextContent('notes has no Git repositories, so nothing was added. To start in an empty folder, select it on its own.')
  expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ members: [{ path: 'standalone', source_identity: 'source:standalone' }] }), ['standalone'])
})

it('explains a sandbox folder without repositories', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url.startsWith('/api/workspace/discovery?')
    ? json({ ok: true, page: { root_identity: 'root', root: entry('.', 'directory'), entries: [entry('notes', 'directory')], truncated: false } })
    : json({ ok: false, code: 'invalid_selection' }, 409)))
  render(<WorkspacePicker daemon={daemon} runtime="docker" repos={[]} onChange={() => {}} />)
  fireEvent.click(await screen.findByRole('checkbox', { name: /notes.*Folder/ }))
  expect(await screen.findByRole('alert')).toHaveTextContent('A sandbox needs at least one repository; empty folders run on This machine.')
})

function layoutPreview(fallback = false) {
  const requests: WorkspaceSelection[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url.startsWith('/api/workspace/discovery?')) return json({ ok: true, page: {
      root_identity: 'root', root: entry('.', 'directory'),
      entries: [entry('repositories/ai-playground'), entry('dev/ll/api')], truncated: false,
    } })
    const body = JSON.parse(String(init?.body)) as { selection: WorkspaceSelection }
    requests.push(body.selection)
    return json({ ok: true, selection: body.selection, members: body.selection.members.map(m => ({ name: m.path.split('/').at(-1) })), member_paths: body.selection.layout === 'structure' ? ['repositories/ai-playground', 'dev/ll/api'] : body.selection.members.map(m => m.path.split('/').at(-1)), structure_fallback: fallback && body.selection.layout === 'structure' })
  }))
  return requests
}
async function selectLayoutRepos() {
  fireEvent.click(await screen.findByRole('checkbox', { name: 'ai-playground Git repository' }))
  await waitFor(() => expect(screen.getByRole('checkbox', { name: 'api Git repository' }).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('checkbox', { name: 'api Git repository' }))
  await waitFor(() => expect(screen.getByRole('checkbox', { name: 'Keep folder structure' }).hasAttribute('disabled')).toBe(false))
}
it.each([390, 3840])('offers layout only for separate multi-repo picks and sends the choice at %ipx', async width => {
  await page.viewport(width, 900)
  const requests = layoutPreview()
  const change = vi.fn()
  render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change} />)
  expect(screen.queryByRole('checkbox', { name: 'Keep folder structure' })).toBeNull()
  fireEvent.click(await screen.findByRole('checkbox', { name: 'ai-playground Git repository' }))
  await waitFor(() => expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ members: [expect.objectContaining({ path: 'repositories/ai-playground' })] }), ['ai-playground']))
  expect(screen.queryByRole('checkbox', { name: 'Keep folder structure' })).toBeNull()
  fireEvent.click(screen.getByRole('checkbox', { name: 'api Git repository' }))
  const option = await screen.findByRole('checkbox', { name: 'Keep folder structure' }) as HTMLInputElement
  await waitFor(() => expect(option.disabled).toBe(false))
  expect(option.checked).toBe(false)
  expect(requests.at(-1)?.layout).toBe('names')
  expect(screen.getByText('Place repositories at their paths under the folder they share, instead of side by side by name.')).toBeTruthy()
  fireEvent.click(option)
  await waitFor(() => expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ layout: 'structure' }), ['ai-playground', 'api']))
  expect(requests.at(-1)?.layout).toBe('structure')
  expect(screen.getByText(/Repositories appear at their paths under their deepest shared folder/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Remove api' }))
  await waitFor(() => expect(screen.queryByRole('checkbox', { name: 'Keep folder structure' })).toBeNull())
  expect(requests.at(-1)?.layout).toBeUndefined()
})
it('remembers layout across a picker reload for the same account only', async () => {
  layoutPreview()
  let view = render(<WorkspacePicker daemon={daemon} runtime="docker" repos={[]} onChange={() => {}} />)
  await selectLayoutRepos()
  fireEvent.click(screen.getByRole('checkbox', { name: 'Keep folder structure' }))
  await waitFor(() => expect(localStorage.getItem('blerg.launch.workspace-layout.person-a')).toBe('structure'))
  await waitFor(() => expect(screen.getByRole('checkbox', { name: 'Keep folder structure' }).hasAttribute('disabled')).toBe(false))
  view.unmount()
  view = render(<WorkspacePicker daemon={daemon} runtime="docker" repos={[]} onChange={() => {}} />)
  await selectLayoutRepos()
  expect((screen.getByRole('checkbox', { name: 'Keep folder structure' }) as HTMLInputElement).checked).toBe(true)
  view.unmount()
  identity.account = 'person-b'
  render(<WorkspacePicker daemon={daemon} runtime="docker" repos={[]} onChange={() => {}} />)
  await selectLayoutRepos()
  expect((screen.getByRole('checkbox', { name: 'Keep folder structure' }) as HTMLInputElement).checked).toBe(false)
})
it('explains whole-selection fallback to names when structure is unsafe', async () => {
  layoutPreview(true)
  render(<WorkspacePicker daemon={daemon} runtime="docker" repos={[]} onChange={() => {}} />)
  await selectLayoutRepos()
  fireEvent.click(screen.getByRole('checkbox', { name: 'Keep folder structure' }))
  expect(await screen.findByRole('status')).toBeTruthy()
  await waitFor(() => expect(screen.getByText(/Folder structure is unavailable because a path component cannot be shown safely/)).toBeTruthy())
  expect(screen.getByText(/Repositories appear at the workspace root under their names/)).toBeTruthy()
  expect((screen.getByRole('checkbox', { name: 'Keep folder structure' }) as HTMLInputElement).checked).toBe(true)
})

it('review: old daemon preview gives the reinstall instruction without retrying', async () => {
  layoutPreview()
  const original = fetch
  const requests: WorkspaceSelection[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (!url.startsWith('/api/workspace/discovery?')) {
      const body = JSON.parse(String(init?.body)) as { selection: WorkspaceSelection }
      requests.push(body.selection)
      if (body.selection.members.length > 1) return json({ ok: false, code: 'desktop_update_required' }, 409)
    }
    return original(url, init)
  }))
  render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={() => {}} />)
  fireEvent.click(await screen.findByRole('checkbox', { name: 'ai-playground Git repository' }))
  await waitFor(() => expect(screen.getByRole('checkbox', { name: 'api Git repository' }).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('checkbox', { name: 'api Git repository' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Update Blerg on this desktop to use several repositories (reinstall its daemon).')
  expect(requests).toHaveLength(2)
})
it('review: a failed chip removal keeps the remainder visible and unvalidated until rechecked', async () => {
  layoutPreview()
  const original = fetch
  let fail = false
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (fail && !url.startsWith('/api/workspace/discovery?')) throw new Error('temporary network failure')
    return original(url, init)
  }))
  const change = vi.fn()
  render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change} />)
  await selectLayoutRepos()
  fail = true
  fireEvent.click(screen.getByRole('button', { name: 'Remove api' }))
  await screen.findByRole('alert')
  expect(screen.queryByRole('button', { name: 'Remove api' })).toBeNull()
  expect(screen.getByRole('button', { name: 'Remove ai-playground' })).toBeTruthy()
  expect(change).toHaveBeenLastCalledWith(null, [])
  expect(screen.getByText(/selection needs to be checked before launch/i)).toBeTruthy()
  fail = false
  fireEvent.click(screen.getByRole('button', { name: 'Recheck selection' }))
  await waitFor(() => expect(change).toHaveBeenLastCalledWith(expect.objectContaining({ members: [expect.objectContaining({ path: 'repositories/ai-playground' })] }), ['ai-playground']))
})
it('review: the shell anchor note uses the projected path', async () => {
  layoutPreview()
  render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={() => {}} />)
  await selectLayoutRepos()
  const note = screen.getByRole('note', { name: 'Workspace launch confirmation' })
  expect(note.textContent).toContain('(ai-playground)')
  expect(note.textContent).not.toContain('(repositories/ai-playground)')
})
it('review: a mounted picker rereads the preference when the account changes', async () => {
  localStorage.setItem('blerg.launch.workspace-layout.person-b', 'structure')
  layoutPreview()
  const view = render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={() => {}} />)
  await selectLayoutRepos()
  expect((screen.getByRole('checkbox', { name: 'Keep folder structure' }) as HTMLInputElement).checked).toBe(false)
  identity.account = 'person-b'
  view.rerender(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={() => {}} />)
  expect((screen.getByRole('checkbox', { name: 'Keep folder structure' }) as HTMLInputElement).checked).toBe(true)
  identity.account = 'person-a'
  view.rerender(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={() => {}} />)
  expect((screen.getByRole('checkbox', { name: 'Keep folder structure' }) as HTMLInputElement).checked).toBe(false)
})

it('review: an old daemon layout recheck preserves local choices and asks for an update', async () => {
  layoutPreview()
  const original = fetch
  let old = false
  const change = vi.fn()
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (old && !url.startsWith('/api/workspace/discovery?')) return json({ ok: false, code: 'desktop_update_required' }, 409)
    return original(url, init)
  }))
  render(<WorkspacePicker daemon={daemon} runtime="daemon" repos={[]} onChange={change} />)
  await selectLayoutRepos()
  old = true
  fireEvent.click(screen.getByRole('checkbox', { name: 'Keep folder structure' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Update Blerg on this desktop to use several repositories (reinstall its daemon).')
  expect(screen.getByRole('button', { name: 'Remove ai-playground' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Remove api' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Recheck selection' })).toBeNull()
  expect(change).toHaveBeenLastCalledWith(null, [])
})
