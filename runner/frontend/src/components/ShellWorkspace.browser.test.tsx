import { createRef } from 'react'
import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import ShellWorkspace, { type ShellWorkspaceHandle } from './ShellWorkspace'
import { HostShellConsentRequired } from '../shellTransport'
import { FakeShell } from '../test/shellMock'
afterEach(() => localStorage.clear())
for (const width of [390, 3840]) {
  it(`fits owner shells at ${width}px with isolated terminals and phone tabs`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    const host = document.createElement('div'); host.style.cssText = 'position:fixed;inset:0;display:flex;overflow:hidden'; document.body.appendChild(host)
    const ref = createRef<ShellWorkspaceHandle>(); const shells: FakeShell[] = []
    const createShell = async () => { const shell = new FakeShell(`${shells.length + 1}`); shells.push(shell); return shell }
    const view = render(<><main style={{ flex: 1, minWidth: 0, marginTop: 48 }}><ShellWorkspace restoreShells={async () => []} ref={ref} sessionId="session" accountId="owner" createShell={createShell}><div data-testid="chat">Conversation</div></ShellWorkspace></main>{width > 1000 && <aside data-testid="session-panel" style={{ flex: '0 0 340px' }}>Session panel</aside>}</>, { container: host })
    await act(async () => { ref.current?.open() })
    await waitFor(() => expect(shells[0].resizes.length).toBeGreaterThan(0))
    const dock = view.getByLabelText('Session shells')
    if (width === 390) { expect(Math.round(dock.getBoundingClientRect().top)).toBe(0); expect(Math.round(dock.getBoundingClientRect().width)).toBe(390); expect(Math.round(dock.getBoundingClientRect().height)).toBe(844) }
    else { expect(Math.round(dock.getBoundingClientRect().height)).toBeGreaterThan(650); expect(Math.round(dock.getBoundingClientRect().width)).toBe(3500) }
    fireEvent.click(view.getByLabelText('Split right'))
    await waitFor(() => expect(shells).toHaveLength(2))
    await waitFor(() => expect(shells[1].output).toBeDefined())
    await act(async () => { shells[0].emit('\x1b[31mfirst-terminal\x1b[0m\r\n'); shells[1].emit('second-terminal\r\n') })
    const panes = view.container.querySelectorAll('.shell-pane')
    await waitFor(() => expect(panes[0].textContent).toContain('first-terminal'))
    await waitFor(() => expect(panes[1].textContent).toContain('second-terminal'))
    expect(panes[1].textContent).not.toContain('first-terminal')
    const textarea = panes[1].querySelector('textarea')!
    const clipboard = new DataTransfer(); clipboard.setData('text/plain', 'phone paste 日本語')
    textarea.dispatchEvent(new ClipboardEvent('paste', { clipboardData: clipboard, bubbles: true, cancelable: true }))
    await waitFor(() => expect(shells[1].inputs.length).toBeGreaterThan(0))
    expect(new TextDecoder().decode(shells[1].inputs[shells[1].inputs.length - 1])).toContain('日本語')
    expect(shells[0].inputs).toHaveLength(0)
    const beforeComposition = shells[1].inputs.length
    fireEvent.compositionStart(textarea)
    textarea.value = '入力'
    fireEvent.compositionUpdate(textarea, { data: '入力' })
    fireEvent.compositionEnd(textarea, { data: '入力' })
    await waitFor(() => expect(shells[1].inputs.length).toBeGreaterThan(beforeComposition))
    expect(shells[1].inputs.slice(beforeComposition).map(b => new TextDecoder().decode(b)).join('')).toContain('入力')
    const inputCount = shells[1].inputs.length
    textarea.focus()
    await userEvent.keyboard('{Control>}{Backquote}{/Control}')
    expect(view.getByLabelText('Expand shell')).toBeDefined()
    expect(shells[1].inputs.length).toBe(inputCount)
    fireEvent.click(view.getByLabelText('Expand shell'))
    const livePanes = view.container.querySelectorAll('.shell-pane')
    if (width === 390) {
      expect(getComputedStyle(livePanes[0]).display).toBe('none')
      fireEvent.click(view.getByRole('tab', { name: 'sandbox · project-1' }))
      expect(getComputedStyle(livePanes[0]).display).not.toBe('none')
      const header = view.container.querySelector('.shell-header')!
      fireEvent.touchStart(header, { touches: [new Touch({ identifier: 1, target: header, clientY: 20 })] })
      fireEvent.touchEnd(header, { changedTouches: [new Touch({ identifier: 1, target: header, clientY: 150 })] })
      expect(view.getByLabelText('Expand shell')).toBeDefined()
    } else {
      fireEvent.click(view.getByLabelText('Split down'))
      await waitFor(() => expect(shells).toHaveLength(3))
      const divider = await view.findByRole('separator', { name: 'Resize shell columns' })
      const area = view.container.querySelector('.shell-panes')!.getBoundingClientRect()
      divider.setPointerCapture = () => {}
      fireEvent.pointerDown(divider, { pointerId: 1, clientX: area.left + area.width / 2 })
      fireEvent.pointerMove(divider, { pointerId: 1, clientX: area.left + area.width * 0.65 })
      fireEvent.pointerUp(divider, { pointerId: 1 })
      expect((view.container.querySelector('.shell-panes') as HTMLElement).style.gridTemplateColumns).toContain('65%')
      fireEvent.change(view.getByLabelText('Dock shell'), { target: { value: 'right' } })
      expect(Math.round(view.getByTestId('session-panel').getBoundingClientRect().width)).toBe(340)
      expect(view.getByTestId('chat').getBoundingClientRect().width).toBeLessThan(3000)
      fireEvent.change(view.getByLabelText('Dock shell'), { target: { value: 'maximized' } })
      expect(getComputedStyle(view.container.querySelector('.shell-chat')!).display).toBe('none')
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    await userEvent.keyboard('{Control>}{Backquote}{/Control}')
  })
}

for (const width of [390, 3840]) {
 it(`routes human input and retains the local screen at ${width}px`, async () => {
  await page.viewport(width, 844)
  const ref = createRef<ShellWorkspaceHandle>(); const shells: FakeShell[] = []
  const view = render(<div style={{height: '100dvh'}}><ShellWorkspace ref={ref} sessionId="focus" accountId="owner" restoreShells={async () => []} createShell={async () => { const s = new FakeShell(String(shells.length)); shells.push(s); return s }}><textarea aria-label="Chat composer" /></ShellWorkspace></div>)
  const chat = view.getByLabelText('Chat composer') as HTMLTextAreaElement
  chat.focus()
  await userEvent.keyboard('{Control>}{Backquote}{/Control}')
  await waitFor(() => expect(shells).toHaveLength(1))
  const terminal = view.container.querySelector('.xterm-helper-textarea') as HTMLTextAreaElement
  await waitFor(() => expect(document.activeElement).toBe(terminal))
  if (width === 390) expect(view.container.querySelector('.shell-chat')?.hasAttribute('inert')).toBe(true)
  await userEvent.keyboard('synthetic-command')
  expect(chat.value).toBe('')
  expect(shells[0].inputs.map(b => new TextDecoder().decode(b)).join('')).toContain('synthetic-command')
  expect(shells[0].activityCount).toBeGreaterThan(0)
  const humanActivity = shells[0].activityCount
  shells[0].activities.length = 0
  await act(async () => { shells[0].emit('BEFORE-COLLAPSE\r\n\x1b[5n\x1b[6n') })
  await waitFor(() => expect(shells[0].activities.length).toBeGreaterThan(0))
  expect(shells[0].activities.some(Boolean)).toBe(false)
  expect(shells[0].activityCount).toBe(humanActivity)
  fireEvent.click(view.getByLabelText('Collapse shell'))
  expect(document.activeElement).toBe(chat)
  await act(async () => { shells[0].emit('HIDDEN-OUTPUT\r\n') })
  fireEvent.click(view.getByLabelText('Expand shell'))
  expect(view.container.querySelector('.xterm-helper-textarea')).toBe(terminal)
  await waitFor(() => expect(document.activeElement).toBe(terminal))
  await waitFor(() => expect(view.container.textContent).toContain('HIDDEN-OUTPUT'))
  expect(view.container.textContent).toContain('BEFORE-COLLAPSE')
  fireEvent.click(view.getByLabelText('Split right'))
  await waitFor(() => expect(shells).toHaveLength(2))
  await waitFor(() => expect(document.activeElement).toBe(view.container.querySelectorAll('.xterm-helper-textarea')[1]))
  fireEvent.click(view.getByRole('tab', {name: 'sandbox · project-0'}))
  await waitFor(() => expect(document.activeElement).toBe(terminal))
 })
}
it('preserves terminal identity and captures the shortcut in a real popup', async () => {
 await page.viewport(3840, 1200)
 const ref = createRef<ShellWorkspaceHandle>(); const shell = new FakeShell('popup')
 const opened = vi.spyOn(window, 'open')
 const view = render(<div style={{height:'100dvh'}}><ShellWorkspace ref={ref} sessionId="popup" accountId="owner" restoreShells={async()=>[]} createShell={async()=>shell}><textarea aria-label="Chat" /></ShellWorkspace></div>)
 await act(async()=>{ref.current?.open()})
 const terminal = view.container.querySelector('.xterm-helper-textarea')!
 await act(async()=>{shell.emit('POPUP-SCREEN\r\n')})
 await waitFor(()=>expect(view.container.textContent).toContain('POPUP-SCREEN'))
 fireEvent.change(view.getByLabelText('Dock shell'),{target:{value:'pop-out'}})
 const popup = opened.mock.results[0].value as Window
 try {
  expect(popup.document.querySelector('.xterm-helper-textarea')).toBe(terminal)
  expect(popup.document.body.textContent).toContain('POPUP-SCREEN')
  const before = shell.inputs.length
  fireEvent.keyDown(popup,{ctrlKey:true,code:'Backquote'})
  expect(popup.document.querySelector('[aria-label="Expand shell"]')).not.toBeNull()
  expect(shell.inputs.length).toBe(before)
  fireEvent.keyDown(popup,{ctrlKey:true,code:'Backquote'})
  fireEvent.change(popup.document.querySelector('select')!,{target:{value:'bottom'}})
  expect(view.container.querySelector('.xterm-helper-textarea')).toBe(terminal)
  fireEvent.keyDown(window,{ctrlKey:true,code:'Backquote'})
  expect(view.getByLabelText('Expand shell')).toBeDefined()
 } finally {popup.close();opened.mockRestore()}
})

for (const width of [390,3840]) it(`fits host consent and restores focus after cancel at ${width}px`, async () => {
 await page.viewport(width,844)
 const challenge = {challenge_id: 'challenge', machine_id: 'authenticated-machine', account: 'actual-user', uid:1000, shell:'/bin/bash', cwd:'/prepared/project/'.repeat(8)}
 const ref=createRef<ShellWorkspaceHandle>()
 const view=render(<div style={{height:'100dvh'}}><ShellWorkspace ref={ref} sessionId="consent" accountId="owner" restoreShells={async()=>[]} createShell={async()=>{throw new HostShellConsentRequired(challenge)}}><textarea aria-label="Composer" /></ShellWorkspace></div>)
 const composer=view.getByLabelText('Composer');composer.focus()
 await act(async()=>{ref.current?.open()})
 const dialog=view.getByRole('dialog');expect(dialog.textContent).toContain('authenticated-machine');expect(dialog.textContent).toContain('actual-user (UID 1000)')
 expect(dialog.getBoundingClientRect().left).toBeGreaterThanOrEqual(0);expect(dialog.getBoundingClientRect().right).toBeLessThanOrEqual(width)
 expect(view.container.querySelector('.shell-chat')?.hasAttribute('inert')).toBe(true)
 fireEvent.click(view.getByText('Cancel'));await waitFor(()=>expect(document.activeElement).toBe(composer))
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
