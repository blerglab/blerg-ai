import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import ShellWorkspace, { type ShellWorkspaceHandle } from './ShellWorkspace'
import { HostShellConsentRequired } from '../shellTransport'
import { apiFetch } from '../apiFetch'
import { FakeShell } from '../test/shellMock'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('./ShellView', () => ({ default: () => <div>Terminal</div> }))
beforeEach(() => localStorage.clear())
it('owns four independent shells, closes panes, remembers docking per person, and collapses', async () => {
  const ref = createRef<ShellWorkspaceHandle>(); const shells: FakeShell[] = []
  const createShell = vi.fn(async () => { const shell = new FakeShell(`${shells.length + 1}`); shells.push(shell); return shell })
  render(<ShellWorkspace restoreShells={async () => []} ref={ref} sessionId="session" accountId="owner" createShell={createShell}><div>Chat</div></ShellWorkspace>)
  await act(async () => { ref.current?.open() })
  fireEvent.change(screen.getByLabelText('Dock shell'), { target: { value: 'right' } })
  expect(localStorage.getItem('shell-dock:owner')).toBe('right')
  expect(localStorage.getItem('shell-dock:other')).toBeNull()
  for (let n = 0; n < 3; n++) { fireEvent.click(screen.getByLabelText('Split right')); await waitFor(() => expect(shells).toHaveLength(n + 2)) }
  expect(screen.getByLabelText('Split right')).toBeDisabled()
  fireEvent.click(screen.getByLabelText('End shell project-2'))
  await waitFor(() => expect(shells[1].ended).toBe(true))
  await waitFor(() => expect(screen.queryByLabelText('End shell project-2')).toBeNull())
  fireEvent.keyDown(window, { ctrlKey: true, code: 'Backquote' })
  expect(screen.getByLabelText('Expand shell')).toBeInTheDocument()
  expect(shells[0].ended).toBe(false)
})
it('shows failures without creating a terminal or saving bytes', async () => {
  const ref = createRef<ShellWorkspaceHandle>()
  render(<ShellWorkspace restoreShells={async () => []} ref={ref} sessionId="session" accountId="owner" createShell={async () => { throw new Error('Shell unavailable.') }}><div>Chat</div></ShellWorkspace>)
  await act(async () => { ref.current?.open() })
  expect(screen.getByRole('alert')).toHaveTextContent('Shell unavailable.')
  expect(screen.queryByLabelText('Session shells')).toBeNull()
  expect(localStorage.length).toBe(0)
})
it('keeps pop-out controlled by an explicit gesture and restores the chat dock', async () => {
  const ref = createRef<ShellWorkspaceHandle>(); const popupDocument = document.implementation.createHTMLDocument('Shell')
  let onClose: (() => void) | undefined
  const close = vi.fn()
  const popup = { document: popupDocument, close, removeEventListener: vi.fn(), addEventListener: (name: string, cb: () => void) => { if (name === 'beforeunload') onClose = cb } }
  const open = vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window)
  const result = render(<ShellWorkspace restoreShells={async () => []} ref={ref} sessionId="session" accountId="owner" createShell={async () => new FakeShell('1')}><div>Chat</div></ShellWorkspace>)
  await act(async () => { ref.current?.open() })
  expect(open).not.toHaveBeenCalled()
  fireEvent.change(screen.getByLabelText('Dock shell'), { target: { value: 'pop-out' } })
  expect(open).toHaveBeenCalledTimes(1)
  expect(popupDocument.querySelector('.shell-dock')).not.toBeNull()
  expect(screen.getByText('Chat')).toBeInTheDocument()
  act(() => { onClose?.() })
  expect(screen.getByLabelText('Dock shell')).toHaveValue('bottom')
  result.unmount(); open.mockRestore()
})
it('restores the current login’s panes so another tab can explicitly take over', async () => {
  const restored = new FakeShell('restored'); const create = vi.fn(async () => new FakeShell('new'))
  render(<ShellWorkspace sessionId="session" accountId="owner" createShell={create} restoreShells={async () => [restored]}><div>Chat</div></ShellWorkspace>)
  await waitFor(() => expect(screen.getByLabelText('End shell project-restored')).toBeInTheDocument())
  expect(create).not.toHaveBeenCalled()
})

it('requires explicit consent naming the authenticated OS identity before host open', async () => {
  const challenge = { challenge_id: 'fresh-challenge', machine_id: 'verified-machine', account: 'actual-user', uid: 1000, shell: '/bin/bash', cwd: '/prepared/project' }
  vi.mocked(apiFetch).mockResolvedValue(new Response('{}', { status: 200 }))
  const create = vi.fn().mockRejectedValueOnce(new HostShellConsentRequired(challenge)).mockResolvedValue(new FakeShell('host'))
  const ref = createRef<ShellWorkspaceHandle>()
  render(<ShellWorkspace ref={ref} sessionId="session" accountId="owner" createShell={create} restoreShells={async () => []}><textarea aria-label="Composer" /></ShellWorkspace>)
  await act(async () => { ref.current?.open() })
  const dialog = screen.getByRole('dialog')
  expect(dialog).toHaveTextContent('verified-machine'); expect(dialog).toHaveTextContent('actual-user (UID 1000)'); expect(dialog).toHaveTextContent('/bin/bash'); expect(dialog).toHaveTextContent('/prepared/project')
  expect(create).toHaveBeenCalledTimes(1)
  expect(screen.getByLabelText('Composer').closest('.shell-chat')).toHaveAttribute('inert')
  fireEvent.click(screen.getByText('Allow this launch'))
  await waitFor(() => expect(create).toHaveBeenCalledTimes(2))
  expect(apiFetch).toHaveBeenCalledWith('/api/sessions/session/shell-consent/accept', expect.objectContaining({ body: JSON.stringify({challenge_id: 'fresh-challenge', accept: true}) }))
  expect(screen.queryByRole('dialog')).toBeNull()
})
