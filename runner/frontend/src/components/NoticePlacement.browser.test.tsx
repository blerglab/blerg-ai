import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import type { ShellWorkspaceHandle } from './ShellWorkspace'
import ToastContainer from './ToastContainer'
import { useToastStore } from '../hooks/useToastStore'
import '../index.css'
vi.mock('../sound', () => ({ playToastSound: vi.fn(), playMessageSound: vi.fn() }))
afterEach(() => { useToastStore.setState({ toasts: [] }); vi.unstubAllGlobals() })

for (const width of [390, 3840]) it(`keeps shell errors and every toast clear of typing at ${width}px`, async () => {
  await page.viewport(width, 900)
  const ref = createRef<ShellWorkspaceHandle>()
  const unavailable = async () => { throw new Error('Shell is unavailable for this session.') }
  render(<MemoryRouter><div style={{ position: 'fixed', inset: '48px 0 0' }}>
    <ShellWorkspace ref={ref} accountId="owner" sessionId="notice" restoreShells={async () => []} createShell={unavailable}>
      <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
        <input aria-label="Search conversation" style={{ marginTop: 100 }} />
        <div style={{ flex: 1 }}>Conversation</div>
        <div className="agent-composer" data-testid="composer-region" style={{ minHeight: 180 }}>
          <textarea aria-label="Session composer" /><button>Attach files</button><div>attached.txt</div>
        </div>
      </div>
    </ShellWorkspace>
  </div><ToastContainer /></MemoryRouter>)
  const composer = screen.getByRole('textbox', { name: 'Session composer' })
  composer.focus()
  const before = screen.getByTestId('composer-region').getBoundingClientRect()
  await act(async () => { ref.current?.open() })
  const alert = await screen.findByRole('alertdialog')
  const box = alert.getBoundingClientRect()
  expect(box.left).toBeGreaterThanOrEqual(0)
  expect(box.right).toBeLessThanOrEqual(width)
  expect(screen.getByRole('button', { name: 'Dismiss' })).toHaveFocus()
  fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
  expect(screen.getByTestId('composer-region').getBoundingClientRect().top).toBe(before.top)
  expect(document.activeElement).toBe(composer)
  await userEvent.keyboard('still typing')
  expect((composer as HTMLTextAreaElement).value).toBe('still typing')
  expect(screen.queryByRole('alertdialog')).toBeNull()

  // Queue enough notices to overflow the old unbounded stack, including long bodies.
  act(() => { for (let i = 0; i < 15; i++) useToastStore.getState().addToast({ title: `Notice ${i}`, body: 'A long notice. '.repeat(35), variant: i % 2 ? 'message' : 'session', ttlMs: 60000, action: { label: 'Undo', onClick: vi.fn() } }) })
  const assertClear = (protectedBox: DOMRect) => {
    const region = screen.getByRole('region', { name: 'Notifications' }).getBoundingClientRect()
    expect(region.bottom).toBeLessThanOrEqual(protectedBox.top - 4)
    expect(region.right).toBeLessThanOrEqual(width)
    expect(region.left).toBeGreaterThanOrEqual(0)
    expect(screen.getByTestId('composer-region').getBoundingClientRect().top).toBe(before.top)
  }
  await waitFor(() => assertClear(before))
  const search = screen.getByRole('textbox', { name: 'Search conversation' })
  search.focus()
  await waitFor(() => assertClear(search.getBoundingClientRect()))
  composer.focus()
  // Simulate a visual viewport shrinking and moving when the phone keyboard opens.
  const viewport = new EventTarget()
  Object.assign(viewport, { offsetTop: 40, offsetLeft: 0, width, height: 480 })
  vi.stubGlobal('visualViewport', viewport)
  fireEvent(window, new Event('resize'))
  await waitFor(() => expect(screen.getByRole('region', { name: 'Notifications' }).getBoundingClientRect().bottom).toBeLessThanOrEqual(512))
  expect(document.activeElement).toBe(composer)
  fireEvent.click(screen.getAllByRole('button', { name: 'Dismiss notification' })[0])
  expect(useToastStore.getState().toasts).toHaveLength(14)
})

for (const width of [390, 3840]) it(`keeps shell connection notices above their input at ${width}px`, async () => {
  await page.viewport(width, 900)
  const { FakeShell } = await import('../test/shellMock')
  const shell = new FakeShell('notice'); shell.connectionState = 'connecting'
  const ref = createRef<ShellWorkspaceHandle>()
  render(<div style={{ height: '100dvh' }}><ShellWorkspace ref={ref} sessionId="connection" accountId="owner" restoreShells={async () => []} createShell={async () => shell}><textarea aria-label="Composer" /></ShellWorkspace></div>)
  await act(async () => { ref.current?.open() })
  await waitFor(() => expect(shell.state).toBeDefined())
  await screen.findByText('Loading shell')
  expect(screen.getByText('Connecting…')).toBeInTheDocument()
  const connection = document.querySelector('.shell-connection')!.getBoundingClientRect()
  expect(connection.bottom).toBeLessThanOrEqual(screen.getByRole('region', { name: 'shell' }).getBoundingClientRect().top)
  for (const state of ['syncing', 'disconnected', 'closed'] as const) {
    act(() => { shell.state?.(state) })
    const status = document.querySelector('.shell-connection')!.getBoundingClientRect()
    const terminal = document.querySelector('.shell-terminal-renderer')!.getBoundingClientRect()
    expect(status.bottom).toBeLessThanOrEqual(terminal.top)
    if (state === 'syncing') {
      const loader = screen.getByRole('region', { name: 'shell' }).getBoundingClientRect()
      expect(loader.top).toBeGreaterThanOrEqual(status.bottom)
    }
  }
  fireEvent.click(screen.getByRole('button', { name: 'Close Shell' }))
})

for (const width of [390, 3840]) it(`protects the real chat composer and its attachments at ${width}px`, async () => {
  await page.viewport(width, 900)
  const { ChatView } = await import('@blerglab/chat')
  const { createFakeTransport } = await import('../../../../packages/chat/src/test/fakeTransport')
  const { meta, resetChatState, seed, SESSION } = await import('../../../../packages/chat/src/test/chat')
  resetChatState(); seed()
  const fake = createFakeTransport(), ref = createRef<ShellWorkspaceHandle>()
  render(<MemoryRouter><div style={{ height: '100dvh' }}><ShellWorkspace ref={ref} sessionId={SESSION} accountId="owner" restoreShells={async () => []} createShell={async () => { throw new Error('Shell is unavailable for this session.') }}>
    <ChatView session={SESSION} transport={fake.transport} meta={meta()} />
  </ShellWorkspace></div><ToastContainer /></MemoryRouter>)
  const composer = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement
  fireEvent.change(screen.getByTestId('attach-input'), { target: { files: [new File(['draft'], 'draft.txt', { type: 'text/plain' })] } })
  const attachment = await screen.findByRole('list', { name: 'Attached files' })
  await waitFor(() => expect(attachment.textContent).toContain('draft.txt'))
  // Measure once the upload has settled (uploads start from the session's queue, #231), so only
  // the notices could move the composer.
  await waitFor(() => expect(attachment.querySelector('.attach-chip.done')).not.toBeNull())
  composer.focus()
  const before = composer.getBoundingClientRect()
  await act(async () => { ref.current?.open() })
  await screen.findByRole('alertdialog')
  fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
  act(() => { useToastStore.getState().addToast({ title: 'Needs you', body: 'Another session is waiting.', ttlMs: 60000 }) })
  await waitFor(() => {
    const noticeBox = screen.getByRole('region', { name: 'Notifications' }).getBoundingClientRect()
    expect(noticeBox.bottom).toBeLessThanOrEqual(document.querySelector('.agent-composer')!.getBoundingClientRect().top - 4)
    expect(noticeBox.bottom).toBeLessThanOrEqual(attachment.getBoundingClientRect().top)
  })
  expect(composer.getBoundingClientRect().top).toBe(before.top)
  expect(document.activeElement).toBe(composer)
  await userEvent.keyboard('Draft stays editable')
  expect(composer.value).toBe('Draft stays editable')
})
