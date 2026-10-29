import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import type { ShellWorkspaceHandle } from './ShellWorkspace'
import { HostShellConsentRequired } from '../shellTransport'
import { apiFetch } from '../apiFetch'
import { FakeShell } from '../test/shellMock'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('./ShellView', () => ({ default: () => <div>Terminal</div> }))
beforeEach(() => localStorage.clear())
const empty = async () => []
it('opens four permanent shell tabs, explains the limit and ends on every close path', async () => {
  const ref = createRef<ShellWorkspaceHandle>(); const shells: FakeShell[] = []
  const createShell = vi.fn(async () => { const shell = new FakeShell(`${shells.length + 1}`); shells.push(shell); return shell })
  render(<ShellWorkspace restoreShells={empty} ref={ref} sessionId="session" accountId="owner" createShell={createShell}>Chat</ShellWorkspace>)
  for (let n = 0; n < 4; n++) await act(async () => { ref.current?.open() })
  expect(screen.getAllByRole('tab').map(t => t.textContent?.trim())).toEqual(['Chat', '›_ Shell', '›_ Shell 2', '›_ Shell 3', '›_ Shell 4'])
  expect(screen.getByRole('button', { name: 'New shell' })).toHaveAttribute('aria-description', expect.stringContaining('four shells'))
  expect(screen.getByRole('button', { name: 'New shell' }).title).toContain('four shells')
  expect(screen.getByRole('tab', { name: 'Shell 2' }).title).toBe('Shell 2 · sandbox · project-2')
  fireEvent.click(screen.getByLabelText('Close Shell 2'))
  await waitFor(() => expect(shells[1].ended).toBe(true))
  fireEvent(screen.getByRole('tab', { name: 'Shell 3' }).parentElement!, new MouseEvent('auxclick', { bubbles: true, button: 1 }))
  expect(shells[2].ended).toBe(true)
  fireEvent.keyDown(window, { altKey: true, key: 'w' })
  expect(shells[3].ended).toBe(true)
  expect(screen.queryByLabelText('Dock shell')).toBeNull()
})
it('waits for restoration before creating so a list cannot race the new attachment', async () => {
  let finish!: (shells: FakeShell[]) => void
  const restore = vi.fn(() => new Promise<FakeShell[]>(resolve => { finish = resolve }))
  const create = vi.fn(async () => new FakeShell('new'))
  const ref = createRef<ShellWorkspaceHandle>()
  render(<ShellWorkspace restoreShells={restore} ref={ref} sessionId="race" accountId="owner" createShell={create}>Chat</ShellWorkspace>)
  await act(async () => { ref.current?.open() })
  expect(create).not.toHaveBeenCalled()
  await act(async () => { finish([new FakeShell('old')]) })
  await waitFor(() => expect(create).toHaveBeenCalledOnce())
  expect(screen.getByRole('tab', { name: 'Shell 2' })).toHaveAttribute('aria-selected', 'true')
})
it('always removes a disconnected shell even when DELETE fails', async () => {
  const shell = new FakeShell('dead'); shell.end = vi.fn().mockRejectedValue(new Error('offline'))
  render(<ShellWorkspace restoreShells={async () => [shell]} sessionId="dead" accountId="owner">Chat</ShellWorkspace>)
  await screen.findByRole('tab', { name: 'Shell' })
  fireEvent.click(screen.getByLabelText('Close Shell'))
  expect(screen.queryByRole('tab', { name: 'Shell' })).toBeNull()
  await waitFor(() => expect(shell.disconnected).toBe(true))
  expect(shell.end).toHaveBeenCalledOnce()
  expect(screen.queryByRole('alertdialog')).toBeNull()
})
it('keeps host consent explicit and launches only after accepting it', async () => {
  const challenge = { challenge_id: 'fresh-challenge', machine_id: 'verified-machine', account: 'actual-user', uid: 1000, shell: '/bin/bash', cwd: '/prepared/project' }
  vi.mocked(apiFetch).mockResolvedValue(new Response('{}', { status: 200 }))
  const create = vi.fn().mockRejectedValueOnce(new HostShellConsentRequired(challenge)).mockResolvedValue(new FakeShell('host'))
  const ref = createRef<ShellWorkspaceHandle>()
  render(<ShellWorkspace ref={ref} sessionId="consent" accountId="owner" createShell={create} restoreShells={empty}><textarea aria-label="Composer" /></ShellWorkspace>)
  await act(async () => { ref.current?.open() })
  expect(screen.getByRole('dialog')).toHaveTextContent('actual-user (UID 1000)')
  expect(screen.getByLabelText('Composer').closest('.shell-chat')).toHaveAttribute('inert')
  fireEvent.click(screen.getByText('Allow this launch'))
  await waitFor(() => expect(create).toHaveBeenCalledTimes(2))
  expect(apiFetch).toHaveBeenCalledWith('/api/sessions/consent/shell-consent/accept', expect.objectContaining({ body: JSON.stringify({challenge_id: 'fresh-challenge', accept: true}) }))
})
it('does not resurrect a closed tab from an inventory response already in flight', async () => {
  const shell = new FakeShell('closed')
  let finish!: (shells: FakeShell[]) => void
  const restore = vi.fn().mockResolvedValueOnce([shell]).mockImplementation(() => new Promise<FakeShell[]>(resolve => { finish = resolve }))
  const ui = (enabled: boolean, capabilityPending: boolean) => <ShellWorkspace sessionId="closing" accountId="owner" enabled={enabled} capabilityPending={capabilityPending} restoreShells={restore}>Chat</ShellWorkspace>
  const view = render(ui(true, false))
  await screen.findByRole('tab', { name: 'Shell' })
  view.rerender(ui(false, true)); view.rerender(ui(true, false))
  await waitFor(() => expect(restore).toHaveBeenCalledTimes(2))
  fireEvent.click(screen.getByLabelText('Close Shell'))
  await act(async () => { finish([shell]) })
  expect(screen.queryByRole('tab', { name: 'Shell' })).toBeNull()
  expect(shell.ended).toBe(true)
})
it('waits for an in-flight launch before refreshing inventory', async () => {
  const shell = new FakeShell('opening')
  let finish!: (shell: FakeShell) => void
  const create = vi.fn(() => new Promise<FakeShell>(resolve => { finish = resolve }))
  const restore = vi.fn().mockResolvedValueOnce([]).mockResolvedValue([shell])
  const ref = createRef<ShellWorkspaceHandle>()
  const ui = (enabled: boolean, capabilityPending: boolean) => <ShellWorkspace ref={ref} sessionId="launch-refresh" accountId="owner" enabled={enabled} capabilityPending={capabilityPending} restoreShells={restore} createShell={create}>Chat</ShellWorkspace>
  const view = render(ui(true, false))
  await act(async () => { ref.current?.open() })
  await waitFor(() => expect(create).toHaveBeenCalledOnce())
  await act(async () => { view.rerender(ui(false, true)) })
  await act(async () => { view.rerender(ui(true, false)) })
  expect(restore).toHaveBeenCalledTimes(1)
  await act(async () => { finish(shell) })
  await waitFor(() => expect(restore).toHaveBeenCalledTimes(2))
  expect(screen.getByRole('tab', { name: 'Shell' })).toHaveAttribute('aria-selected', 'true')
  expect(shell.disconnected).toBe(false)
})

it('H1: retains connections, names and saved split positions after a capability failure', async () => {
  const shells = [new FakeShell('a'), new FakeShell('b')]
  const restore = async () => shells
  const savedTabs = shells.map((p, i) => ({ id: `shell:${p.info.shell_id}`, preview: false, view: { kind: 'shell', shellId: p.info.shell_id, runtime: p.info.runtime, folder: p.info.folder, label: `Shell ${7 + i}` } }))
  localStorage.setItem('blerg-runner.tabs.owner.capability', JSON.stringify({ tabs: savedTabs, active: 'shell:b', focused: 'second', layout: { type: 'split', id: 'split', axis: 'vertical', ratio: .4, first: { type: 'group', id: 'main', tabs: ['chat', 'shell:a'], active: 'shell:a' }, second: { type: 'group', id: 'second', tabs: ['shell:b'], active: 'shell:b' } } }))
  const ui = (enabled: boolean) => <ShellWorkspace sessionId="capability" accountId="owner" enabled={enabled} restoreShells={restore}>Chat</ShellWorkspace>
  const view = render(ui(true))
  await screen.findByRole('tab', { name: 'Shell 8' })
  const before = localStorage.getItem('blerg-runner.tabs.owner.capability')
  view.rerender(ui(false))
  expect(screen.getByRole('tab', { name: 'Shell 8' })).toBeTruthy()
  expect(shells.every(shell => !shell.disconnected)).toBe(true)
  expect(localStorage.getItem('blerg-runner.tabs.owner.capability')).toBe(before)
})
it.each([503, 401, 200])('H2: real restore rejects non-authoritative inventory (%i)', async status => {
  const { restoreShells, ShellRateGroup } = await import('../shellTransport')
  vi.mocked(apiFetch).mockResolvedValue(new Response('{}', { status }))
  await expect(restoreShells('session', new ShellRateGroup())).rejects.toThrow()
})
it('M1: queues a close before inventory knows the handle', async () => {
  const original = new FakeShell('queued')
  const first = render(<ShellWorkspace sessionId="queued" accountId="owner" restoreShells={async () => [original]}>Chat</ShellWorkspace>)
  await screen.findByRole('tab', { name: 'Shell' })
  first.unmount()
  let finish!: (shells: FakeShell[]) => void
  const restored = new FakeShell('queued')
  render(<ShellWorkspace sessionId="queued" accountId="owner" restoreShells={() => new Promise(resolve => { finish = resolve })}>Chat</ShellWorkspace>)
  await waitFor(() => expect(finish).toBeTypeOf('function'))
  fireEvent.click(screen.getByLabelText('Close Shell'))
  await act(async () => { finish([restored]) })
  expect(screen.queryByRole('tab', { name: 'Shell' })).toBeNull()
  expect(restored.ended).toBe(true)
})
it('L2: remembers failed ends across reload until a successful inventory stops listing them', async () => {
  const failed = new FakeShell('failed'); failed.end = vi.fn().mockRejectedValue(new Error('offline'))
  const first = render(<ShellWorkspace sessionId="failed" accountId="owner" restoreShells={async () => [failed]}>Chat</ShellWorkspace>)
  await screen.findByRole('tab', { name: 'Shell' })
  fireEvent.click(screen.getByLabelText('Close Shell'))
  first.unmount()
  const restored = new FakeShell('failed')
  render(<ShellWorkspace sessionId="failed" accountId="owner" restoreShells={async () => [restored]}>Chat</ShellWorkspace>)
  await waitFor(() => expect(restored.ended).toBe(true))
  expect(screen.queryByRole('tab', { name: 'Shell' })).toBeNull()
})
it('L3: activating the four-shell button explains the limit', async () => {
  const shells = ['a','b','c','d'].map(id => new FakeShell(id))
  render(<ShellWorkspace sessionId="limit" accountId="owner" restoreShells={async () => shells}>Chat</ShellWorkspace>)
  await screen.findByRole('tab', { name: 'Shell 4' })
  const button = screen.getByRole('button', { name: 'New shell' })
  expect(button).not.toBeDisabled()
  fireEvent.click(button)
  expect(await screen.findByRole('alertdialog')).toHaveTextContent('four shells')
})
it('H2: a real 503 refresh retains the owned transport and exact saved layout', async () => {
  const { restoreShells } = await import('../shellTransport')
  const shell = new FakeShell('survivor')
  const restore = vi.fn().mockResolvedValueOnce([shell]).mockImplementation(restoreShells)
  const ui = (pending: boolean) => <ShellWorkspace sessionId="http" accountId="owner" enabled={!pending} capabilityPending={pending} restoreShells={restore}>Chat</ShellWorkspace>
  const view = render(ui(false))
  await screen.findByRole('tab', { name: 'Shell' })
  const saved = localStorage.getItem('blerg-runner.tabs.owner.http')
  vi.mocked(apiFetch).mockResolvedValue(new Response('{}', { status: 503 }))
  view.rerender(ui(true)); view.rerender(ui(false))
  await waitFor(() => expect(restore).toHaveBeenCalledTimes(2))
  await act(async () => {})
  expect(screen.getByRole('tab', { name: 'Shell' })).toBeTruthy()
  expect(shell.disconnected).toBe(false)
  expect(localStorage.getItem('blerg-runner.tabs.owner.http')).toBe(saved)
})
it('counts server shells whose end failed, even after their tabs close', async () => {
  const shells = ['a','b','c','d'].map(id => new FakeShell(id))
  shells[3].end = vi.fn().mockRejectedValue(new Error('offline'))
  const create = vi.fn(async () => new FakeShell('fifth'))
  render(<ShellWorkspace sessionId="server-limit" accountId="owner" restoreShells={async () => shells} createShell={create}>Chat</ShellWorkspace>)
  await screen.findByRole('tab', { name: 'Shell 4' })
  fireEvent.click(screen.getByLabelText('Close Shell 4'))
  fireEvent.click(screen.getByRole('button', { name: 'New shell' }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent('four shells')
  expect(create).not.toHaveBeenCalled()
})
