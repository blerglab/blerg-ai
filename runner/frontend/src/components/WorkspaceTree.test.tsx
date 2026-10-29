import { afterEach, expect, it, vi } from 'vitest'
import { Blob as NodeBlob } from 'node:buffer'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { inputClock } from '../../../../packages/chat/src/model/clock'
import WorkspaceTree from './WorkspaceTree'

const read = vi.hoisted(() => vi.fn())
const fileRead = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: { workspaceFile: fileRead } }) }))
const member = { id: 'member-id', name: 'member', path: 'member' }
const root = [{ name: 'member', path: 'member', directory: true, repository: true, member_id: 'member-id' }, { name: 'README.md', path: 'README.md', directory: false }]
function fixture(searchDirectories = false) {
  read.mockImplementation(async (url: string) => {
    const query = new URL(url, 'https://example.com').searchParams
    const op = query.get('op'), path = query.get('path')
    return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, operation: op, path, entries: op === 'search' ? [root[searchDirectories ? 0 : 1]] : path ? [{ name: 'nested.txt', path: 'member/nested.txt', directory: false }] : root, truncated: false, ...(op === 'status' ? { status: { branch: 'feature/<script>', changed: 2, untracked: 1, partial: true } } : {}) }, members: [member], runtime: 'sandbox' }))
  })
  fileRead.mockResolvedValue({ path: 'README.md', size: 7, view: 'text', content_type: 'text/plain', blob: new NodeBlob(['fixture']) })
}
afterEach(() => { vi.clearAllMocks(); vi.restoreAllMocks() })
it('loads directories lazily, requests only expanded member status, and filters loaded names locally', async () => {
  fixture()
  render(<WorkspaceTree sessionId="session" active />)
  await screen.findByText('README.md')
  expect(document.querySelector('[data-file-icon=markdown]')).toBeInTheDocument()
  expect(document.querySelector('[data-file-icon=folder]')).toBeInTheDocument()
  expect(read).toHaveBeenCalledTimes(1)
  expect(screen.queryByText('nested.txt')).toBeNull()
  expect(screen.getByText('Only selected repositories were copied.')).toBeInTheDocument()
  fireEvent.change(screen.getByRole('combobox', { name: 'Search workspace files' }), { target: { value: 'README' } })
  expect(read).toHaveBeenCalledTimes(1)
  fireEvent.change(screen.getByRole('combobox'), { target: { value: '' } })
  fireEvent.click(screen.getByText('member'))
  await screen.findByText('nested.txt')
  const branch = await screen.findByTitle('Branch feature/<script>')
  expect(branch).toHaveTextContent('⎇ feature/<script>')
  expect(branch.querySelector('[aria-hidden="true"]')).toHaveTextContent('⎇')
  expect(branch).toHaveTextContent('· 3 changed (partial)')
  expect(read).toHaveBeenCalledTimes(3)
  expect(document.querySelector('script')).toBeNull()
})
it('shows a detached HEAD as its short commit with a descriptive tooltip', async () => {
  fixture()
  const base = read.getMockImplementation()!
  read.mockImplementation(async (url: string) => {
    if (url.includes('op=status')) return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, operation: 'status', path: 'member', entries: [], status: { branch: '(detached)', head: 'abcdef0123456789abcdef0123456789abcdef01', changed: 1, untracked: 0 } }, members: [member], runtime: 'sandbox' }))
    return base(url)
  })
  render(<WorkspaceTree sessionId="session" active />)
  fireEvent.click(await screen.findByText('member'))
  const branch = await screen.findByTitle('Detached at abcdef0')
  expect(branch).toHaveTextContent('⎇ abcdef0')
  expect(branch).toHaveTextContent('· 1 changed')
})
it('performs explicit bounded find and opens the existing authenticated file viewer', async () => {
  fixture()
  render(<WorkspaceTree sessionId="session" active />)
  await screen.findByText('README.md')
  fireEvent.change(screen.getByRole('combobox'), { target: { value: 'README' } })
  fireEvent.click(screen.getByRole('button', { name: /Search all files/ }))
  await screen.findByText('Name matches')
  expect(read.mock.calls[1][0]).toContain('op=search')
  fireEvent.click(screen.getByText('README.md'))
  await waitFor(() => expect(fileRead).toHaveBeenCalled())
  expect(fileRead.mock.calls[0].slice(0, 2)).toEqual(['session', 'README.md'])
  expect(screen.getByTestId('workspace-viewer-overlay')).toBeInTheDocument()
  await screen.findByText('fixture')
})
it('cancels navigation and clears the loaded tree when the panel closes', async () => {
  fixture()
  const view = render(<WorkspaceTree sessionId="session" active />)
  await screen.findByText('README.md')
  const signal = read.mock.calls[0][1].signal as AbortSignal
  view.rerender(<WorkspaceTree sessionId="session" active={false} />)
  expect(signal.aborted).toBe(true)
  expect(screen.queryByText('README.md')).toBeNull()
  view.rerender(<WorkspaceTree sessionId="other-session" active />)
  await screen.findByText('README.md')
  expect(read.mock.calls[1][0]).toContain('/other-session/')
})

it('opens a found directory as a browsable subtree and clears the find filter', async () => {
  fixture(true)
  render(<WorkspaceTree sessionId="session" active />)
  await screen.findByText('README.md')
  fireEvent.change(screen.getByRole('combobox'), { target: { value: 'member' } })
  fireEvent.click(screen.getByRole('button', { name: /Search all files/ }))
  await screen.findByText('Name matches')
  fireEvent.click(screen.getByText('member'))
  await screen.findByText('nested.txt')
  expect(screen.getByRole('combobox')).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Workspace' })).toBeInTheDocument()
})

it('refreshes expired status on re-expansion and recovers from unavailable without polling', async () => {
  let now = 1000
  vi.spyOn(inputClock, 'now').mockImplementation(() => now)
  fixture()
  let attempts = 0
  const base = read.getMockImplementation()!
  read.mockImplementation(async (...args: unknown[]) => {
    if (String(args[0]).includes('op=status')) {
      attempts++
      if (attempts === 1) return new Response('', { status: 409 })
    }
    return base(...args)
  })
  render(<WorkspaceTree sessionId="session" active />)
  await screen.findByText('README.md')
  fireEvent.click(screen.getByText('member'))
  await screen.findByText('status unavailable')
  fireEvent.click(screen.getByText('member'))
  now += 4999
  fireEvent.click(screen.getByText('member'))
  expect(attempts).toBe(1)
  fireEvent.click(screen.getByText('member'))
  now += 1
  expect(attempts).toBe(1) // No background polling.
  fireEvent.click(screen.getByText('member'))
  await screen.findByText('feature/<script> · 3 changed (partial)')
  expect(attempts).toBe(2)
  expect(read.mock.calls.filter(([url]) => String(url).includes('op=list'))).toHaveLength(2)
})

it.each([false, true])('refreshes expired cached repository status entered from Find (unavailable=%s)', async unavailable => {
  let now = 1000
  vi.spyOn(inputClock, 'now').mockImplementation(() => now)
  fixture(true)
  const base = read.getMockImplementation()!
  let attempts = 0
  read.mockImplementation(async (...args: unknown[]) => {
    if (String(args[0]).includes('op=status') && ++attempts === 1 && unavailable) return new Response('', { status: 409 })
    return base(...args)
  })
  render(<WorkspaceTree sessionId="session" active />)
  await screen.findByText('README.md')
  fireEvent.click(screen.getByText('member'))
  await screen.findByText(unavailable ? 'status unavailable' : 'feature/<script> · 3 changed (partial)')
  fireEvent.click(screen.getByText('member'))
  now += 6000
  fireEvent.change(screen.getByRole('combobox'), { target: { value: 'member' } })
  fireEvent.click(screen.getByRole('button', { name: /Search all files/ }))
  await screen.findByText('Name matches')
  fireEvent.click(screen.getByText('member'))
  await screen.findByText('nested.txt')
  await waitFor(() => expect(attempts).toBe(2))
  expect(read.mock.calls.filter(([url]) => String(url).includes('op=list'))).toHaveLength(2)
  await screen.findByText('feature/<script> · 3 changed (partial)')
})

it('includes expanded status meanings in file and parent row names', async () => {
  fixture()
  const base = read.getMockImplementation()!
  read.mockImplementation(async (...args: unknown[]) => {
    const reply = await base(...args) as Response
    const body = await reply.json()
    if (String(args[0]).includes('op=status')) body.result.status.files = [{ path: 'nested.txt', code: 'M' }]
    return new Response(JSON.stringify(body))
  })
  render(<WorkspaceTree sessionId="session" active />)
  fireEvent.click(await screen.findByRole('treeitem', { name: 'member' }))
  const file = await screen.findByRole('treeitem', { name: 'nested.txt, modified' })
  file.focus(); expect(file).toHaveAccessibleName('nested.txt, modified')
  expect(screen.getByRole('treeitem', { name: 'member, modified in this folder' })).toHaveAccessibleName('member, modified in this folder')
})
it('associates the combobox with loaded options, navigates without moving input focus and activates a file', async () => {
  fixture()
  render(<WorkspaceTree sessionId="session" active />)
  await screen.findByRole('treeitem', { name: 'README.md' })
  const input = screen.getByRole('combobox', { name: 'Search workspace files' })
  expect(document.getElementById(input.getAttribute('aria-controls')!)).toHaveAttribute('role', 'listbox')
  expect(input).toHaveAttribute('aria-expanded', 'false')
  input.focus(); fireEvent.change(input, { target: { value: 'README' } })
  expect(input).toHaveAttribute('aria-expanded', 'true')
  expect(input).not.toHaveAttribute('aria-activedescendant')
  const option = screen.getByRole('option', { name: 'README.md' })
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  expect(input.getAttribute('aria-activedescendant')).toBe(option.id)
  expect(option).toHaveAttribute('aria-selected', 'true')
  expect(document.activeElement).toBe(input)
  fireEvent.keyDown(input, { key: 'Enter' })
  await screen.findByText('fixture')
  expect(read.mock.calls.some(([url]) => String(url).includes('op=search'))).toBe(false)
  expect(fileRead.mock.calls.at(-1)!.slice(0, 2)).toEqual(['session', 'README.md'])
})

it('shows a specific refusal and retries with refresh progress and a result', async () => {
  read.mockResolvedValueOnce(new Response(JSON.stringify({ code: 'not_running', error: 'The session has no live workspace daemon.' }), { status: 409 }))
  render(<WorkspaceTree sessionId="legacy" active />)
  await screen.findByText('The session has no live workspace daemon.')
  let resolve!: (value: Response) => void
  read.mockImplementationOnce(() => new Promise<Response>(done => { resolve = done }))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh workspace' }))
  await screen.findByText('Refreshing workspace…')
  expect(read).toHaveBeenCalledTimes(2)
  resolve(new Response(JSON.stringify({ result: { ok: true, entries: [], truncated: false, launch_generation: 'legacy', generation: 1 }, members: [], runtime: 'host' })))
  await screen.findByText('Workspace refreshed.')
  expect(screen.queryByText('The session has no live workspace daemon.')).toBeNull()
})
it('waits out a busy explorer instead of showing the refusal', async () => {
  fixture()
  const base = read.getMockImplementation()!
  let refusals = 2
  read.mockImplementation(async (url: string, init?: RequestInit) => refusals-- > 0
    ? new Response(JSON.stringify({ error: 'The workspace explorer is busy.', code: 'busy' }), { status: 409 })
    : base(url, init))
  render(<WorkspaceTree sessionId="session" active />)
  await screen.findByText('README.md', {}, { timeout: 3000 })
  expect(read).toHaveBeenCalledTimes(3)
  expect(screen.queryByText('The workspace explorer is busy.')).toBeNull()
})
it('shows a lasting busy refusal after its retries', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  read.mockImplementation(async () => new Response(JSON.stringify({ error: 'The workspace explorer is busy.', code: 'busy' }), { status: 409 }))
  render(<WorkspaceTree sessionId="session" active />)
  await vi.advanceTimersByTimeAsync(9000)
  await screen.findByText('The workspace explorer is busy.')
  expect(read).toHaveBeenCalledTimes(8)
  vi.useRealTimers()
})

it('keeps listed folders and files fully visible and searchable', async () => {
  read.mockImplementation(async (url: string) => {
    const path = new URL(url, 'https://example.com').searchParams.get('path')
    return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: path ? [{ name: 'package.js', path: 'node_modules/package.js', directory: false, ignored: true }] : [{ name: 'node_modules', path: 'node_modules', directory: true, ignored: true }], truncated: false }, members: [], runtime: 'host' }))
  })
  fileRead.mockResolvedValue({ path: 'node_modules/package.js', size: 7, view: 'text', content_type: 'text/plain', blob: new NodeBlob(['fixture']) })
  render(<WorkspaceTree sessionId="session" active />)
  const folder = await screen.findByRole('treeitem', { name: 'node_modules' })
  expect(folder).not.toHaveClass('workspace-row-ignored')
  fireEvent.click(folder)
  const file = await screen.findByRole('treeitem', { name: 'package.js' })
  expect(file).not.toHaveClass('workspace-row-ignored')
  fireEvent.change(screen.getByRole('combobox'), { target: { value: 'package' } })
  expect(screen.queryAllByRole('option')).toHaveLength(1)
  fireEvent.change(screen.getByRole('combobox'), { target: { value: '' } })
  fireEvent.click(await screen.findByRole('treeitem', { name: 'package.js' }))
  await waitFor(() => expect(fileRead).toHaveBeenCalled())
})
